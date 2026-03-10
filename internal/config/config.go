package config

import (
	"errors"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the cron-runner service.
// Job-specific settings (endpoints, loop intervals, etc.) are hardcoded in
// internal/jobs/registry.go — only global service settings live here.
type Config struct {
	// Backend connection
	BackendURL   string
	PipelineAuth string

	// Retry settings (used by pipeline client for all requests)
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	BackoffFactor  float64

	// HTTP client settings
	RequestTimeout time.Duration

	// Job polling settings (used by PollTask via pipeline client)
	PollInitialInterval time.Duration
	PollMaxInterval     time.Duration
	PollMaxWaitTime     time.Duration

	// HTTP server
	HTTPPort string

	// Graceful shutdown drain window
	DrainTimeout time.Duration

	// Logging
	LogLevel string
	LogJSON  bool
}

// Load reads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		BackendURL:          getEnvOrDefault("BACKEND_URL", "https://api.courtvision.dev"),
		PipelineAuth:        os.Getenv("PIPELINE_API_TOKEN"),
		MaxRetries:          getEnvIntOrDefault("MAX_RETRIES", 3),
		InitialBackoff:      getEnvDurationOrDefault("INITIAL_BACKOFF", 2*time.Second),
		MaxBackoff:          getEnvDurationOrDefault("MAX_BACKOFF", 30*time.Second),
		BackoffFactor:       getEnvFloatOrDefault("BACKOFF_FACTOR", 2.0),
		RequestTimeout:      getEnvDurationOrDefault("REQUEST_TIMEOUT", 30*time.Second),
		PollInitialInterval: getEnvDurationOrDefault("POLL_INITIAL_INTERVAL", 5*time.Second),
		PollMaxInterval:     getEnvDurationOrDefault("POLL_MAX_INTERVAL", 30*time.Second),
		PollMaxWaitTime:     getEnvDurationOrDefault("POLL_MAX_WAIT_TIME", 15*time.Minute),
		HTTPPort:            getEnvOrDefault("HTTP_PORT", "8082"),
		DrainTimeout:        getEnvDurationOrDefault("DRAIN_TIMEOUT", 30*time.Second),
		LogLevel:            getEnvOrDefault("LOG_LEVEL", "info"),
		LogJSON:             getEnvBoolOrDefault("LOG_JSON", true),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks that required configuration is present.
func (c *Config) Validate() error {
	if c.BackendURL == "" {
		return errors.New("BACKEND_URL environment variable is required")
	}
	if c.PipelineAuth == "" {
		return errors.New("PIPELINE_API_TOKEN environment variable is required")
	}
	return nil
}

func getEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvIntOrDefault(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvFloatOrDefault(key string, defaultVal float64) float64 {
	if val := os.Getenv(key); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

func getEnvDurationOrDefault(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}

func getEnvBoolOrDefault(key string, defaultVal bool) bool {
	if val := os.Getenv(key); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return defaultVal
}
