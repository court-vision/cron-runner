package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"cron-runner/internal/scheduler"

	"github.com/rs/zerolog"
)

// Server provides health and status HTTP endpoints for Railway health checks
// and operational visibility.
type Server struct {
	httpServer *http.Server
	sched      *scheduler.Scheduler
	log        zerolog.Logger
}

func New(port string, sched *scheduler.Scheduler, log zerolog.Logger) *Server {
	s := &Server{
		sched: sched,
		log:   log.With().Str("component", "http-server").Logger(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /status", s.handleStatus)

	s.httpServer = &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

// Start runs the HTTP server. Blocking — call in a goroutine.
func (s *Server) Start() {
	s.log.Info().Str("addr", s.httpServer.Addr).Msg("http_server_starting")
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		s.log.Error().Err(err).Msg("http_server_error")
	}
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// GET /health — Railway health check.
// Returns 200 {"status":"ok"} when the service is running.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// GET /status — Current state of all registered jobs.
// Returns scheduler uptime and per-job last_run, next_run, last_result, run_count.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"scheduler": "running",
		"uptime":    s.sched.Uptime(),
		"jobs":      s.sched.Statuses(),
	})
}
