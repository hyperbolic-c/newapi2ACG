// Package observability sets up structured logging and process metrics.
package observability

import (
	"io"
	"log/slog"
	"strings"
)

// NewLogger returns a JSON logger. Never pass records, secrets or DSNs as
// attributes: log identities, counts and positions only.
func NewLogger(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l}))
}
