package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"cron-runner/internal/config"
	"cron-runner/internal/jobs"
	"cron-runner/internal/logger"
	"cron-runner/internal/pipeline"
	"cron-runner/internal/scheduler"
	"cron-runner/internal/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		os.Stderr.WriteString("Configuration error: " + err.Error() + "\n")
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogJSON)
	log.Info().
		Str("backend_url", cfg.BackendURL).
		Str("http_port", cfg.HTTPPort).
		Dur("drain_timeout", cfg.DrainTimeout).
		Msg("cron-runner starting")

	client := pipeline.NewClient(cfg, log)

	sched := scheduler.New(log)
	for _, def := range jobs.RegisterAll(client, log) {
		if err := sched.Register(def); err != nil {
			log.Fatal().Err(err).Str("job", def.Name).Msg("failed to register job")
		}
	}

	srv := server.New(cfg.HTTPPort, sched, log)
	go srv.Start()

	sched.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	log.Info().Msg("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), cfg.DrainTimeout)
	defer cancel()

	if err := sched.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("scheduler shutdown error")
	}
	if err := srv.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("http server shutdown error")
	}

	log.Info().Msg("shutdown complete")
}
