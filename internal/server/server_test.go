package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cron-runner/internal/scheduler"

	"github.com/rs/zerolog"
)

type noopTask struct{}

func (noopTask) Name() string              { return "noop" }
func (noopTask) Run(context.Context) error { return nil }

func TestHandleHealth(t *testing.T) {
	t.Setenv("RAILWAY_GIT_COMMIT_SHA", "270eb2e9f1c0a5b3d4e6f7a8b9c0d1e2f3a4b5c6")

	sched := scheduler.New(zerolog.New(io.Discard))
	if err := sched.Register(scheduler.JobDef{Name: "noop", Schedule: "0 0 * * *", Task: noopTask{}}); err != nil {
		t.Fatal(err)
	}
	srv := New("0", sched, zerolog.New(io.Discard))

	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	// Better Stack / CI keyword monitors match on this exact literal.
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("body %s does not contain the literal \"status\":\"ok\"", rec.Body.String())
	}

	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if got.Version != "270eb2e" {
		t.Errorf("version = %q, want the 7-char SHA prefix %q", got.Version, "270eb2e")
	}
	if got.Jobs != 1 {
		t.Errorf("jobs = %d, want 1", got.Jobs)
	}
	if got.Uptime == "" {
		t.Errorf("uptime is empty")
	}
}

func TestBuildVersionOutsideRailway(t *testing.T) {
	t.Setenv("RAILWAY_GIT_COMMIT_SHA", "")
	if got := buildVersion(); got != "dev" {
		t.Errorf("buildVersion() = %q, want \"dev\"", got)
	}
	t.Setenv("RAILWAY_GIT_COMMIT_SHA", "abc")
	if got := buildVersion(); got != "abc" {
		t.Errorf("buildVersion() with a short SHA = %q, want \"abc\"", got)
	}
}
