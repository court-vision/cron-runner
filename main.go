package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cron-runner/internal/config"
	"cron-runner/internal/health"
	"cron-runner/internal/logger"
	"cron-runner/internal/pipeline"

	"github.com/rs/zerolog"
)

func main() {
	// Parse command line flags
	runOnce := flag.Bool("once", false, "Run pipeline trigger once and exit (CLI mode)")
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
		Dur("poll_initial_interval", cfg.PollInitialInterval).
		Dur("poll_max_interval", cfg.PollMaxInterval).
		Dur("poll_max_wait_time", cfg.PollMaxWaitTime).
		Msg("cron-runner starting")

	// Create pipeline client
	pipelineClient := pipeline.NewClient(cfg, log)

	// CLI mode: run once and exit
	if *runOnce {
		runOnceMode(pipelineClient, log)
		return
	}

	// Server mode: start health server and wait for signals
	serverMode(cfg, pipelineClient, log)
}

// runOnceMode triggers the pipeline once and exits.
func runOnceMode(client *pipeline.Client, log zerolog.Logger) {
	log.Info().Msg("running in CLI mode (single execution)")

	ctx := context.Background()
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

// serverMode runs an HTTP server with health checks and a trigger endpoint.
func serverMode(cfg *config.Config, client *pipeline.Client, log zerolog.Logger) {
	log.Info().Msg("running in server mode")

	// Create cancellation function for shutdown
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create health state tracker
	healthState := health.NewState()

	// Set up HTTP routes
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"service": "cron-runner",
			"version": "1.0.0",
		})
	})

	mux.HandleFunc("/health", healthState.HandleHealth)
	mux.HandleFunc("/ready", healthState.HandleReady)

	mux.HandleFunc("/trigger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		log.Info().Msg("received trigger request")

		result := client.TriggerAll(r.Context())
		healthState.RecordPipelineRun(result.Success, result.Duration, result.Attempts, result.Error)

		w.Header().Set("Content-Type", "application/json")
		if result.Success {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":   "success",
				"attempts": result.Attempts,
				"duration": result.Duration.String(),
			})
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			errMsg := ""
			if result.Error != nil {
				errMsg = result.Error.Error()
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":      "failed",
				"attempts":    result.Attempts,
				"duration":    result.Duration.String(),
				"status_code": result.StatusCode,
				"error":       errMsg,
			})
		}
	})

	// Create HTTP server
	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 5 * time.Minute, // Long timeout for pipeline triggers
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Info().Str("port", cfg.Port).Msg("HTTP server starting")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("HTTP server error")
			cancel()
		}
	}()

	// Wait for shutdown signal
	sig := <-sigChan
	log.Info().Str("signal", sig.String()).Msg("received shutdown signal, shutting down gracefully")

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	healthState.SetReady(false)

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}

	log.Info().Msg("server stopped")
}
