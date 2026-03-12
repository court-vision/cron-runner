package jobs

import (
	"time"

	"cron-runner/internal/pipeline"
	"cron-runner/internal/reporter"
	"cron-runner/internal/scheduler"
	"cron-runner/internal/task"

	"github.com/rs/zerolog"
)

// RegisterAll returns all scheduled job definitions.
// To add a new job, append a JobDef here — no other changes needed.
func RegisterAll(client *pipeline.Client, rep *reporter.Reporter, log zerolog.Logger) []scheduler.JobDef {
	return []scheduler.JobDef{
		{
			Name:      "pre-game",
			Schedule:  "0/15 14-23,0-1 * * *",
			Singleton: true,
			Timeout:   20 * time.Minute,
			Task: &task.TriggerTask{
				Client:   client,
				Endpoint: "/v1/internal/pipelines/pre-game",
				Log:      log.With().Str("job", "pre-game").Logger(),
				Reporter: rep,
			},
		},
		{
			Name:        "live-stats",
			Schedule:    "*/30 * 16-23,0-6 * * *",
			WithSeconds: true,
			Singleton:   true,
			Task: &task.TriggerTask{
				Client:   client,
				Endpoint: "/v1/internal/pipelines/live-stats",
				Log:      log.With().Str("job", "live-stats").Logger(),
				Reporter: rep,
			},
		},
		{
			Name:      "post-game",
			Schedule:  "0/15 3-9 * * *",
			Singleton: true,
			Timeout:   20 * time.Minute,
			Task: &task.TriggerTask{
				Client:   client,
				Endpoint: "/v1/internal/pipelines/post-game",
				Log:      log.With().Str("job", "post-game").Logger(),
				Reporter: rep,
			},
		},
	}
}
