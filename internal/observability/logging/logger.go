package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New builds the gateway's default logger: a JSON handler writing to w, wrapped
// in a RedactHandler so secrets never reach the output. level accepts
// "debug"|"info"|"warn"|"error" (case-insensitive); anything else means info.
func New(w io.Writer, level string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(NewRedactHandler(h))
}

// parseLevel maps a level string to a slog.Level, defaulting to info.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
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
