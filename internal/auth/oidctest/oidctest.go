// Package oidctest is an in-process fake OIDC IdP for tests: discovery,
// authorization endpoint, token endpoint issuing RS256 ID tokens, and a
// JWKS endpoint — all over httptest, no live network. Both the internal/auth
// unit tests and the e2e journeys drive it.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

const kid = "test-key"

// IDP is a minimal but honest IdP: the code it issues is bound to the nonce
// from the authorization request, and the token endpoint requires the
// client credentials the flow sends.
type IDP struct {
	Server *httptest.Server
	Issuer string

	ClientID     string
	ClientSecret string
	RedirectURL  string // the app's /auth/callback; set before the flow runs

	key    *rsa.PrivateKey
	mu     sync.Mutex
	nonces map[string]string // code → nonce from the authorization request
}

// New starts the fake IdP.
func New(t *testing.T) *IDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("fake idp key: %v", err)
	}
	p := &IDP{
		ClientID:     "page",
		ClientSecret: "idp-secret",
		key:          key,
		nonces:       map[string]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 p.Server.URL,
			"authorization_endpoint": p.Server.URL + "/authorize",
			"token_endpoint":         p.Server.URL + "/token",
			"jwks_uri":               p.Server.URL + "/jwks",
		})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code, err := randHex(16)
		if err != nil {
			http.Error(w, "rand", http.StatusInternalServerError)
			return
		}
		p.mu.Lock()
		p.nonces[code] = q.Get("nonce")
		p.mu.Unlock()
		dest, _ := url.Parse(p.RedirectURL)
		destQ := dest.Query()
		destQ.Set("code", code)
		destQ.Set("state", q.Get("state"))
		dest.RawQuery = destQ.Encode()
		http.Redirect(w, r, dest.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("client_id") != p.ClientID || r.Form.Get("client_secret") != p.ClientSecret {
			http.Error(w, "bad client credentials", http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		nonce := p.nonces[r.Form.Get("code")]
		p.mu.Unlock()
		now := time.Now()
		idt := p.SignIDToken(map[string]any{
			"iss":   p.Server.URL,
			"sub":   "user-1",
			"email": "user@example.com",
			"aud":   p.ClientID,
			"exp":   now.Add(5 * time.Minute).Unix(),
			"iat":   now.Unix(),
			"nonce": nonce,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "fake-access",
			"token_type":   "bearer",
			"id_token":     idt,
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(p.key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.PublicKey.E)).Bytes())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig", "n": n, "e": e,
			}},
		})
	})

	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Server.Close)
	p.Issuer = p.Server.URL
	return p
}

// SignIDToken signs arbitrary claims with the IdP's key, for tests that need
// malformed, expired, or otherwise hostile tokens.
func (p *IDP) SignIDToken(claims map[string]any) string {
	header := map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"}
	return signJWT(header, claims, func(d []byte) []byte {
		h := sha256.Sum256(d)
		sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, h[:])
		if err != nil {
			panic(err)
		}
		return sig
	})
}

// RandHex is exported for tests that need state-shaped strings.
func RandHex(n int) string {
	s, err := randHex(n)
	if err != nil {
		panic(err)
	}
	return s
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hexEncode(b), nil
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

func signJWT(header, claims map[string]any, sign func([]byte) []byte) string {
	enc := mapToB64(header) + "." + mapToB64(claims)
	return enc + "." + base64.RawURLEncoding.EncodeToString(sign([]byte(enc)))
}

func mapToB64(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
