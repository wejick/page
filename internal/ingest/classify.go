package ingest

import (
	"net/url"
	"strings"
)

// KeepRules is the categorized keep-external allowlist (design D3). It lives
// in configuration; hosts are lowercase hostnames.
type KeepRules struct {
	Fonts []string
	JS    []string
	Icons []string
	Misc  []string
}

// ShouldKeep reports whether the URL's host is on the allowlist. The category
// only organizes configuration — the decision is binary keep vs bake.
func (r KeepRules) ShouldKeep(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	for _, group := range [][]string{r.Fonts, r.JS, r.Icons, r.Misc} {
		for _, h := range group {
			if host == h {
				return true
			}
		}
	}
	return false
}

// signedParams are query parameters that mark a URL as expiring/signed.
// Such refs are baked regardless of host (D3, order 1).
var signedParams = []string{
	"expires", "expiresat", "signature", "sig", "token",
	"x-amz-signature", "x-amz-expires", "x-goog-signature",
	"awsaccesskeyid",
}

// IsSignedURL reports whether the URL carries signed/expiring query params.
// Parameter names are compared case-insensitively (sources use Expires,
// expires, Signature, …).
func IsSignedURL(u *url.URL) bool {
	for k := range u.Query() {
		for _, want := range signedParams {
			if strings.EqualFold(k, want) {
				return true
			}
		}
	}
	return false
}
