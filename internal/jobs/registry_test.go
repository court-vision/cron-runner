package jobs

import (
	"io"
	"testing"
	"time"

	"cron-runner/internal/config"
	"cron-runner/internal/pipeline"
	"cron-runner/internal/reporter"
	"cron-runner/internal/scheduler"
	"cron-runner/internal/task"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog"
)

// testRegistry builds the registry the same way main.go does, with a client
// that never leaves the process (nothing is triggered — only definitions are
// inspected).
func testRegistry(t *testing.T) []scheduler.JobDef {
	t.Helper()
	log := zerolog.New(io.Discard)
	cfg := &config.Config{
		BackendURL:     "http://data.test",
		PipelineAuth:   "test-token",
		MaxRetries:     0,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		BackoffFactor:  1,
		RequestTimeout: time.Second,
	}
	client := pipeline.NewClient(cfg, log)
	rep := reporter.New(cfg.BackendURL, cfg.PipelineAuth, log)
	return RegisterAll(client, rep, log)
}

// expected mirrors the registry one-for-one. Update it deliberately when a
// schedule changes — the point of this table is to make such edits visible.
var expected = []struct {
	name        string
	schedule    string
	withSeconds bool
	singleton   bool
	timeout     time.Duration
	endpoint    string
}{
	{"pre-game", "0/15 13-23,0-1 * * *", false, true, 5 * time.Minute, "/v1/internal/pipelines/pre-game"},
	{"live-stats", "*/30 * 16-23,0-7 * * *", true, true, 0, "/v1/internal/pipelines/live-stats"},
	{"post-game", "0/15 2-13 * * *", false, true, 5 * time.Minute, "/v1/internal/pipelines/post-game"},
	{"schedule-sync", "0 12 * * 1", false, true, 5 * time.Minute, "/v1/internal/pipelines/game-start-times?source=cdn"},
	{"playoffs", "0 6 * * *", false, true, 5 * time.Minute, "/v1/internal/pipelines/playoffs"},
	{"deploy", "0 8 * * *", false, true, 5 * time.Minute, "/v1/internal/pipelines/deploy"},
}

func TestRegisterAllDefinitions(t *testing.T) {
	defs := testRegistry(t)
	if len(defs) != len(expected) {
		t.Fatalf("expected %d jobs, got %d", len(expected), len(defs))
	}

	seen := make(map[string]bool, len(defs))
	for i, want := range expected {
		got := defs[i]
		if got.Name != want.name {
			t.Errorf("job %d: name = %q, want %q", i, got.Name, want.name)
		}
		if seen[got.Name] {
			t.Errorf("duplicate job name %q", got.Name)
		}
		seen[got.Name] = true

		if got.Schedule != want.schedule {
			t.Errorf("%s: schedule = %q, want %q", want.name, got.Schedule, want.schedule)
		}
		if got.WithSeconds != want.withSeconds {
			t.Errorf("%s: withSeconds = %v, want %v", want.name, got.WithSeconds, want.withSeconds)
		}
		if got.Singleton != want.singleton {
			t.Errorf("%s: singleton = %v, want %v", want.name, got.Singleton, want.singleton)
		}
		if got.Timeout != want.timeout {
			t.Errorf("%s: timeout = %v, want %v", want.name, got.Timeout, want.timeout)
		}

		tt, ok := got.Task.(*task.TriggerTask)
		if !ok {
			t.Errorf("%s: task is %T, want *task.TriggerTask", want.name, got.Task)
			continue
		}
		if tt.Endpoint != want.endpoint {
			t.Errorf("%s: endpoint = %q, want %q", want.name, tt.Endpoint, want.endpoint)
		}
		if tt.ReportName() != want.name {
			t.Errorf("%s: reports to job-runs as %q, want the job name", want.name, tt.ReportName())
		}
		if tt.Client == nil || tt.Reporter == nil {
			t.Errorf("%s: client/reporter not wired", want.name)
		}
	}
}

// TestRegisterAllSchedulesParse registers every definition into a real
// Scheduler. gocron validates the crontab (with the right seconds flag) inside
// NewJob, so a bad expression surfaces here instead of at boot.
func TestRegisterAllSchedulesParse(t *testing.T) {
	sched := scheduler.New(zerolog.New(io.Discard))
	for _, def := range testRegistry(t) {
		if err := sched.Register(def); err != nil {
			t.Errorf("%s: register failed for %q: %v", def.Name, def.Schedule, err)
		}
	}
}

// TestScheduleNextFire pins the UTC semantics of each expression using the
// same parser gocron uses at runtime. Instants sit on window edges so an
// off-by-one-hour edit fails loudly.
func TestScheduleNextFire(t *testing.T) {
	byName := make(map[string]scheduler.JobDef)
	for _, def := range testRegistry(t) {
		byName[def.Name] = def
	}
	utc := func(y int, m time.Month, d, hh, mm, ss int) time.Time {
		return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
	}

	cases := []struct {
		job  string
		from time.Time
		want time.Time
	}{
		// pre-game: first slot of the day is 13:00 UTC, last is 01:45 UTC.
		{"pre-game", utc(2026, time.December, 25, 2, 0, 0), utc(2026, time.December, 25, 13, 0, 0)},
		{"pre-game", utc(2026, time.December, 25, 23, 50, 0), utc(2026, time.December, 26, 0, 0, 0)},
		{"pre-game", utc(2026, time.December, 26, 1, 45, 0), utc(2026, time.December, 26, 13, 0, 0)},
		// live-stats: 30-second cadence, quiet 08:00–15:59 UTC.
		{"live-stats", utc(2026, time.November, 1, 16, 0, 0), utc(2026, time.November, 1, 16, 0, 30)},
		{"live-stats", utc(2026, time.November, 1, 7, 59, 30), utc(2026, time.November, 1, 16, 0, 0)},
		// post-game: 02:00–13:45 UTC.
		{"post-game", utc(2026, time.November, 1, 1, 50, 0), utc(2026, time.November, 1, 2, 0, 0)},
		{"post-game", utc(2026, time.November, 1, 13, 45, 0), utc(2026, time.November, 2, 2, 0, 0)},
		// schedule-sync: Mondays 12:00 UTC (2026-08-31 is a Monday).
		{"schedule-sync", utc(2026, time.August, 25, 0, 0, 0), utc(2026, time.August, 31, 12, 0, 0)},
		{"schedule-sync", utc(2026, time.August, 31, 12, 0, 0), utc(2026, time.September, 7, 12, 0, 0)},
		// playoffs / deploy: once daily.
		{"playoffs", utc(2026, time.April, 20, 6, 0, 0), utc(2026, time.April, 21, 6, 0, 0)},
		{"deploy", utc(2026, time.August, 25, 7, 59, 0), utc(2026, time.August, 25, 8, 0, 0)},
	}

	for _, c := range cases {
		def, ok := byName[c.job]
		if !ok {
			t.Fatalf("job %q not in registry", c.job)
		}
		cron := gocron.NewDefaultCron(def.WithSeconds)
		if err := cron.IsValid(def.Schedule, time.UTC, c.from); err != nil {
			t.Fatalf("%s: %q invalid: %v", c.job, def.Schedule, err)
		}
		if got := cron.Next(c.from); !got.Equal(c.want) {
			t.Errorf("%s: next after %s = %s, want %s", c.job, c.from.Format(time.RFC3339), got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
	}
}
