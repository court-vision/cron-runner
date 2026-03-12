package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

const (
	reportTimeout      = 5 * time.Second
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
	url    string
	token  string
	client *http.Client
	log    zerolog.Logger
}

// New creates a Reporter that POSTs to baseURL/v1/internal/cron/job-runs.
func New(baseURL, token string, log zerolog.Logger) *Reporter {
	return &Reporter{
		url:    baseURL + "/v1/internal/cron/job-runs",
		token:  token,
		client: &http.Client{Timeout: reportTimeout},
		log:    log.With().Str("component", "reporter").Logger(),
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

func (r *Reporter) send(report RunReport) {
	body, err := json.Marshal(report)
	if err != nil {
		r.log.Warn().Err(err).Msg("reporter_marshal_failed")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		r.log.Warn().Err(err).Msg("reporter_request_create_failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", r.token))

	resp, err := r.client.Do(req)
	if err != nil {
		r.log.Warn().Err(err).Str("job", report.JobName).Msg("reporter_send_failed")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		r.log.Warn().
			Int("status", resp.StatusCode).
			Str("job", report.JobName).
			Msg("reporter_non_success_status")
	}
}
