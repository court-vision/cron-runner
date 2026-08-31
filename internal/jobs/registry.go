package jobs

import (
	"time"

	"cron-runner/internal/pipeline"
	"cron-runner/internal/reporter"
	"cron-runner/internal/scheduler"
	"cron-runner/internal/task"

	"github.com/rs/zerolog"
)

// Job schedules — read this before editing.
//
// Every schedule is a cron expression evaluated in UTC (the scheduler is built
// with gocron.WithLocation(time.UTC)). NBA game times are US/Eastern, which is
// UTC-4 (EDT) from mid-March to early November and UTC-5 (EST) for the rest of
// the season, so each window below is padded by an hour to hold across the DST
// switch. When WithSeconds is true the expression has six fields and the first
// field is seconds.
//
// Every endpoint here is self-gating on the data-platform side: it inspects the
// game schedule, pipeline-run history and time windows and returns HTTP 200
// immediately ("no games", "outside window", "already ran") when there is
// nothing to do. Firing a job outside its useful window, or all summer, is
// harmless. The cron-runner never decides whether to run — only when to ask.
//
//	pre-game       Injury status, breakout detection, lineup alerts. The endpoint
//	               opens 150 min before the first tip of the NBA date. Every
//	               15 min 13:00–01:45 UTC (9 AM–9:45 PM EDT / 8 AM–8:45 PM EST),
//	               early enough for a noon-ET holiday matinee in either offset.
//	live-stats     Live box scores → nba.live_player_stats. Every 30 s
//	               16:00–07:59 UTC (noon–3:59 AM EDT / 11 AM–2:59 AM EST) so late
//	               West-coast finishes are covered in EST. The endpoint no-ops
//	               until 15 min before the first tip.
//	post-game      Game logs, season/rolling/advanced stats, ownership, matchup
//	               scores, schedule. The endpoint runs only inside a window that
//	               opens 150 min after the latest tip and lasts 210 min, once all
//	               games are Final; ESPN-gated pipelines wait for ESPN's scoring
//	               period to advance (2:30 AM CST fallback). Every 15 min
//	               02:00–13:45 UTC (10 PM–9:45 AM EDT / 9 PM–8:45 AM EST).
//	schedule-sync  Refreshes future rows of nba.games (tip-off times, moved or
//	               postponed games) from the NBA CDN schedule feed. Mondays
//	               12:00 UTC (8 AM EDT / 7 AM EST).
//	playoffs       Playoff bracket / series standings → nba.playoff_series.
//	               Daily 06:00 UTC (2 AM EDT / 1 AM EST), after the last game.
//	preseason-market  Draft-market snapshot (ESPN draft ranks, ADP, auction
//	               values — and stat projections once ESPN publishes them) →
//	               nba.draft_market / nba.player_projections. Daily 11:00 UTC
//	               (7 AM EDT / 6 AM EST). The endpoint no-ops outside the
//	               Aug 15–Oct 31 draft-prep window and while the public league
//	               has not rolled to the target season.
//	deploy         GitHub repository_dispatch that promotes backend and
//	               data-platform to production. Daily 08:00 UTC (4 AM EDT /
//	               3 AM EST). The backend also auto-deploys on push, so in
//	               practice this is the data-platform's nightly release.

// RegisterAll returns all scheduled job definitions.
// To add a new job, append a JobDef here — no other changes needed.
func RegisterAll(client *pipeline.Client, rep *reporter.Reporter, log zerolog.Logger) []scheduler.JobDef {
	// trigger builds the fire-and-forget task shared by every job. name is both
	// the log field and the job_name reported to /v1/internal/cron/job-runs.
	trigger := func(name, endpoint string) *task.TriggerTask {
		return &task.TriggerTask{
			Client:   client,
			Endpoint: endpoint,
			JobName:  name,
			Log:      log.With().Str("job", name).Logger(),
			Reporter: rep,
		}
	}

	return []scheduler.JobDef{
		{
			Name:      "pre-game",
			Schedule:  "0/15 13-23,0-1 * * *",
			Singleton: true,
			Timeout:   5 * time.Minute, // endpoint returns at once; pipelines run in the background
			Task:      trigger("pre-game", "/v1/internal/pipelines/pre-game"),
		},
		{
			Name:        "live-stats",
			Schedule:    "*/30 * 16-23,0-7 * * *",
			WithSeconds: true,
			Singleton:   true,
			Task:        trigger("live-stats", "/v1/internal/pipelines/live-stats"),
		},
		{
			Name:      "post-game",
			Schedule:  "0/15 2-13 * * *",
			Singleton: true,
			Timeout:   5 * time.Minute,
			Task:      trigger("post-game", "/v1/internal/pipelines/post-game"),
		},
		{
			Name:      "schedule-sync",
			Schedule:  "0 12 * * 1", // Mondays 12:00 UTC = 8 AM EDT / 7 AM EST
			Singleton: true,
			Timeout:   5 * time.Minute,
			Task:      trigger("schedule-sync", "/v1/internal/pipelines/game-start-times?source=cdn"),
		},
		{
			Name:      "playoffs",
			Schedule:  "0 6 * * *", // 06:00 UTC = 2 AM EDT / 1 AM EST — refresh bracket after games finish
			Singleton: true,
			Timeout:   5 * time.Minute,
			Task:      trigger("playoffs", "/v1/internal/pipelines/playoffs"),
		},
		{
			Name:      "preseason-market",
			Schedule:  "0 11 * * *", // 11:00 UTC = 7 AM EDT / 6 AM EST — daily draft-prep snapshot; endpoint self-gates
			Singleton: true,
			Timeout:   5 * time.Minute,
			Task:      trigger("preseason-market", "/v1/internal/pipelines/preseason-market"),
		},
		{
			Name:      "deploy",
			Schedule:  "0 8 * * *", // 08:00 UTC = 3 AM CDT / 2 AM CST
			Singleton: true,
			Timeout:   5 * time.Minute,
			Task:      trigger("deploy", "/v1/internal/pipelines/deploy"),
		},
	}
}
