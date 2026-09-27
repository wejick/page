package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"page/internal/auth/oidctest"
	"page/internal/config"
)

func testOIDC(t *testing.T) *OIDC {
	t.Helper()
	idp := oidctest.New(t)
	o, err := NewOIDC(t.Context(), config.OIDC{Issuer: idp.Issuer, ClientID: idp.ClientID,
		ClientSecret: idp.ClientSecret, RedirectURL: "https://app.test/auth/callback"}, "test-session-secret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	return o
}

// sessionValue builds the raw cookie value for an arbitrary payload — the
// test's own signing path, mirroring mintSession.
func sessionValue(t *testing.T, o *OIDC, p sessionPayload) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := base64.RawURLEncoding.EncodeToString(o.sign([]byte(body)))
	return body + "." + mac
}

func requestWithSession(value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/pages", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
	return r
}

func TestSessionRoundTrip(t *testing.T) {
	o := testOIDC(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	o.mintSession(w, r, "user-1", "u@example.com")

	ck := w.Header().Get("Set-Cookie")
	if !strings.Contains(ck, "HttpOnly") || !strings.Contains(ck, "SameSite=Lax") || !strings.Contains(ck, "Path=/") {
		t.Fatalf("cookie attributes = %q, want HttpOnly, SameSite=Lax, Path=/", ck)
	}
	if strings.Contains(ck, "Secure") {
		t.Fatalf("plain-http request must not set Secure: %q", ck)
	}
	name := ck[:strings.Index(ck, "=")]
	if name != sessionCookieName {
		t.Fatalf("cookie name = %q", name)
	}
	value := ck[strings.Index(ck, "=")+1 : strings.Index(ck, ";")]

	got, ok := o.sessionFrom(requestWithSession(value))
	if !ok {
		t.Fatalf("fresh session rejected")
	}
	if got.Sub != "user-1" || got.Email != "u@example.com" {
		t.Fatalf("session = %+v", got)
	}
	if got.Exp <= time.Now().Unix() {
		t.Fatalf("exp = %d, want in the future (12h TTL)", got.Exp)
	}
}

func TestSessionSecureOverHTTPS(t *testing.T) {
	o := testOIDC(t)
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	o.mintSession(w, r, "user-1", "u@example.com")
	if !strings.Contains(w.Header().Get("Set-Cookie"), "Secure") {
		t.Fatalf("proxied HTTPS request must set Secure")
	}
}

func TestSessionRejections(t *testing.T) {
	o := testOIDC(t)
	good := sessionValue(t, o, sessionPayload{Sub: "u", Email: "e", Exp: time.Now().Add(time.Hour).Unix()})

	tests := []struct {
		name  string
		value string
	}{
		{name: "expired", value: sessionValue(t, o, sessionPayload{Sub: "u", Exp: time.Now().Add(-time.Minute).Unix()})},
		{name: "tampered payload", value: func() string {
			body, mac, _ := strings.Cut(good, ".")
			_ = mac
			// Flip a byte inside the payload's JSON by re-encoding modified claims.
			raw, _ := base64.RawURLEncoding.DecodeString(body)
			var p sessionPayload
			_ = json.Unmarshal(raw, &p)
			p.Sub = "attacker"
			mod, _ := json.Marshal(p)
			return base64.RawURLEncoding.EncodeToString(mod) + "." + mac
		}()},
		{name: "bad mac", value: good[:strings.LastIndex(good, ".")] + ".AAAA"},
		{name: "garbage", value: "not-a-session"},
		{name: "no dot", value: "nodot"},
		{name: "empty", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := o.sessionFrom(requestWithSession(tt.value)); ok {
				t.Fatalf("session accepted, want rejected")
			}
		})
	}
	if _, ok := o.sessionFrom(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Fatalf("request without cookie accepted")
	}
}

// TestSessionWrongSecret proves a session minted under one secret does not
// verify under another — rotation revokes.
func TestSessionWrongSecret(t *testing.T) {
	o := testOIDC(t)
	// Same shape, different signing secret — no discovery needed here.
	other := &OIDC{issuer: "https://idp.other", clientID: "c", secret: []byte("a-different-secret")}
	p := sessionPayload{Sub: "u", Exp: time.Now().Add(time.Hour).Unix()}
	raw, _ := json.Marshal(p)
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, other.secret)
	mac.Write([]byte(body))
	value := body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, ok := o.sessionFrom(requestWithSession(value)); ok {
		t.Fatalf("session from another secret accepted")
	}
}
