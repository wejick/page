package httpx

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func TestRequestID(t *testing.T) {
	id := NewRequestID()
	if len(id) != 32 {
		t.Fatalf("request ID length = %d, want 32 hex chars", len(id))
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("request ID %q is not lowercase hex", id)
		}
	}
	if NewRequestID() == id {
		t.Fatal("two generated request IDs are equal")
	}
	if RequestID(context.Background()) != "" {
		t.Fatal("RequestID on a bare context should be empty")
	}
}

func TestLog(t *testing.T) {
	fallback := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	// Bare context: falls back.
	if Log(context.Background(), fallback) != fallback {
		t.Fatal("Log should fall back without a stored logger")
	}
	// Stamped logger wins, so request-scoped attributes survive.
	stamped := fallback.With("request_id", "abc")
	if got := Log(WithLog(context.Background(), stamped), fallback); got != stamped {
		t.Fatal("Log should return the stored logger")
	}
}
