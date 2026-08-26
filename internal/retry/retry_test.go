package retry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// answer is one scripted transport result.
type answer struct {
	status int   // 0 = return err instead of a response
	err    error //
}

func fastCfg(maxRetries int) Config {
	return Config{
		MaxRetries:     maxRetries,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		BackoffFactor:  1,
	}
}

// doWith runs Do against a transport that replays answers in order and fails
// the test if Do sends more requests than were scripted. It returns the Result
// and the number of requests actually sent.
func doWith(t *testing.T, ctx context.Context, cfg Config, answers ...answer) (Result, int) {
	t.Helper()
	sent := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if sent >= len(answers) {
			t.Fatalf("request %d sent but only %d answers scripted", sent+1, len(answers))
		}
		a := answers[sent]
		sent++
		if a.err != nil {
			return nil, a.err
		}
		return &http.Response{
			StatusCode: a.status,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})}
	req, err := http.NewRequest(http.MethodPost, "http://example.test/trigger", nil)
	if err != nil {
		t.Fatal(err)
	}
	return Do(ctx, client, req, cfg, zerolog.New(io.Discard)), sent
}

func TestDoSucceedsOnSecondAttempt(t *testing.T) {
	res, sent := doWith(t, context.Background(), fastCfg(3),
		answer{status: http.StatusInternalServerError},
		answer{status: http.StatusOK},
	)
	if sent != 2 {
		t.Fatalf("sent %d requests, want 2", sent)
	}
	if res.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", res.Attempts)
	}
	if res.FinalError != nil {
		t.Errorf("FinalError = %v, want nil", res.FinalError)
	}
	if res.Response == nil || res.Response.StatusCode != http.StatusOK {
		t.Errorf("Response = %+v, want 200", res.Response)
	}
}

func TestDoExhaustsRetriesOn5xx(t *testing.T) {
	res, sent := doWith(t, context.Background(), fastCfg(3),
		answer{status: http.StatusServiceUnavailable},
		answer{status: http.StatusServiceUnavailable},
		answer{status: http.StatusServiceUnavailable},
		answer{status: http.StatusServiceUnavailable},
	)
	if sent != 4 {
		t.Fatalf("sent %d requests, want MaxRetries+1 = 4", sent)
	}
	if res.Attempts != 4 {
		t.Errorf("Attempts = %d, want 4", res.Attempts)
	}
	if res.Response == nil || res.Response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Response = %+v, want the last 503", res.Response)
	}
}

func TestDoExhaustsRetriesOnTransportError(t *testing.T) {
	boom := errors.New("connection refused")
	res, sent := doWith(t, context.Background(), fastCfg(2),
		answer{err: boom}, answer{err: boom}, answer{err: boom},
	)
	if sent != 3 {
		t.Fatalf("sent %d requests, want MaxRetries+1 = 3", sent)
	}
	if res.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", res.Attempts)
	}
	if res.FinalError == nil {
		t.Errorf("FinalError = nil, want the transport error")
	}
	if res.Response != nil {
		t.Errorf("Response = %+v, want nil", res.Response)
	}
}

func TestDoNonRetryableStopsAfterOne(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
		res, sent := doWith(t, context.Background(), fastCfg(3), answer{status: status})
		if sent != 1 {
			t.Errorf("%d: sent %d requests, want 1", status, sent)
		}
		if res.Attempts != 1 {
			t.Errorf("%d: Attempts = %d, want 1 (not MaxRetries+1)", status, res.Attempts)
		}
		if res.Response == nil || res.Response.StatusCode != status {
			t.Errorf("%d: Response = %+v", status, res.Response)
		}
	}
}

func TestDoCancelledDuringBackoffReportsRealAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := fastCfg(3)
	cfg.InitialBackoff = time.Hour // only the cancelled context can end the wait
	cfg.MaxBackoff = time.Hour

	sent := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		sent++
		cancel() // give up while Do is backing off for the retry
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})}
	req, err := http.NewRequest(http.MethodPost, "http://example.test/trigger", nil)
	if err != nil {
		t.Fatal(err)
	}

	res := Do(ctx, client, req, cfg, zerolog.New(io.Discard))
	if sent != 1 {
		t.Fatalf("sent %d requests, want 1", sent)
	}
	if res.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", res.Attempts)
	}
	if !errors.Is(res.FinalError, context.Canceled) {
		t.Errorf("FinalError = %v, want context.Canceled", res.FinalError)
	}
}
