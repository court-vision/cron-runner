package task

import (
	"context"
	"fmt"

	"cron-runner/internal/pipeline"

	"github.com/rs/zerolog"
)

// TriggerTask posts to an endpoint once and returns.
// Retries are handled by the pipeline client.
type TriggerTask struct {
	Client   *pipeline.Client
	Endpoint string
	Log      zerolog.Logger
}

func (t *TriggerTask) Name() string { return "trigger:" + t.Endpoint }

func (t *TriggerTask) Run(ctx context.Context) error {
	result := t.Client.TriggerEndpoint(ctx, t.Endpoint)
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
