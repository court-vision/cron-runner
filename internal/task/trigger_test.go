package task

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cron-runner/internal/config"
	"cron-runner/internal/pipeline"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func TestJobNameFromEndpoint(t *testing.T) {
	cases := map[string]string{
		"/v1/internal/pipelines/pre-game":                    "pre-game",
		"/v1/internal/pipelines/game-start-times?source=cdn": "game-start-times",
		"/v1/internal/pipelines/post-game/":                  "post-game",
		"/v1/internal/pipelines/playoffs#frag":               "playoffs",
		"live-stats":                                         "live-stats",
	}
	for in, want := range cases {
		if got := jobNameFromEndpoint(in); got != want {
			t.Errorf("jobNameFromEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReportNamePrefersJobName(t *testing.T) {
	tt := &TriggerTask{Endpoint: "/v1/internal/pipelines/game-start-times?source=cdn"}
	if got := tt.ReportName(); got != "game-start-times" {
		t.Errorf("ReportName() without JobName = %q, want %q", got, "game-start-times")
	}
	tt.JobName = "schedule-sync"
	if got := tt.ReportName(); got != "schedule-sync" {
		t.Errorf("ReportName() with JobName = %q, want %q", got, "schedule-sync")
	}
}

// TestRunCorrelationID pins the contract that makes a run traceable across
// services: each Run sends a fresh UUID as X-Correlation-ID, and the same id is
// logged on the run's trigger_succeeded / trigger_failed line.
func TestRunCorrelationID(t *testing.T) {
	var mu sync.Mutex
	var headers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get(pipeline.CorrelationHeader))
		n := len(headers)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound) // second run fails, not retried
	}))
	t.Cleanup(srv.Close)

	var logs bytes.Buffer
	log := zerolog.New(&logs)
	cfg := &config.Config{
		BackendURL:     srv.URL,
		PipelineAuth:   "test-token",
		MaxRetries:     0,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		BackoffFactor:  1,
		RequestTimeout: time.Second,
	}
	tt := &TriggerTask{
		Client:   pipeline.NewClient(cfg, log),
		Endpoint: "/v1/internal/pipelines/pre-game",
		JobName:  "pre-game",
		Log:      log.With().Str("job", "pre-game").Logger(),
	}

	if err := tt.Run(context.Background()); err != nil {
		t.Fatalf("first Run: unexpected error: %v", err)
	}
	if err := tt.Run(context.Background()); err == nil {
		t.Fatalf("second Run: expected an error on 404")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(headers))
	}
	for i, h := range headers {
		if _, err := uuid.Parse(h); err != nil {
			t.Errorf("request %d: %s = %q is not a UUID: %v", i+1, pipeline.CorrelationHeader, h, err)
		}
	}
	if headers[0] == headers[1] {
		t.Errorf("expected a fresh correlation id per run, both were %q", headers[0])
	}

	// The trigger_* line for each run must carry the id that went out on the wire.
	logged := map[string]string{} // message → correlation_id
	sc := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	for sc.Scan() {
		var line struct {
			Message       string `json:"message"`
			CorrelationID string `json:"correlation_id"`
			Job           string `json:"job"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("log line is not JSON: %s", sc.Text())
		}
		if line.Message == "trigger_succeeded" || line.Message == "trigger_failed" {
			if line.Job != "pre-game" {
				t.Errorf("%s line lost the job field: %s", line.Message, sc.Text())
			}
			logged[line.Message] = line.CorrelationID
		}
	}
	if got := logged["trigger_succeeded"]; got != headers[0] {
		t.Errorf("trigger_succeeded logged correlation_id %q, request sent %q", got, headers[0])
	}
	if got := logged["trigger_failed"]; got != headers[1] {
		t.Errorf("trigger_failed logged correlation_id %q, request sent %q", got, headers[1])
	}
}

// TestRunCheckStatus: with CheckStatus, a 200 whose body says status "error" (a
// pipeline that ran and failed) is a failed run; without it, any 2xx succeeds.
func TestRunCheckStatus(t *testing.T) {
	bodies := []string{
		`{"status":"error","message":"News Feed failed: ESPN timed out","data":null}`,
		`{"status":"success","message":"stored 4","data":{}}`,
		`not json`,
	}
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		body := bodies[n%len(bodies)]
		n++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	log := zerolog.New(io.Discard)
	cfg := &config.Config{
		BackendURL:     srv.URL,
		PipelineAuth:   "test-token",
		MaxRetries:     0,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		BackoffFactor:  1,
		RequestTimeout: time.Second,
	}
	tt := &TriggerTask{Client: pipeline.NewClient(cfg, log), Endpoint: "/v1/internal/pipelines/news", Log: log}

	// Without the check, the failed pipeline's 200 reads as success
	if err := tt.Run(context.Background()); err != nil {
		t.Fatalf("without CheckStatus: unexpected error: %v", err)
	}
	tt.CheckStatus = true
	err := tt.Run(context.Background()) // body 2: success
	if err != nil {
		t.Fatalf("status success: unexpected error: %v", err)
	}
	if err := tt.Run(context.Background()); err != nil { // body 3: not the envelope
		t.Fatalf("a body that isn't the envelope: unexpected error: %v", err)
	}
	err = tt.Run(context.Background()) // body 1 again: error
	if err == nil || !strings.Contains(err.Error(), "ESPN timed out") {
		t.Fatalf("status error: want a failure naming the message, got %v", err)
	}
}

func TestWithRequestTimeoutLeavesTheOriginal(t *testing.T) {
	cfg := &config.Config{BackendURL: "http://data.test", RequestTimeout: 30 * time.Second}
	base := pipeline.NewClient(cfg, zerolog.New(io.Discard))
	long := base.WithRequestTimeout(4 * time.Minute)
	if base.RequestTimeout() != 30*time.Second || long.RequestTimeout() != 4*time.Minute {
		t.Fatalf("timeouts = %v / %v, want 30s / 4m", base.RequestTimeout(), long.RequestTimeout())
	}
}
