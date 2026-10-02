// Package httpx carries per-request observability context shared by the
// serve middleware and the admin handlers: the request ID and the logger
// pre-stamped with request-scoped attributes (observability D3). It is a
// leaf package so handler packages can read the context without importing
// serve.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

type ctxKey int

const (
	logKey ctxKey = iota
	requestIDKey
)

// WithLog stores a request-scoped logger in the context. The middleware
// stamps it once per request; handlers read it with Log.
func WithLog(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, logKey, l)
}

// Log returns the request-scoped logger, falling back to fallback when the
// request never passed through the middleware (direct handler tests).
func Log(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(logKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return fallback
}

// WithRequestID stores the request ID in the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID returns the request ID, or "" when absent.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// NewRequestID generates a random 128-bit hex request ID. It honors no
// caller input; honoring a client-supplied X-Request-ID is the caller's
// decision (the middleware accepts and validates it before storing).
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is unrecoverable; a zero ID is better than a
		// panic on the serve path.
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}
