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
	case config.JobLive:
		runLiveMode(ctx, pipelineClient, cfg, log)
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

// runLiveMode polls the live stats endpoint every LiveLoopInterval until all games
// are complete or the safety timeout (LiveMaxDuration) is reached.
//
// On startup it queries the schedule endpoint to find today's first tip-off
// and sleeps until 30 minutes before game time. This means the external cron
// can fire at a fixed early time (e.g. 8am ET) and the container handles its
// own wake-up — no hardcoded per-day scheduling required.
func runLiveMode(ctx context.Context, client *pipeline.Client, cfg *config.Config, log zerolog.Logger) {
	endpoint := cfg.Endpoint

	log.Info().
		Str("endpoint", endpoint).
		Str("schedule_endpoint", cfg.LiveScheduleEndpoint).
		Dur("loop_interval", cfg.LiveLoopInterval).
		Dur("max_duration", cfg.LiveMaxDuration).
		Msg("live_mode_started")

	// Safety timeout: prevents zombie containers if something goes wrong.
	// Default is 16h to cover the widest possible game window (morning to late night).
	ctx, cancel := context.WithTimeout(ctx, cfg.LiveMaxDuration)
	defer cancel()

	// scheduleResponse mirrors the JSON shape of GET /v1/live/schedule/today
	type scheduleResponse struct {
		Data struct {
			HasGames  bool   `json:"has_games"`
			WakeAtET  string `json:"wake_at_et"`
		} `json:"data"`
	}

	// liveStatsResponse mirrors the JSON shape of POST /v1/internal/pipelines/live-stats
	type liveStatsResponse struct {
		Data struct {
			AllGamesComplete bool `json:"all_games_complete"`
		} `json:"data"`
	}

	// Step 1: Fetch today's schedule and sleep until the pregame window.
	schedResult := client.FetchEndpoint(ctx, cfg.LiveScheduleEndpoint)
	if !schedResult.Success {
		log.Warn().
			Err(schedResult.Error).
			Msg("schedule_fetch_failed_starting_immediately")
	} else {
		var sched scheduleResponse
		if err := json.Unmarshal([]byte(schedResult.ResponseBody), &sched); err != nil {
			log.Warn().Err(err).Msg("schedule_parse_failed_starting_immediately")
		} else if !sched.Data.HasGames {
			log.Info().Msg("no_games_today_exiting")
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
						Msg("sleeping_until_pregame_window")

					select {
					case <-ctx.Done():
						log.Info().Msg("context_cancelled_during_pregame_sleep")
						os.Exit(0)
					case <-time.After(sleepDur):
					}

					log.Info().Msg("pregame_sleep_complete_starting_loop")
				} else {
					log.Info().
						Time("wake_at", wakeAt).
						Msg("wake_time_already_past_starting_loop_immediately")
				}
			}
		}
	}

	// Step 2: Poll the live-stats endpoint until all games are complete.

	// triggerOnce fires one request and returns true if all games are complete.
	triggerOnce := func() bool {
		result := client.TriggerEndpoint(ctx, endpoint)

		if !result.Success {
			// Log but continue looping — transient errors shouldn't stop the loop
			log.Warn().
				Err(result.Error).
				Int("attempts", result.Attempts).
				Dur("duration", result.Duration).
				Msg("live_trigger_failed_continuing")
			return false
		}

		log.Info().
			Int("status_code", result.StatusCode).
			Int("attempts", result.Attempts).
			Dur("duration", result.Duration).
			Msg("live_trigger_success")

		// Parse response to detect completion signal
		var resp liveStatsResponse
		if err := json.Unmarshal([]byte(result.ResponseBody), &resp); err != nil {
			log.Warn().
				Err(err).
				Msg("live_response_parse_failed_continuing")
			return false
		}

		if resp.Data.AllGamesComplete {
			log.Info().Msg("all_games_complete_exiting")
			return true
		}

		return false
	}

	// Fire immediately before the first tick
	if triggerOnce() {
		os.Exit(0)
	}

	ticker := time.NewTicker(cfg.LiveLoopInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().
				Err(ctx.Err()).
				Msg("live_mode_context_done_exiting")
			os.Exit(0)
		case <-ticker.C:
			if triggerOnce() {
				os.Exit(0)
			}
		}
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
