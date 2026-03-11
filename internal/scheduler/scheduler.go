package scheduler

import (
	"context"
	"sync"
	"time"

	"cron-runner/internal/task"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// JobDef is the declarative description of a scheduled job.
// Adding a new job means adding one JobDef to the registry — nothing else changes.
type JobDef struct {
	Name        string
	Schedule    string        // cron expression; 5-field by default, 6-field if WithSeconds is true
	WithSeconds bool          // if true, Schedule's first field is seconds (e.g. "*/30 * * * * *")
	Task        task.Task
	Singleton   bool          // if true, a new run is dropped if the previous is still running
	Timeout     time.Duration // 0 = no timeout; cancels the job's context when exceeded
}

// JobStatus is the runtime state of a registered job, reported by GET /status.
type JobStatus struct {
	Name         string     `json:"name"`
	Schedule     string     `json:"schedule"`
	LastRun      *time.Time `json:"last_run,omitempty"`
	NextRun      *time.Time `json:"next_run,omitempty"`
	LastResult   string     `json:"last_result"` // "never", "running", "success", "failure"
	LastError    string     `json:"last_error,omitempty"`
	LastDuration string     `json:"last_duration,omitempty"`
	RunCount     uint64     `json:"run_count"`
}

type jobState struct {
	def          JobDef
	lastRun      *time.Time
	lastResult   string
	lastError    string
	lastDuration time.Duration
	runCount     uint64
}

// Scheduler wraps gocron/v2 with status tracking and structured logging.
type Scheduler struct {
	s       gocron.Scheduler
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
	states  map[string]*jobState // keyed by job name
	running map[string]time.Time // job name → start time (empty if not running)
	log     zerolog.Logger
	started time.Time
}

func New(log zerolog.Logger) *Scheduler {
	s, _ := gocron.NewScheduler(gocron.WithLocation(time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		s:       s,
		ctx:     ctx,
		cancel:  cancel,
		states:  make(map[string]*jobState),
		running: make(map[string]time.Time),
		log:     log.With().Str("component", "scheduler").Logger(),
	}
}

// Register adds a job to the scheduler. Must be called before Start.
func (s *Scheduler) Register(def JobDef) error {
	state := &jobState{
		def:        def,
		lastResult: "never",
	}

	s.mu.Lock()
	s.states[def.Name] = state
	s.mu.Unlock()

	opts := []gocron.JobOption{
		gocron.WithName(def.Name),
		gocron.WithEventListeners(
			gocron.BeforeJobRuns(func(jobID uuid.UUID, jobName string) {
				s.mu.Lock()
				s.running[jobName] = time.Now()
				if st, ok := s.states[jobName]; ok {
					st.lastResult = "running"
				}
				s.mu.Unlock()
				s.log.Info().Str("job", jobName).Msg("job_started")
			}),
			gocron.AfterJobRuns(func(jobID uuid.UUID, jobName string) {
				now := time.Now()
				s.mu.Lock()
				startTime := s.running[jobName]
				delete(s.running, jobName)
				if st, ok := s.states[jobName]; ok {
					st.lastResult = "success"
					st.lastRun = &now
					st.lastError = ""
					st.lastDuration = now.Sub(startTime)
					st.runCount++
				}
				s.mu.Unlock()
				s.log.Info().Str("job", jobName).Msg("job_completed")
			}),
			gocron.AfterJobRunsWithError(func(jobID uuid.UUID, jobName string, err error) {
				now := time.Now()
				s.mu.Lock()
				startTime := s.running[jobName]
				delete(s.running, jobName)
				if st, ok := s.states[jobName]; ok {
					st.lastResult = "failure"
					st.lastRun = &now
					st.lastError = err.Error()
					st.lastDuration = now.Sub(startTime)
					st.runCount++
				}
				s.mu.Unlock()
				s.log.Error().Str("job", jobName).Err(err).Msg("job_failed")
			}),
		),
	}

	if def.Singleton {
		opts = append(opts, gocron.WithSingletonMode(gocron.LimitModeReschedule))
	}

	// Capture def for the closure.
	jobDef := def

	_, err := s.s.NewJob(
		gocron.CronJob(jobDef.Schedule, jobDef.WithSeconds),
		gocron.NewTask(func() error {
			// Build a run context derived from the scheduler's context so that
			// s.cancel() (called on shutdown) propagates to all running tasks.
			runCtx := s.ctx
			if jobDef.Timeout > 0 {
				var cancel context.CancelFunc
				runCtx, cancel = context.WithTimeout(s.ctx, jobDef.Timeout)
				defer cancel()
			}
			return jobDef.Task.Run(runCtx)
		}),
		opts...,
	)
	if err != nil {
		return err
	}

	s.log.Info().
		Str("job", def.Name).
		Str("schedule", def.Schedule).
		Bool("singleton", def.Singleton).
		Dur("timeout", def.Timeout).
		Msg("job_registered")

	return nil
}

// Start begins the scheduler. Non-blocking.
func (s *Scheduler) Start() {
	s.started = time.Now()
	s.s.Start()
	s.log.Info().Msg("scheduler_started")
}

// Shutdown cancels all running tasks and waits for them to complete.
// ctx controls how long to wait before giving up on the drain.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.log.Info().Msg("scheduler_shutting_down")
	// Cancel the scheduler context first — this signals all running tasks to stop.
	s.cancel()

	done := make(chan error, 1)
	go func() { done <- s.s.Shutdown() }()
	select {
	case err := <-done:
		s.log.Info().Msg("scheduler_stopped")
		return err
	case <-ctx.Done():
		s.log.Warn().Msg("scheduler_drain_timeout_exceeded")
		return ctx.Err()
	}
}

// Statuses returns the current runtime state of all registered jobs.
func (s *Scheduler) Statuses() []JobStatus {
	// Build next-run map from gocron.
	nextRuns := make(map[string]time.Time)
	for _, j := range s.s.Jobs() {
		if nr, err := j.NextRun(); err == nil && !nr.IsZero() {
			nextRuns[j.Name()] = nr
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	statuses := make([]JobStatus, 0, len(s.states))
	for _, st := range s.states {
		js := JobStatus{
			Name:       st.def.Name,
			Schedule:   st.def.Schedule,
			LastRun:    st.lastRun,
			LastResult: st.lastResult,
			LastError:  st.lastError,
			RunCount:   st.runCount,
		}
		if st.lastDuration > 0 {
			js.LastDuration = st.lastDuration.Round(time.Millisecond).String()
		}
		if nr, ok := nextRuns[st.def.Name]; ok {
			js.NextRun = &nr
		}
		statuses = append(statuses, js)
	}

	return statuses
}

// Uptime returns a human-readable uptime string.
func (s *Scheduler) Uptime() string {
	if s.started.IsZero() {
		return "not started"
	}
	return time.Since(s.started).Round(time.Second).String()
}
