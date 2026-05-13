// Package logging provides structured logging for Symphony.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Logger is the global logger instance.
var Logger *slog.Logger

// Init initializes the global logger with the specified level.
func Init(level string) {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: logLevel,
	}
	Logger = slog.New(slog.NewJSONHandler(os.Stdout, opts))
	slog.SetDefault(Logger)
}

// Int returns a slog.Attr for an int value.
func Int(key string, value int) slog.Attr {
	return slog.Int(key, value)
}

// String returns a slog.Attr for a string value.
func String(key, value string) slog.Attr {
	return slog.String(key, value)
}

// Err returns a slog.Attr for an error value.
func Err(err error) slog.Attr {
	if err == nil {
		return slog.String("error", "<nil>")
	}
	return slog.String("error", err.Error())
}

// Duration returns a slog.Attr for a time.Duration value.
func Duration(key string, value time.Duration) slog.Attr {
	return slog.String(key, formatDuration(value))
}

func formatDuration(d time.Duration) string {
	ms := d.Milliseconds()
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	sec := ms / 1000
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	min := sec / 60
	return fmt.Sprintf("%dm%ds", min, sec%60)
}

type ctxKeyType int

const (
	ctxKeyIssueID ctxKeyType = iota
	ctxKeyIssueIdentifier
	ctxKeySessionID
)

// WithIssue returns a context with issue fields.
func WithIssue(ctx context.Context, issueID, identifier string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyIssueID, issueID)
	ctx = context.WithValue(ctx, ctxKeyIssueIdentifier, identifier)
	return ctx
}

// WithSession returns a context with session fields.
func WithSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, ctxKeySessionID, sessionID)
}

// FromContext extracts logging fields from context.
func FromContext(ctx context.Context) []any {
	var attrs []any
	if id, ok := ctx.Value(ctxKeyIssueID).(string); ok && id != "" {
		attrs = append(attrs, slog.String("issue_id", id))
	}
	if id, ok := ctx.Value(ctxKeyIssueIdentifier).(string); ok && id != "" {
		attrs = append(attrs, slog.String("issue_identifier", id))
	}
	if id, ok := ctx.Value(ctxKeySessionID).(string); ok && id != "" {
		attrs = append(attrs, slog.String("session_id", id))
	}
	return attrs
}

// Info logs an info message.
func Info(msg string, attrs ...any) {
	Logger.Info(msg, attrs...)
}

// Warn logs a warning message.
func Warn(msg string, attrs ...any) {
	Logger.Warn(msg, attrs...)
}

// ErrLog logs an error message.
func ErrLog(msg string, attrs ...any) {
	Logger.Error(msg, attrs...)
}

// Debug logs a debug message.
func Debug(msg string, attrs ...any) {
	Logger.Debug(msg, attrs...)
}