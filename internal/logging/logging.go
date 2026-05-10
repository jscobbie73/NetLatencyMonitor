// Package logging configures the process-wide structured logger.
package logging

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Init configures the global zerolog logger from env vars and returns it.
//
//	NLM_LOG_LEVEL  trace|debug|info|warn|error  (default info)
//	NLM_LOG_FORMAT json|console                  (default json)
func Init() zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339Nano

	level := parseLevel(os.Getenv("NLM_LOG_LEVEL"))
	zerolog.SetGlobalLevel(level)

	var out io.Writer = os.Stdout
	if strings.EqualFold(os.Getenv("NLM_LOG_FORMAT"), "console") {
		out = zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	}

	logger := zerolog.New(out).With().Timestamp().Logger()
	log.Logger = logger
	return logger
}

func parseLevel(s string) zerolog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "fatal":
		return zerolog.FatalLevel
	default:
		return zerolog.InfoLevel
	}
}
