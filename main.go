package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

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
		Str("endpoint", cfg.Endpoint).
		Msg("cron-runner starting")

	// Create pipeline client
	pipelineClient := pipeline.NewClient(cfg, log)

	ctx := context.Background()

	switch cfg.Job {
	case config.JobTrigger:
		runTriggerMode(ctx, pipelineClient, cfg.Endpoint, log)
	case config.JobPoll:
		runPollMode(ctx, pipelineClient, cfg.Endpoint, log)
	case config.JobLoop:
		runLoopMode(ctx, pipelineClient, cfg, log)
	}
}

// runTriggerMode POSTs to the configured endpoint and exits immediately.
// Use for one-shot pipeline triggers (alerts, post-game jobs, fire-and-forget).
func runTriggerMode(ctx context.Context, client *pipeline.Client, endpoint string, log zerolog.Logger) {
	log.Info().
		Str("endpoint", endpoint).
		Msg("trigger_mode_started")

	result := client.TriggerEndpoint(ctx, endpoint)

	if result.Success {
		log.Info().
			Int("attempts", result.Attempts).
			Int("status_code", result.StatusCode).
			Dur("duration", result.Duration).
			Msg("trigger_succeeded")
		os.Exit(0)
	} else {
		log.Error().
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Err(result.Error).
			Msg("trigger_failed")
		os.Exit(1)
	}
}

// runPollMode starts a pipeline job at the configured endpoint and polls until completion.
// Use for batch jobs where you need to confirm all pipelines succeeded.
func runPollMode(ctx context.Context, client *pipeline.Client, endpoint string, log zerolog.Logger) {
	log.Info().
		Str("endpoint", endpoint).
		Msg("poll_mode_started")

	result := client.TriggerAll(ctx, endpoint)

	if result.Success {
		log.Info().
			Str("job_id", result.JobID).
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Msg("poll_succeeded")
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
		logEvent.Msg("poll_failed")
		os.Exit(1)
	}
}

// runLoopMode repeatedly POSTs to the configured endpoint until the response signals
// completion via data.done == true, or the safety timeout is reached.
//
// If LOOP_SCHEDULE_ENDPOINT is set, the runner first fetches that endpoint to check
// whether there is work to do today and when to start. This allows the external cron
// to fire at a fixed early time while the container handles its own wake-up timing.
func runLoopMode(ctx context.Context, client *pipeline.Client, cfg *config.Config, log zerolog.Logger) {
	endpoint := cfg.Endpoint

	log.Info().
		Str("endpoint", endpoint).
		Str("schedule_endpoint", cfg.LoopScheduleEndpoint).
		Dur("loop_interval", cfg.LoopInterval).
		Dur("max_duration", cfg.LoopMaxDuration).
		Msg("loop_mode_started")

	// Safety timeout: prevents zombie containers if something goes wrong.
	ctx, cancel := context.WithTimeout(ctx, cfg.LoopMaxDuration)
	defer cancel()

	// scheduleResponse mirrors the JSON shape of the optional schedule endpoint.
	type scheduleResponse struct {
		Data struct {
			HasGames bool   `json:"has_games"`
			WakeAtET string `json:"wake_at_et"`
		} `json:"data"`
	}

	// loopResponse mirrors the JSON shape of the loop endpoint.
	// Endpoints used in loop mode must return data.done = true to signal completion.
	type loopResponse struct {
		Data struct {
			Done bool `json:"done"`
		} `json:"data"`
	}

	// Step 1 (optional): Fetch the schedule endpoint and sleep until the right time.
	if cfg.LoopScheduleEndpoint != "" {
		schedResult := client.FetchEndpoint(ctx, cfg.LoopScheduleEndpoint)
		if !schedResult.Success {
			log.Warn().
				Err(schedResult.Error).
				Msg("schedule_fetch_failed_starting_immediately")
		} else {
			var sched scheduleResponse
			if err := json.Unmarshal([]byte(schedResult.ResponseBody), &sched); err != nil {
				log.Warn().Err(err).Msg("schedule_parse_failed_starting_immediately")
			} else if !sched.Data.HasGames {
				log.Info().Msg("no_work_today_exiting")
				os.Exit(0)
			} else if sched.Data.WakeAtET != "" {
				wakeAt, err := time.Parse(time.RFC3339, sched.Data.WakeAtET)
				if err != nil {
					log.Warn().Err(err).Str("wake_at_raw", sched.Data.WakeAtET).Msg("wake_time_parse_failed_starting_immediately")
				} else {
					sleepDur := time.Until(wakeAt)
					if sleepDur > 0 {
						log.Info().
							Time("wake_at", wakeAt).
							Dur("sleep_duration", sleepDur).
							Msg("sleeping_until_start_window")

						select {
						case <-ctx.Done():
							log.Info().Msg("context_cancelled_during_sleep")
							os.Exit(0)
						case <-time.After(sleepDur):
						}

						log.Info().Msg("sleep_complete_starting_loop")
					} else {
						log.Info().
							Time("wake_at", wakeAt).
							Msg("wake_time_already_past_starting_loop_immediately")
					}
				}
			}
		}
	}

	// Step 2: Poll the endpoint until done.

	// triggerOnce fires one request and returns true if the loop should stop.
	triggerOnce := func() bool {
		result := client.TriggerEndpoint(ctx, endpoint)

		if !result.Success {
			// Log but continue looping — transient errors shouldn't stop the loop
			log.Warn().
				Err(result.Error).
				Int("attempts", result.Attempts).
				Dur("duration", result.Duration).
				Msg("loop_trigger_failed_continuing")
			return false
		}

		log.Info().
			Int("status_code", result.StatusCode).
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Msg("loop_trigger_success")

		var resp loopResponse
		if err := json.Unmarshal([]byte(result.ResponseBody), &resp); err != nil {
			log.Warn().
				Err(err).
				Msg("loop_response_parse_failed_continuing")
			return false
		}

		if resp.Data.Done {
			log.Info().Msg("loop_done_signal_received_exiting")
			return true
		}

		return false
	}

	// Fire immediately before the first tick
	if triggerOnce() {
		os.Exit(0)
	}

	ticker := time.NewTicker(cfg.LoopInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().
				Err(ctx.Err()).
				Msg("loop_mode_context_done_exiting")
			os.Exit(0)
		case <-ticker.C:
			if triggerOnce() {
				os.Exit(0)
			}
		}
	}
}
