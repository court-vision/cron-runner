package main

import (
	"context"
	"flag"
	"os"

	"cron-runner/internal/config"
	"cron-runner/internal/logger"
	"cron-runner/internal/pipeline"

	"github.com/rs/zerolog"
)

func main() {
	// Parse command line flags
	fireAndForget := flag.Bool("fire-and-forget", false, "Start pipelines and exit immediately without waiting for completion")
	flag.Parse()

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
		Bool("fire_and_forget", *fireAndForget).
		Msg("cron-runner starting")

	// Create pipeline client
	pipelineClient := pipeline.NewClient(cfg, log)

	ctx := context.Background()

	if *fireAndForget {
		// Fire-and-forget: start job and exit immediately
		runFireAndForget(ctx, pipelineClient, log)
	} else {
		// Default: start job and poll until completion
		runWithPolling(ctx, pipelineClient, log)
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
