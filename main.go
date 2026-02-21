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
		Str("job", cfg.Job).
		Msg("cron-runner starting")

	// Create pipeline client
	pipelineClient := pipeline.NewClient(cfg, log)

	ctx := context.Background()

	switch cfg.Job {
	case config.JobPostGame:
		runPostGameMode(ctx, pipelineClient, cfg.Endpoint, log)
	case config.JobAlert:
		runAlertMode(ctx, pipelineClient, cfg.Endpoint, log)
	case config.JobFireAndForget:
		runFireAndForget(ctx, pipelineClient, log)
	case config.JobPipeline:
		runWithPolling(ctx, pipelineClient, log)
	}
}

// runPostGameMode triggers the post-game pipeline endpoint and exits.
func runPostGameMode(ctx context.Context, client *pipeline.Client, endpoint string, log zerolog.Logger) {
	log.Info().
		Str("endpoint", endpoint).
		Msg("post_game_mode_triggered")

	result := client.TriggerEndpoint(ctx, endpoint)

	if result.Success {
		log.Info().
			Int("attempts", result.Attempts).
			Int("status_code", result.StatusCode).
			Dur("duration", result.Duration).
			Msg("post-game pipeline endpoint triggered successfully")
		os.Exit(0)
	} else {
		log.Error().
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Err(result.Error).
			Msg("failed to trigger post-game pipeline endpoint")
		os.Exit(1)
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
