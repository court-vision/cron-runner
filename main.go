package main

import (
	"context"
	"os"

	"cron-runner/internal/config"
	"cron-runner/internal/logger"
	"cron-runner/internal/pipeline"

	"github.com/rs/zerolog"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		os.Stderr.WriteString("Configuration error: " + err.Error() + "\n")
		os.Exit(1)
	}

	// Initialize structured logger
	log := logger.New(cfg.LogLevel, cfg.LogJSON)
	log.Info().
		Str("backend_url", cfg.BackendURL).
		Int("max_retries", cfg.MaxRetries).
		Dur("initial_backoff", cfg.InitialBackoff).
		Dur("request_timeout", cfg.RequestTimeout).
		Bool("fire_and_forget", cfg.FireAndForget).
		Bool("alert_mode", cfg.AlertMode).
		Msg("cron-runner starting")

	// Create pipeline client
	pipelineClient := pipeline.NewClient(cfg, log)

	ctx := context.Background()

	if cfg.AlertMode {
		// Alert mode: trigger the lineup alerts endpoint and exit
		runAlertMode(ctx, pipelineClient, cfg.AlertEndpoint, log)
	} else if cfg.FireAndForget {
		// Fire-and-forget: start job and exit immediately
		runFireAndForget(ctx, pipelineClient, log)
	} else {
		// Polling: start job and poll until completion
		runWithPolling(ctx, pipelineClient, log)
	}
}

// runAlertMode triggers the lineup alerts endpoint and exits.
func runAlertMode(ctx context.Context, client *pipeline.Client, endpoint string, log zerolog.Logger) {
	log.Info().
		Str("endpoint", endpoint).
		Msg("alert_mode_triggered")

	result := client.TriggerEndpoint(ctx, endpoint)

	if result.Success {
		log.Info().
			Int("attempts", result.Attempts).
			Int("status_code", result.StatusCode).
			Dur("duration", result.Duration).
			Msg("lineup alert triggered successfully")
		os.Exit(0)
	} else {
		log.Error().
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Err(result.Error).
			Msg("failed to trigger lineup alert")
		os.Exit(1)
	}
}

// runFireAndForget starts the pipeline job and exits immediately without waiting.
func runFireAndForget(ctx context.Context, client *pipeline.Client, log zerolog.Logger) {
	log.Info().Msg("running in fire-and-forget mode")

	result := client.StartJob(ctx)

	if result.Success {
		log.Info().
			Str("job_id", result.JobID).
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Msg("pipeline job started successfully (not waiting for completion)")
		os.Exit(0)
	} else {
		log.Error().
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Err(result.Error).
			Msg("failed to start pipeline job")
		os.Exit(1)
	}
}

// runWithPolling starts the pipeline job and waits for completion.
func runWithPolling(ctx context.Context, client *pipeline.Client, log zerolog.Logger) {
	log.Info().Msg("running with polling (waiting for completion)")

	result := client.TriggerAll(ctx)

	if result.Success {
		log.Info().
			Str("job_id", result.JobID).
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Msg("pipeline completed successfully")
		os.Exit(0)
	} else {
		logEvent := log.Error().
			Str("job_id", result.JobID).
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Err(result.Error)
		if result.JobDetails != nil {
			logEvent = logEvent.
				Int("pipelines_failed", result.JobDetails.PipelinesFailed).
				Int("pipelines_completed", result.JobDetails.PipelinesCompleted).
				Str("job_status", result.JobDetails.Status)
		}
		logEvent.Msg("pipeline trigger failed")
		os.Exit(1)
	}
}
