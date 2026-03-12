package task

import (
	"context"
	"fmt"
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
	Endpoint string
	Log      zerolog.Logger
	Reporter *reporter.Reporter // optional; nil = no push reporting
}

func (t *TriggerTask) Name() string { return "trigger:" + t.Endpoint }

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
			jobNameFromEndpoint(t.Endpoint),
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

// jobNameFromEndpoint extracts a short job name from the endpoint path.
// e.g. "/v1/internal/pipelines/pre-game" → "pre-game"
func jobNameFromEndpoint(endpoint string) string {
	for i := len(endpoint) - 1; i >= 0; i-- {
		if endpoint[i] == '/' {
			return endpoint[i+1:]
		}
	}
	return endpoint
}
