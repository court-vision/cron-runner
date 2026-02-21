package pipeline

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cron-runner/internal/config"

	"github.com/rs/zerolog"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestClient(baseURL string, transport http.RoundTripper) *Client {
	cfg := &config.Config{
		BackendURL:          baseURL,
		PipelineAuth:        "test-token",
		MaxRetries:          0,
		InitialBackoff:      time.Millisecond,
		MaxBackoff:          time.Millisecond,
		BackoffFactor:       1,
		RequestTimeout:      time.Second,
		PollInitialInterval: time.Millisecond,
		PollMaxInterval:     time.Millisecond,
		PollMaxWaitTime:     time.Second,
		LogLevel:            "info",
		LogJSON:             true,
	}

	client := NewClient(cfg, zerolog.New(io.Discard))
	client.httpClient.Transport = transport
	return client
}

func TestTriggerEndpointSuccess200(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})

	client := newTestClient("http://example.test", transport)
	result := client.TriggerEndpoint(context.Background(), "/test")

	if !result.Success {
		t.Fatalf("expected success true, got false with error: %v", result.Error)
	}
	if result.Error != nil {
		t.Fatalf("expected nil error, got: %v", result.Error)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", result.StatusCode)
	}
	if result.ResponseBody != "ok" {
		t.Fatalf("expected response body %q, got %q", "ok", result.ResponseBody)
	}
}

func TestTriggerEndpointFailure500(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("boom")),
			Header:     make(http.Header),
		}, nil
	})

	client := newTestClient("http://example.test", transport)
	result := client.TriggerEndpoint(context.Background(), "/test")

	if result.Success {
		t.Fatalf("expected success false, got true")
	}
	if result.Error == nil {
		t.Fatalf("expected non-nil error")
	}
	if result.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", result.StatusCode)
	}
	if result.ResponseBody != "boom" {
		t.Fatalf("expected response body %q, got %q", "boom", result.ResponseBody)
	}
}

func TestTriggerEndpointFailure404(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("missing")),
			Header:     make(http.Header),
		}, nil
	})

	client := newTestClient("http://example.test", transport)
	result := client.TriggerEndpoint(context.Background(), "/test")

	if result.Success {
		t.Fatalf("expected success false, got true")
	}
	if result.Error == nil {
		t.Fatalf("expected non-nil error")
	}
	if result.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", result.StatusCode)
	}
	if result.ResponseBody != "missing" {
		t.Fatalf("expected response body %q, got %q", "missing", result.ResponseBody)
	}
}
