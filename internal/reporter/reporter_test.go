package reporter

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func newTestReporter(t *testing.T, handler http.HandlerFunc) *Reporter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	r := New(srv.URL, "test-token", zerolog.New(io.Discard))
	r.retryDelay = 0
	return r
}

func sampleReport() RunReport {
	now := time.Now().UTC()
	return RunReport{
		JobName:     "live-stats",
		TriggeredAt: now,
		CompletedAt: now,
		DurationMs:  12,
		Result:      "failure",
		Attempts:    1,
	}
}

// TestSendRetryPolicy: exactly one retry, and only on 5xx.
func TestSendRetryPolicy(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []int // answered in order; the last one repeats
		wantCalls int32
	}{
		{"created first try", []int{http.StatusCreated}, 1},
		{"4xx is not retried", []int{http.StatusUnprocessableEntity}, 1},
		{"5xx then success", []int{http.StatusServiceUnavailable, http.StatusCreated}, 2},
		{"5xx twice gives up", []int{http.StatusInternalServerError}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			r := newTestReporter(t, func(w http.ResponseWriter, req *http.Request) {
				i := int(calls.Add(1)) - 1
				if i >= len(tc.statuses) {
					i = len(tc.statuses) - 1
				}
				w.WriteHeader(tc.statuses[i])
			})
			r.send(sampleReport())
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("sent %d requests, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestSendRetryResendsSamePayload: the retry carries the same report and auth.
func TestSendRetryResendsSamePayload(t *testing.T) {
	var calls atomic.Int32
	seen := make(chan RunReport, 2)
	r := newTestReporter(t, func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var rep RunReport
		if err := json.NewDecoder(req.Body).Decode(&rep); err != nil {
			t.Errorf("decode body: %v", err)
		}
		seen <- rep
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})

	want := sampleReport()
	r.send(want)
	if got := calls.Load(); got != 2 {
		t.Fatalf("sent %d requests, want 2", got)
	}
	for i := 0; i < 2; i++ {
		got := <-seen
		if got.JobName != want.JobName || got.Result != want.Result || got.Attempts != want.Attempts {
			t.Errorf("request %d carried %+v, want %+v", i+1, got, want)
		}
	}
}

func TestSendRetriesOnTransportError(t *testing.T) {
	r := New("http://example.test", "test-token", zerolog.New(io.Discard))
	r.retryDelay = 0

	calls := 0
	r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("connection refused")
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Body:       http.NoBody,
			Header:     make(http.Header),
		}, nil
	})

	r.send(sampleReport())
	if calls != 2 {
		t.Errorf("sent %d requests, want 2 (one retry after the transport error)", calls)
	}
}
