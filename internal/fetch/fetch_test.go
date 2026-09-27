package fetch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStandardGuard(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"93.184.216.34", true},
		{"8.8.8.8", true},
		{"2606:2800:220:1:248:1893:25c8:1946", true},
		{"127.0.0.1", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"172.32.0.1", true},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		{"100.128.0.1", true},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"::1", false},
		{"fc00::1", false},
		{"fd12::1", false},
		{"fe80::1", false},
		{"ff02::1", false},
	}
	for _, tc := range cases {
		if got := Standard(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("Standard(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestEntryFetchRedirectsToFinalURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/final/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<h1>final</h1>")
	})
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final/", http.StatusMovedPermanently)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ef := NewEntryFetcher(1<<20, 5*time.Second, Permissive)
	e, err := ef.Fetch(context.Background(), srv.URL+"/start")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(string(e.Data), "final") {
		t.Errorf("body = %q, want the final document", e.Data)
	}
	if e.FinalURL.Path != "/final/" {
		t.Errorf("final URL = %s, want path /final/", e.FinalURL)
	}
}

func TestEntryFetchCharsetNormalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		_, _ = w.Write([]byte("<html><body>caf\xe9</body></html>"))
	}))
	defer srv.Close()

	ef := NewEntryFetcher(1<<20, 5*time.Second, Permissive)
	e, err := ef.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(string(e.Data), "café") {
		t.Errorf("body = %q, want latin-1 é normalized to UTF-8", e.Data)
	}
}

func TestEntryFetchFailures(t *testing.T) {
	jsonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"a":1}`)
	}))
	defer jsonSrv.Close()

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer slowSrv.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	blockedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached the server despite the guard")
	}))
	defer blockedSrv.Close()

	// Redirect from an allowed first hop into a refused second hop: every
	// dial goes through the guard, so the redirect hop is refused too.
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, blockedSrv.URL+"/x", http.StatusFound)
	}))
	defer first.Close()
	dials := 0
	hopGuard := GuardFunc(func(net.IP) bool { dials++; return dials == 1 })

	ef := NewEntryFetcher(1<<20, 5*time.Second, Permissive)
	oversize := NewEntryFetcher(16, 5*time.Second, Permissive)
	impatient := NewEntryFetcher(1<<20, 100*time.Millisecond, Permissive)
	guarded := NewEntryFetcher(1<<20, 5*time.Second, Standard)
	hopGuarded := NewEntryFetcher(1<<20, 5*time.Second, hopGuard)

	cases := []struct {
		name string
		url  string
		ef   *EntryFetcher
		want error
	}{
		{"json body", jsonSrv.URL, ef, ErrNotHTML},
		{"source 500", errSrv.URL, ef, ErrUnreachable},
		{"dead target", deadURL, ef, ErrUnreachable},
		{"timeout", slowSrv.URL, impatient, ErrUnreachable},
		{"invalid scheme", "file:///etc/passwd", ef, ErrInvalidURL},
		{"no host", "not a url", ef, ErrInvalidURL},
		{"oversize body", "data:text/html," + strings.Repeat("x", 64), ef, ErrInvalidURL}, // data: rejected at validation
		{"loopback refused", blockedSrv.URL, guarded, ErrBlocked},
		{"redirect hop refused", first.URL, hopGuarded, ErrBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.ef.Fetch(context.Background(), tc.url)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// Oversize needs an HTML body over the cap on a reachable server.
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html><body>"+strings.Repeat("x", 64)+"</body></html>")
	}))
	defer big.Close()
	if _, err := oversize.Fetch(context.Background(), big.URL); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize err = %v, want %v", err, ErrTooLarge)
	}
}
