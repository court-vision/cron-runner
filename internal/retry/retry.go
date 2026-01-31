package retry

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"
)

// Config holds retry configuration.
type Config struct {
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	BackoffFactor  float64
}

// Result contains the outcome of a retried operation.
type Result struct {
	Response   *http.Response
	Attempts   int
	TotalTime  time.Duration
	FinalError error
}

// IsRetryable determines if an HTTP response should trigger a retry.
func IsRetryable(resp *http.Response, err error) bool {
	if err != nil {
		return true // Network errors are retryable
	}
	if resp == nil {
		return true
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests: // 429
		return true
	case http.StatusServiceUnavailable: // 503
		return true
	case http.StatusGatewayTimeout: // 504
		return true
	case http.StatusBadGateway: // 502
		return true
	}

	// Retry on 5xx errors (server errors)
	if resp.StatusCode >= 500 {
		return true
	}

	return false
}

// CalculateBackoff computes the next backoff duration with exponential growth.
func CalculateBackoff(cfg Config, attempt int, resp *http.Response) time.Duration {
	// Check for Retry-After header on 429 responses
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if seconds, err := strconv.Atoi(retryAfter); err == nil {
				return time.Duration(seconds) * time.Second
			}
			if t, err := time.Parse(time.RFC1123, retryAfter); err == nil {
				return time.Until(t)
			}
		}
	}

	// Exponential backoff: initial * factor^attempt
	backoff := float64(cfg.InitialBackoff) * math.Pow(cfg.BackoffFactor, float64(attempt))
	if backoff > float64(cfg.MaxBackoff) {
		backoff = float64(cfg.MaxBackoff)
	}

	return time.Duration(backoff)
}

// Do executes an HTTP request with retry logic.
func Do(ctx context.Context, client *http.Client, req *http.Request, cfg Config, log zerolog.Logger) Result {
	start := time.Now()
	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := CalculateBackoff(cfg, attempt-1, lastResp)
			log.Info().
				Int("attempt", attempt+1).
				Dur("backoff", backoff).
				Msg("retrying after backoff")

			select {
			case <-ctx.Done():
				return Result{
					Attempts:   attempt,
					TotalTime:  time.Since(start),
					FinalError: ctx.Err(),
				}
			case <-time.After(backoff):
			}
		}

		// Clone the request for retry (body needs to be re-readable)
		reqCopy := req.Clone(ctx)

		log.Debug().
			Int("attempt", attempt+1).
			Str("url", req.URL.String()).
			Msg("sending request")

		resp, err := client.Do(reqCopy)
		lastResp = resp
		lastErr = err

		if err != nil {
			log.Warn().
				Err(err).
				Int("attempt", attempt+1).
				Msg("request failed with error")

			if IsRetryable(nil, err) && attempt < cfg.MaxRetries {
				continue
			}
			break
		}

		log.Debug().
			Int("status", resp.StatusCode).
			Int("attempt", attempt+1).
			Msg("received response")

		if !IsRetryable(resp, nil) {
			return Result{
				Response:  resp,
				Attempts:  attempt + 1,
				TotalTime: time.Since(start),
			}
		}

		if attempt < cfg.MaxRetries {
			log.Warn().
				Int("status", resp.StatusCode).
				Int("attempt", attempt+1).
				Msg("retryable error received")
		}
	}

	return Result{
		Response:   lastResp,
		Attempts:   cfg.MaxRetries + 1,
		TotalTime:  time.Since(start),
		FinalError: lastErr,
	}
}
