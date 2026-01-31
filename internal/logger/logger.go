package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

// New creates a configured zerolog logger.
func New(level string, jsonFormat bool) zerolog.Logger {
	var logger zerolog.Logger

	if jsonFormat {
		logger = zerolog.New(os.Stdout)
	} else {
		logger = zerolog.New(zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: time.RFC3339,
		})
	}

	logger = logger.With().
		Timestamp().
		Str("service", "cron-runner").
		Logger()

	switch level {
	case "debug":
		logger = logger.Level(zerolog.DebugLevel)
	case "info":
		logger = logger.Level(zerolog.InfoLevel)
	case "warn":
		logger = logger.Level(zerolog.WarnLevel)
	case "error":
		logger = logger.Level(zerolog.ErrorLevel)
	default:
		logger = logger.Level(zerolog.InfoLevel)
	}

	return logger
}
