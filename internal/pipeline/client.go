package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"cron-runner/internal/config"
	"cron-runner/internal/retry"

	"github.com/rs/zerolog"
)

// Client handles communication with the backend pipeline API.
type Client struct {
	httpClient  *http.Client
	baseURL     string
	authToken   string
	retryCfg    retry.Config
	pollCfg     PollConfig
	log         zerolog.Logger
}

// PollConfig holds settings for job status polling.
type PollConfig struct {
	InitialInterval time.Duration
	MaxInterval     time.Duration
	MaxWaitTime     time.Duration
}

// NewClient creates a new pipeline client.
func NewClient(cfg *config.Config, log zerolog.Logger) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: cfg.RequestTimeout},
		baseURL:    cfg.BackendURL,
		authToken:  cfg.PipelineAuth,
		retryCfg: retry.Config{
			MaxRetries:     cfg.MaxRetries,
			InitialBackoff: cfg.InitialBackoff,
			MaxBackoff:     cfg.MaxBackoff,
			BackoffFactor:  cfg.BackoffFactor,
		},
		pollCfg: PollConfig{
			InitialInterval: cfg.PollInitialInterval,
			MaxInterval:     cfg.PollMaxInterval,
			MaxWaitTime:     cfg.PollMaxWaitTime,
		},
		log: log.With().Str("component", "pipeline-client").Logger(),
	}
}

// TriggerResult contains the outcome of a pipeline trigger.
type TriggerResult struct {
	Success      bool
	JobID        string
	StatusCode   int
	ResponseBody string
	Attempts     int
	Duration     time.Duration
	Error        error
	JobDetails   *JobStatus
}

// JobStatus represents the status of a pipeline job from the API.
type JobStatus struct {
	JobID              string                    `json:"job_id"`
	Status             string                    `json:"status"`
	CreatedAt          string                    `json:"created_at"`
	StartedAt          string                    `json:"started_at,omitempty"`
	CompletedAt        string                    `json:"completed_at,omitempty"`
	DurationSeconds    float64                   `json:"duration_seconds,omitempty"`
	PipelinesTotal     int                       `json:"pipelines_total"`
	PipelinesCompleted int                       `json:"pipelines_completed"`
	PipelinesFailed    int                       `json:"pipelines_failed"`
	CurrentPipeline    string                    `json:"current_pipeline,omitempty"`
	Results            map[string]PipelineResult `json:"results,omitempty"`
	Error              string                    `json:"error,omitempty"`
}

// PipelineResult represents the result of a single pipeline.
type PipelineResult struct {
	PipelineName     string  `json:"pipeline_name"`
	Status           string  `json:"status"`
	Message          string  `json:"message"`
	DurationSeconds  float64 `json:"duration_seconds,omitempty"`
	RecordsProcessed int     `json:"records_processed,omitempty"`
	Error            string  `json:"error,omitempty"`
}

// jobCreatedResponse is the response from POST /pipelines/all
type jobCreatedResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    struct {
		JobID          string `json:"job_id"`
		Status         string `json:"status"`
		CreatedAt      string `json:"created_at"`
		PipelinesTotal int    `json:"pipelines_total"`
	} `json:"data"`
}

// jobStatusResponse is the response from GET /pipelines/jobs/{job_id}
type jobStatusResponse struct {
	Status  string    `json:"status"`
	Message string    `json:"message"`
	Data    JobStatus `json:"data"`
}

