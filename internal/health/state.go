package health

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Status represents the health status of the service.
type Status struct {
	Status    string    `json:"status"`
	Service   string    `json:"service"`
	Timestamp time.Time `json:"timestamp"`
	Checks    *Checks   `json:"checks,omitempty"`
}

// Checks contains individual health check results.
type Checks struct {
	LastPipelineRun *PipelineRunStatus `json:"last_pipeline_run,omitempty"`
}

// PipelineRunStatus tracks the last pipeline execution.
type PipelineRunStatus struct {
	Success   bool      `json:"success"`
	Timestamp time.Time `json:"timestamp"`
	Duration  string    `json:"duration"`
	Attempts  int       `json:"attempts"`
	Error     string    `json:"error,omitempty"`
}

// State tracks the health state of the service.
type State struct {
	mu              sync.RWMutex
	ready           bool
	lastPipelineRun *PipelineRunStatus
}

// NewState creates a new health state tracker.
func NewState() *State {
	return &State{
		ready: true,
	}
}

// RecordPipelineRun records the result of a pipeline execution.
func (s *State) RecordPipelineRun(success bool, duration time.Duration, attempts int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := &PipelineRunStatus{
		Success:   success,
		Timestamp: time.Now().UTC(),
		Duration:  duration.String(),
		Attempts:  attempts,
	}

	if err != nil {
		status.Error = err.Error()
	}

	s.lastPipelineRun = status
}

// SetReady sets the readiness state.
func (s *State) SetReady(ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = ready
}

// HandleHealth handles the /health endpoint.
func (s *State) HandleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	lastRun := s.lastPipelineRun
	s.mu.RUnlock()

	status := Status{
		Status:    "healthy",
		Service:   "cron-runner",
		Timestamp: time.Now().UTC(),
	}

	if lastRun != nil {
		status.Checks = &Checks{
			LastPipelineRun: lastRun,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}

// HandleReady handles the /ready endpoint.
func (s *State) HandleReady(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	ready := s.ready
	s.mu.RUnlock()

	status := Status{
		Service:   "cron-runner",
		Timestamp: time.Now().UTC(),
	}

	if ready {
		status.Status = "ready"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	} else {
		status.Status = "not_ready"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	json.NewEncoder(w).Encode(status)
}
