package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookieName = "page_session"
	sessionTTL        = 12 * time.Hour
	stateCookieName   = "page_oidc_state"
	stateTTL          = 10 * time.Minute
)

// sessionPayload is the signed cookie body (auth-modes D3): stateless, so a
// leaked secret is the only way to forge one and rotating SESSION_SECRET
// revokes every live session at once.
type sessionPayload struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Exp   int64  `json:"exp"` // unix seconds
}

// mintSession sets the session cookie for the validated identity.
func (o *OIDC) mintSession(w http.ResponseWriter, r *http.Request, sub, email string) {
	payload, _ := json.Marshal(sessionPayload{
		Sub: sub, Email: email, Exp: time.Now().Add(sessionTTL).Unix(),
	})
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := base64.RawURLEncoding.EncodeToString(o.sign([]byte(body)))
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: body + "." + mac, Path: "/",
		MaxAge: int(sessionTTL / time.Second), HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: overHTTPS(r),
	})
}

// sessionFrom verifies and decodes the request's session cookie. Any absence,
// expiry, tampering, or malformed value is unauthenticated.
func (o *OIDC) sessionFrom(r *http.Request) (sessionPayload, bool) {
	ck, err := r.Cookie(sessionCookieName)
	if err != nil {
		return sessionPayload{}, false
	}
	body, mac, ok := strings.Cut(ck.Value, ".")
	if !ok {
		return sessionPayload{}, false
	}
	got, err := base64.RawURLEncoding.DecodeString(mac)
	if err != nil || !hmac.Equal(got, o.sign([]byte(body))) {
		return sessionPayload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return sessionPayload{}, false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return sessionPayload{}, false
	}
	if time.Now().Unix() >= p.Exp {
		return sessionPayload{}, false
	}
	return p, true
}

// sign is the shared HMAC-SHA256 over the base64 body.
func (o *OIDC) sign(b []byte) []byte {
	m := hmac.New(sha256.New, o.secret)
	m.Write(b)
	return m.Sum(nil)
}

// overHTTPS decides the Secure attribute: behind a TLS-terminating proxy the
// request arrives plain, so X-Forwarded-Proto counts too.
func overHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// clearCookie expires one of the auth cookies.
func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: overHTTPS(r),
	})
}
