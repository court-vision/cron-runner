package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cron-runner/internal/pipeline"
	"cron-runner/internal/reporter"

	"github.com/rs/zerolog"
)

// TriggerTask posts to an endpoint once and returns.
// Retries are handled by the pipeline client.
// If Reporter is set, an execution report is pushed to the data-platform after each run.
type TriggerTask struct {
	Client   *pipeline.Client
	Endpoint string // path, plus optional query string, appended to the client's base URL
	JobName  string // optional; job_name sent in run reports. Defaults to Endpoint's last path segment.
	Log      zerolog.Logger
	Reporter *reporter.Reporter // optional; nil = no push reporting
}

func (t *TriggerTask) Name() string { return "trigger:" + t.Endpoint }

// ReportName is the job_name sent to the data-platform's /v1/internal/cron/job-runs.
func (t *TriggerTask) ReportName() string {
	if t.JobName != "" {
		return t.JobName
	}
	return jobNameFromEndpoint(t.Endpoint)
}

func (t *TriggerTask) Run(ctx context.Context) error {
	triggeredAt := time.Now()
	result := t.Client.TriggerEndpoint(ctx, t.Endpoint)
	completedAt := time.Now()
	durationMs := completedAt.Sub(triggeredAt).Milliseconds()

	if t.Reporter != nil {
		var httpStatus *int
		if result.StatusCode != 0 {
			s := result.StatusCode
			httpStatus = &s
		}
		var errMsg *string
		if result.Error != nil {
			s := result.Error.Error()
			errMsg = &s
		}
		res := "success"
		if !result.Success {
			res = "failure"
		}
		t.Reporter.Report(
			t.ReportName(),
			triggeredAt,
			completedAt,
			durationMs,
			res,
			httpStatus,
			result.Attempts,
			errMsg,
			result.ResponseBody,
		)
	}

	if !result.Success {
		return fmt.Errorf("trigger failed after %d attempts: %w", result.Attempts, result.Error)
	}
	t.Log.Info().
		Int("attempts", result.Attempts).
		Int("status_code", result.StatusCode).
		Dur("duration", result.Duration).
		Msg("trigger_succeeded")
	return nil
}

// jobNameFromEndpoint extracts a short job name from the endpoint path,
// ignoring any query string, fragment or trailing slash.
//
//	"/v1/internal/pipelines/pre-game"                    → "pre-game"
//	"/v1/internal/pipelines/game-start-times?source=cdn" → "game-start-times"
func jobNameFromEndpoint(endpoint string) string {
	path := endpoint
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
