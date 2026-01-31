package pipeline

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"cron-runner/internal/config"
	"cron-runner/internal/retry"

	"github.com/rs/zerolog"
)

// Client handles communication with the backend pipeline API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	authToken  string
	retryCfg   retry.Config
	log        zerolog.Logger
}

// NewClient creates a new pipeline client.
func NewClient(cfg *config.Config, log zerolog.Logger) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: cfg.RequestTimeout},
		baseURL:    cfg.BackendURL,
		authToken:  cfg.PipelineAuth,
		retryCfg: retry.Config{
			MaxRetries:     cfg.MaxRetries,
			InitialBackoff: cfg.InitialBackoff,
			MaxBackoff:     cfg.MaxBackoff,
			BackoffFactor:  cfg.BackoffFactor,
		},
		log: log.With().Str("component", "pipeline-client").Logger(),
	}
}

// TriggerResult contains the outcome of a pipeline trigger.
type TriggerResult struct {
	Success      bool
	StatusCode   int
	ResponseBody string
	Attempts     int
	Duration     time.Duration
	Error        error
}

// TriggerAll triggers all pipelines via the backend API.
func (c *Client) TriggerAll(ctx context.Context) TriggerResult {
	url := c.baseURL + "/v1/internal/pipelines/all"

	c.log.Info().
		Str("url", url).
		Msg("triggering all pipelines")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		c.log.Error().Err(err).Msg("failed to create request")
		return TriggerResult{Error: fmt.Errorf("failed to create request: %w", err)}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.authToken)

	result := retry.Do(ctx, c.httpClient, req, c.retryCfg, c.log)

	triggerResult := TriggerResult{
		Attempts: result.Attempts,
		Duration: result.TotalTime,
		Error:    result.FinalError,
	}

	if result.FinalError != nil {
		c.log.Error().
			Err(result.FinalError).
			Int("attempts", result.Attempts).
			Dur("duration", result.TotalTime).
			Msg("pipeline trigger failed after all retries")
		return triggerResult
	}

	if result.Response != nil {
		triggerResult.StatusCode = result.Response.StatusCode
		defer result.Response.Body.Close()

		body, _ := io.ReadAll(result.Response.Body)
		triggerResult.ResponseBody = string(body)

		if result.Response.StatusCode >= 200 && result.Response.StatusCode < 300 {
			triggerResult.Success = true
			c.log.Info().
				Int("status", result.Response.StatusCode).
				Int("attempts", result.Attempts).
				Dur("duration", result.TotalTime).
				Msg("pipeline triggered successfully")
		} else {
			c.log.Error().
				Int("status", result.Response.StatusCode).
				Str("body", triggerResult.ResponseBody).
				Int("attempts", result.Attempts).
				Msg("pipeline trigger failed with non-success status")
		}
	}

	return triggerResult
}