// TriggerAll triggers all pipelines via the backend API using fire-and-forget pattern.
// It starts the job, then polls for completion.
func (c *Client) TriggerAll(ctx context.Context) TriggerResult {
	startTime := time.Now()

	// Step 1: Start the job
	jobID, attempts, err := c.startJob(ctx)
	if err != nil {
		return TriggerResult{
			Attempts: attempts,
			Duration: time.Since(startTime),
			Error:    err,
		}
	}

	c.log.Info().
		Str("job_id", jobID).
		Int("attempts", attempts).
		Msg("pipeline job started, polling for completion")

	// Step 2: Poll for completion
	jobStatus, err := c.pollJobCompletion(ctx, jobID)
	
	result := TriggerResult{
		JobID:      jobID,
		Attempts:   attempts,
		Duration:   time.Since(startTime),
		JobDetails: jobStatus,
	}

	if err != nil {
		result.Error = err
		c.log.Error().
			Err(err).
			Str("job_id", jobID).
			Dur("duration", result.Duration).
			Msg("pipeline job polling failed")
		return result
	}

	if jobStatus != nil {
		result.Success = jobStatus.Status == "completed" && jobStatus.PipelinesFailed == 0

		if result.Success {
			c.log.Info().
				Str("job_id", jobID).
				Int("pipelines_completed", jobStatus.PipelinesCompleted).
				Float64("job_duration_seconds", jobStatus.DurationSeconds).
				Dur("total_duration", result.Duration).
				Msg("all pipelines completed successfully")
		} else {
			c.log.Error().
				Str("job_id", jobID).
				Str("job_status", jobStatus.Status).
				Int("pipelines_failed", jobStatus.PipelinesFailed).
				Int("pipelines_completed", jobStatus.PipelinesCompleted).
				Str("error", jobStatus.Error).
				Msg("pipeline job failed")
		}
	}

	return result
}

// startJob initiates a new pipeline job and returns the job ID.
func (c *Client) startJob(ctx context.Context) (string, int, error) {
	url := c.baseURL + "/v1/internal/pipelines/all"

	c.log.Info().
		Str("url", url).
		Msg("starting pipeline job")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.authToken)

	result := retry.Do(ctx, c.httpClient, req, c.retryCfg, c.log)

	if result.FinalError != nil {
		return "", result.Attempts, fmt.Errorf("failed to start job: %w", result.FinalError)
	}

	if result.Response == nil {
		return "", result.Attempts, fmt.Errorf("no response received")
	}
	defer result.Response.Body.Close()

	body, err := io.ReadAll(result.Response.Body)
	if err != nil {
		return "", result.Attempts, fmt.Errorf("failed to read response: %w", err)
	}

	if result.Response.StatusCode < 200 || result.Response.StatusCode >= 300 {
		return "", result.Attempts, fmt.Errorf("unexpected status %d: %s", result.Response.StatusCode, string(body))
	}

	var resp jobCreatedResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", result.Attempts, fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.Data.JobID == "" {
		return "", result.Attempts, fmt.Errorf("no job ID in response")
	}

	return resp.Data.JobID, result.Attempts, nil
}

// pollJobCompletion polls the job status endpoint until the job completes or times out.
func (c *Client) pollJobCompletion(ctx context.Context, jobID string) (*JobStatus, error) {
	url := c.baseURL + "/v1/internal/pipelines/jobs/" + jobID

	interval := c.pollCfg.InitialInterval
	deadline := time.Now().Add(c.pollCfg.MaxWaitTime)

	for {
		// Check if we've exceeded the deadline
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("polling timeout after %v", c.pollCfg.MaxWaitTime)
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Fetch job status
		status, err := c.fetchJobStatus(ctx, url)
		if err != nil {
			c.log.Warn().
				Err(err).
				Str("job_id", jobID).
				Msg("failed to fetch job status, will retry")
		} else {
			c.log.Debug().
				Str("job_id", jobID).
				Str("status", status.Status).
				Int("completed", status.PipelinesCompleted).
				Int("total", status.PipelinesTotal).
				Str("current", status.CurrentPipeline).
				Msg("job status update")

			// Check if job is done
			if status.Status == "completed" || status.Status == "failed" {
				return status, nil
			}
		}

		// Wait before next poll
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		// Increase interval with backoff (cap at max)
		interval = time.Duration(float64(interval) * 1.5)
		if interval > c.pollCfg.MaxInterval {
			interval = c.pollCfg.MaxInterval
		}
	}
}

// fetchJobStatus fetches the current status of a job.
func (c *Client) fetchJobStatus(ctx context.Context, url string) (*JobStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("job not found")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var statusResp jobStatusResponse
	if err := json.Unmarshal(body, &statusResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &statusResp.Data, nil
}
