package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

const (
	reportTimeout      = 5 * time.Second
	reportRetryDelay   = 2 * time.Second
	reportMaxAttempts  = 2 // one retry: failure reports feed the data-platform's alert streaks
	responseSnippetMax = 300
)

// RunReport is the payload sent to data-platform after a cron job executes.
type RunReport struct {
	JobName         string    `json:"job_name"`
	TriggeredAt     time.Time `json:"triggered_at"`
	CompletedAt     time.Time `json:"completed_at"`
	DurationMs      int64     `json:"duration_ms"`
	Result          string    `json:"result"` // "success" | "failure"
	HTTPStatus      *int      `json:"http_status,omitempty"`
	Attempts        int       `json:"attempts"`
	ErrorMessage    *string   `json:"error_message,omitempty"`
	ResponseSnippet *string   `json:"response_snippet,omitempty"`
}

// Reporter sends job execution reports to the data-platform after each trigger.
// All calls are fire-and-forget — failures are logged but never block job execution.
type Reporter struct {
	url        string
	token      string
	client     *http.Client
	retryDelay time.Duration
	log        zerolog.Logger
}

// New creates a Reporter that POSTs to baseURL/v1/internal/cron/job-runs.
func New(baseURL, token string, log zerolog.Logger) *Reporter {
	return &Reporter{
		url:        baseURL + "/v1/internal/cron/job-runs",
		token:      token,
		client:     &http.Client{Timeout: reportTimeout},
		retryDelay: reportRetryDelay,
		log:        log.With().Str("component", "reporter").Logger(),
	}
}

// Report sends a job run report asynchronously. It never blocks the caller.
func (r *Reporter) Report(jobName string, triggeredAt time.Time, completedAt time.Time,
	durationMs int64, result string, httpStatus *int, attempts int,
	errMsg *string, responseBody string,
) {
	var snippet *string
	if responseBody != "" {
		s := responseBody
		if len(s) > responseSnippetMax {
			s = s[:responseSnippetMax]
		}
		snippet = &s
	}

	report := RunReport{
		JobName:         jobName,
		TriggeredAt:     triggeredAt,
		CompletedAt:     completedAt,
		DurationMs:      durationMs,
		Result:          result,
		HTTPStatus:      httpStatus,
		Attempts:        attempts,
		ErrorMessage:    errMsg,
		ResponseSnippet: snippet,
	}

	go r.send(report)
}

// send POSTs the report, retrying once after retryDelay on a transport error
// or a 5xx response. A 4xx is logged but not retried — it would fail the same
// way again. Failure reports drive the data-platform's job-failure alert
// streaks, so one dropped report is one missed alert; still warn-only, never
// more than one retry, never blocking a job.
func (r *Reporter) send(report RunReport) {
	body, err := json.Marshal(report)
	if err != nil {
		r.log.Warn().Err(err).Msg("reporter_marshal_failed")
		return
	}

	for attempt := 1; ; attempt++ {
		status, err := r.post(body)
		if err == nil && status < 500 {
			if status >= 300 {
				r.log.Warn().
					Int("status", status).
					Str("job", report.JobName).
					Msg("reporter_non_success_status")
			}
			return
		}

		ev := r.log.Warn().Err(err).Str("job", report.JobName).Int("attempt", attempt)
		if status != 0 {
			ev = ev.Int("status", status)
		}
		if attempt >= reportMaxAttempts {
			ev.Msg("reporter_send_failed")
			return
		}
		ev.Dur("retry_in", r.retryDelay).Msg("reporter_send_retrying")
		time.Sleep(r.retryDelay)
	}
}

// post sends the report once and returns the HTTP status, or 0 and the
// transport error.
func (r *Reporter) post(body []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.token)

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection is reusable
	return resp.StatusCode, nil
}
