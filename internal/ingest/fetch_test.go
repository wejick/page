package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetcherCapsAndFailures(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer slow.Close()

	oversize := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 64))
	}))
	defer oversize.Close()

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer denied.Close()

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PNG-CONTENTS"))
	}))
	defer ok.Close()

	f := NewFetcher(16, 100*time.Millisecond, 5*time.Second, 4)
	results := f.FetchAll(context.Background(), []string{
		ok.URL, slow.URL, oversize.URL, denied.URL, "http://127.0.0.1:1/unreachable",
	})

	if r := results[ok.URL]; r.Err != nil || string(r.Data) != "PNG-CONTENTS" {
		t.Fatalf("ok fetch: err=%v data=%q", r.Err, r.Data)
	}
	if r := results[slow.URL]; r.Err == nil {
		t.Fatal("slow fetch succeeded, want timeout error")
	}
	if r := results[oversize.URL]; r.Err == nil || !strings.Contains(r.Err.Error(), "exceeds") {
		t.Fatalf("oversize err = %v, want cap error", r.Err)
	}
	if r := results[denied.URL]; r.Err == nil || !strings.Contains(r.Err.Error(), "403") {
		t.Fatalf("denied err = %v, want 403 error", r.Err)
	}
	if r := results["http://127.0.0.1:1/unreachable"]; r.Err == nil {
		t.Fatal("unreachable fetch succeeded")
	}
	// Every input URL has an entry.
	if len(results) != 5 {
		t.Fatalf("results = %d, want 5", len(results))
	}
}

func TestFetcherBudgetShared(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer hang.Close()

	// Budget of 400ms across two rounds: the second round must fail fast.
	f := NewFetcher(16, 5*time.Second, 400*time.Millisecond, 1)
	ctx := context.Background()
	r1 := f.FetchAll(ctx, []string{hang.URL})[hang.URL]
	_ = r1
	start := time.Now()
	r2 := f.FetchAll(ctx, []string{hang.URL})[hang.URL]
	if time.Since(start) > 2*time.Second {
		t.Fatal("second round did not respect the shared budget")
	}
	_ = r2
}
