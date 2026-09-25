package ingest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Fetcher fetches external assets for baking, bounded per design D10:
// per-request timeout, total budget, concurrency pool, per-asset size cap.
type Fetcher struct {
	client    *http.Client
	budget    time.Duration
	sem       chan struct{}
	maxBytes  int64
	userAgent string
}

// NewFetcher builds a fetcher from ingest caps.
func NewFetcher(maxAssetBytes int64, timeout, budget time.Duration, concurrency int) *Fetcher {
	if concurrency <= 0 {
		concurrency = 8
	}
	return &Fetcher{
		client:    &http.Client{Timeout: timeout},
		budget:    budget,
		sem:       make(chan struct{}, concurrency),
		maxBytes:  maxAssetBytes,
		userAgent: "static-page-hosting-baker/1.0",
	}
}

// FetchResult is the outcome of fetching one URL.
type FetchResult struct {
	Data []byte
	Err  error
}

// FetchAll fetches every URL concurrently under one total budget. The map has
// an entry for every input URL; failures carry Err (never a panic).
func (f *Fetcher) FetchAll(ctx context.Context, urls []string) map[string]FetchResult {
	out := make(map[string]FetchResult, len(urls))
	if len(urls) == 0 {
		return out
	}
	budgetCtx, cancel := context.WithTimeout(ctx, f.budget)
	defer cancel()

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			select {
			case f.sem <- struct{}{}:
				defer func() { <-f.sem }()
			case <-budgetCtx.Done():
				mu.Lock()
				out[u] = FetchResult{Err: fmt.Errorf("fetch budget exhausted")}
				mu.Unlock()
				return
			}
			data, err := f.fetchOne(budgetCtx, u)
			mu.Lock()
			out[u] = FetchResult{Data: data, Err: err}
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	return out
}

func (f *Fetcher) fetchOne(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(data)) > f.maxBytes {
		return nil, fmt.Errorf("asset exceeds %d bytes", f.maxBytes)
	}
	return data, nil
}
