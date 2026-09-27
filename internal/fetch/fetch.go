// Package fetch performs the system's outbound web fetches: entry-document
// imports and asset baking share one SSRF-guarded transport (import-by-url
// D2/D3). It knows HTTP — not pages, slugs, or storage — and depends on
// nothing else in this module.
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

// Typed fetch outcomes; the upload API maps them to status codes
// (import-by-url D6).
var (
	ErrInvalidURL  = errors.New("fetch: invalid url")
	ErrBlocked     = errors.New("fetch: blocked address")
	ErrUnreachable = errors.New("fetch: unreachable")
	ErrTooLarge    = errors.New("fetch: response exceeds size cap")
	ErrNotHTML     = errors.New("fetch: response is not html")
)

// UserAgent identifies the system to source servers.
const UserAgent = "static-page-hosting-baker/1.0"

// GuardFunc reports whether dialing the given IP is permitted.
type GuardFunc func(ip net.IP) bool

// Standard is the production guard: it refuses loopback, private (RFC1918
// and IPv6 ULA), link-local (incl. 169.254.169.254), CGNAT, unspecified, and
// multicast addresses (import-by-url D3).
func Standard(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		// CGNAT 100.64.0.0/10 is not covered by IsPrivate.
		return !(ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127)
	}
	return true
}

// Permissive allows everything. Tests use it because httptest serves on
// loopback, which Standard refuses (import-by-url D3) — the one seam the
// "mock external HTTP only" testing rule needs, and it is external HTTP.
func Permissive(net.IP) bool { return true }

// NewTransport builds an http.Transport whose dialer resolves the target
// host once, dials only a guard-approved IP, and never proxies. Resolving
// and dialing the same checked address closes the DNS-rebinding window;
// TLS keeps the hostname from the URL, so SNI and certificate verification
// are unaffected (import-by-url D3).
func NewTransport(guard GuardFunc) *http.Transport {
	return &http.Transport{
		Proxy: nil, // an env proxy would bypass the guard entirely
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("fetch: %w", err)
			}
			if ip := net.ParseIP(host); ip != nil {
				if !guard(ip) {
					return nil, fmt.Errorf("%w: %s", ErrBlocked, ip)
				}
				return new(net.Dialer).DialContext(ctx, network, addr)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("%w: lookup %s: %v", ErrUnreachable, host, err)
			}
			for _, ia := range ips {
				if guard(ia.IP) {
					return new(net.Dialer).DialContext(ctx, network,
						net.JoinHostPort(ia.IP.String(), port))
				}
			}
			return nil, fmt.Errorf("%w: %s", ErrBlocked, host)
		},
	}
}

// Entry is a fetched entry document: UTF-8 HTML plus the final URL after
// redirects — the resolution base for the document's relative references
// (import-by-url D2).
type Entry struct {
	Data     []byte
	FinalURL *url.URL
}

// EntryFetcher fetches a single entry document over the guarded transport.
type EntryFetcher struct {
	client   *http.Client
	maxBytes int64
}

// NewEntryFetcher builds an entry fetcher: timeout bounds the whole request
// including redirect hops; maxBytes caps the response body.
func NewEntryFetcher(maxBytes int64, timeout time.Duration, guard GuardFunc) *EntryFetcher {
	return &EntryFetcher{
		client: &http.Client{
			Timeout:   timeout,
			Transport: NewTransport(guard),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("%w: stopped after 10 redirects", ErrUnreachable)
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return fmt.Errorf("%w: scheme %q", ErrInvalidURL, req.URL.Scheme)
				}
				return nil
			},
		},
		maxBytes: maxBytes,
	}
}

// Fetch retrieves rawURL, validates it is an HTML document, and normalizes
// it to UTF-8. Failures are typed Err* so callers can map status codes.
func (f *EntryFetcher) Fetch(ctx context.Context, rawURL string) (*Entry, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: %q", ErrInvalidURL, rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlocked) || errors.Is(err, ErrInvalidURL) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUnreachable, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %v", ErrUnreachable, err)
	}
	if int64(len(data)) > f.maxBytes {
		return nil, ErrTooLarge
	}
	data, err = toUTF8(data, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("fetch: decode charset: %w", err)
	}
	if !LooksLikeHTML(data) {
		return nil, ErrNotHTML
	}
	return &Entry{
		Data:     data,
		FinalURL: resp.Request.URL,
	}, nil
}

// toUTF8 converts the document per its declared charset (response header or
// meta tag); UTF-8 and undeclared documents pass through unchanged
// (import-by-url D8).
func toUTF8(data []byte, contentType string) ([]byte, error) {
	r, err := charset.NewReader(bytes.NewReader(data), contentType)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// htmlMarkers identify an HTML document even when served as text/plain.
var htmlMarkers = []string{
	"<!doctype html", "<html", "<head", "<body", "<div", "<script", "<img ",
}

// LooksLikeHTML reports whether data reads as an HTML document. The upload
// path's file branch and entry imports share it, so both accept the same
// content (import-by-url D6).
func LooksLikeHTML(data []byte) bool {
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	if strings.HasPrefix(http.DetectContentType(sample), "text/html") {
		return true
	}
	low := strings.ToLower(string(sample))
	for _, m := range htmlMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}
