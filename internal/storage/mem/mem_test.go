package mem

import (
	"context"
	"strings"
	"testing"

	"page/internal/storage/storagetest"
)

// The mem driver must pass the same conformance suite as s3compat (D7).
func TestConformance(t *testing.T) {
	storagetest.Run(t, context.Background(), New())
}

func TestCopyNoOpOnEmptySourcePrefix(t *testing.T) {
	ctx := context.Background()
	s := New()
	if err := s.Put(ctx, "k/x", "text/plain", strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// An empty source prefix would match every key; it must be refused as a
	// no-op rather than cloning the whole bucket.
	if err := s.Copy(ctx, "", "dst/"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (nothing copied)", s.Len())
	}
}

func TestPutCopiesReaderBytes(t *testing.T) {
	ctx := context.Background()
	s := New()
	data := []byte("mutable")
	if err := s.Put(ctx, "k", "text/plain", strings.NewReader(string(data))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	data[0] = 'X' // mutate after Put; stored copy must be unaffected
	obj, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	buf := make([]byte, obj.Size)
	_, _ = obj.Reader.Read(buf)
	if string(buf) != "mutable" {
		t.Fatalf("stored bytes mutated: %q", buf)
	}
}
