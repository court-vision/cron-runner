package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"cron-runner/internal/scheduler"

	"github.com/rs/zerolog"
)

// Server provides health and status HTTP endpoints for Railway health checks
// and operational visibility.
type Server struct {
	httpServer *http.Server
	sched      *scheduler.Scheduler
	version    string
	log        zerolog.Logger
}

func New(port string, sched *scheduler.Scheduler, log zerolog.Logger) *Server {
	s := &Server{
		sched:   sched,
		version: buildVersion(),
		log:     log.With().Str("component", "http-server").Logger(),
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

// buildVersion is the short git SHA Railway stamps on the container
// (RAILWAY_GIT_COMMIT_SHA), or "dev" when running outside Railway.
func buildVersion() string {
	sha := os.Getenv("RAILWAY_GIT_COMMIT_SHA")
	if sha == "" {
		return "dev"
	}
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return sha
}

// Start runs the HTTP server. Blocking — call in a goroutine.
func (s *Server) Start() {
	s.log.Info().Str("addr", s.httpServer.Addr).Str("version", s.version).Msg("http_server_starting")
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		s.log.Error().Err(err).Msg("http_server_error")
	}
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// healthResponse is the GET /health body. Field order is the documented
// contract: uptime monitors match on the literal `"status":"ok"`.
type healthResponse struct {
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
	Jobs    int    `json:"jobs"`
	Version string `json:"version"`
}

// GET /health — Railway health check and uptime-monitor target.
// Always 200 while the process runs: the scheduler has no external dependency
// to gate on (the data-platform's own /health covers the database).
//
//	{"status":"ok","uptime":"3h12m5s","jobs":6,"version":"270eb2e"}
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(healthResponse{
		Status:  "ok",
		Uptime:  s.sched.Uptime(),
		Jobs:    s.sched.JobCount(),
		Version: s.version,
	})
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
