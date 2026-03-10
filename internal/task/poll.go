package task

import (
	"context"
	"fmt"

	"cron-runner/internal/pipeline"

	"github.com/rs/zerolog"
)

// PollTask starts a pipeline job at an endpoint and polls until it completes.
type PollTask struct {
	Client   *pipeline.Client
	Endpoint string
	Log      zerolog.Logger
}

func (t *PollTask) Name() string { return "poll:" + t.Endpoint }

func (t *PollTask) Run(ctx context.Context) error {
	result := t.Client.TriggerAll(ctx, t.Endpoint)
	if !result.Success {
		return fmt.Errorf("poll failed after %d attempts: %w", result.Attempts, result.Error)
	}
	t.Log.Info().
		Str("job_id", result.JobID).
		Int("attempts", result.Attempts).
		Dur("duration", result.Duration).
		Msg("poll_succeeded")
	return nil
}
