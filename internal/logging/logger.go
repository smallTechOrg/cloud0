// Package logging builds cloud0's structured JSON logger (log/slog on stdout)
// and the HTTP request-logging middleware.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// ParseLevel maps debug|info|warn|error to a slog.Level; unknown → info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New returns a JSON logger writing to stdout at the given level.
func New(level string) *slog.Logger {
	return NewWriter(os.Stdout, level)
}

// NewWriter is New with an explicit destination (tests capture output).
func NewWriter(w io.Writer, level string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: ParseLevel(level)})
	return slog.New(h)
}
