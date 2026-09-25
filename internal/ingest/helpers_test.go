package ingest

import (
	"net/url"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func planRefWith(t *testing.T, raw string, rules KeepRules) *plan {
	t.Helper()
	b := &baker{
		slug: "test-1", files: map[string][]byte{}, keep: rules,
		plans: map[string]*plan{}, toStore: map[string]File{},
	}
	return b.planFor(raw, "")
}

func planRef(t *testing.T, raw string) *plan {
	return planRefWith(t, raw, KeepRules{})
}
