package ingest

import (
	"testing"
)

func TestIsSignedURL(t *testing.T) {
	signed := []string{
		"https://fonts.gstatic.com/f?a.ttf?Expires=123&Signature=abc",
		"https://cdn.example.com/x.png?X-Amz-Signature=deadbeef",
		"https://cdn.example.com/x.png?token=secret",
		"https://cdn.example.com/x.png?sig=abc",
		"https://cdn.example.com/x.png?x-amz-expires=900",
	}
	for _, u := range signed {
		pl := planRef(t, u)
		if pl.action != planBake {
			t.Errorf("signed %s: action = %v, want bake", u, pl.action)
		}
	}
	plain := []string{
		"https://fonts.googleapis.com/css2?family=Inter",
		"https://cdn.jsdelivr.net/npm/lib.js",
		"https://cdn.example.com/plain.png?v=2",
	}
	for _, u := range plain {
		if IsSignedURL(mustURL(t, u)) {
			t.Errorf("plain %s flagged signed", u)
		}
	}
}

func TestKeepRulesAllowlist(t *testing.T) {
	rules := KeepRules{
		Fonts: []string{"fonts.googleapis.com", "fonts.gstatic.com"},
		JS:    []string{"cdn.jsdelivr.net"},
		Icons: []string{"use.fontawesome.com"},
		Misc:  []string{"plausible.io"},
	}
	kept := []string{
		"https://fonts.googleapis.com/css2?family=Inter",
		"https://fonts.gstatic.com/x.woff2",
		"https://cdn.jsdelivr.net/npm/x.js",
		"https://use.fontawesome.com/x.css",
		"https://plausible.io/js/script.js",
	}
	for _, u := range kept {
		if !rules.ShouldKeep(mustURL(t, u)) {
			t.Errorf("ShouldKeep(%s) = false, want true", u)
		}
	}
	unknown := []string{"https://cdn.framer.com/x.png", "https://evil.example/x.png"}
	for _, u := range unknown {
		if rules.ShouldKeep(mustURL(t, u)) {
			t.Errorf("ShouldKeep(%s) = true, want false", u)
		}
	}
}

func TestClassifierOverridesOrder(t *testing.T) {
	// A signed URL on an allowlisted host must still bake (D3 order 1).
	rules := KeepRules{Fonts: []string{"fonts.gstatic.com"}}
	pl := planRefWith(t, "https://fonts.gstatic.com/x.woff2?Expires=1&Signature=z", rules)
	if pl.action != planBake {
		t.Fatalf("signed+allowlisted: action = %v, want bake", pl.action)
	}
}
