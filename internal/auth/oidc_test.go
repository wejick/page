package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"page/internal/auth/oidctest"
	"page/internal/config"
)

func TestNewOIDCDiscovery(t *testing.T) {
	idp := oidctest.New(t)

	t.Run("success", func(t *testing.T) {
		o, err := NewOIDC(t.Context(), config.OIDC{Issuer: idp.Issuer, ClientID: "c",
			ClientSecret: "s", RedirectURL: "https://app.test/cb"}, "secret")
		if err != nil {
			t.Fatalf("NewOIDC: %v", err)
		}
		if o.authURL != idp.Issuer+"/authorize" || o.tokenURL != idp.Issuer+"/token" {
			t.Fatalf("endpoints = %q %q", o.authURL, o.tokenURL)
		}
	})

	t.Run("issuer mismatch fails the boot", func(t *testing.T) {
		doc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"issuer":"https://someone-else","authorization_endpoint":"a","token_endpoint":"t","jwks_uri":"j"}`))
		}))
		defer doc.Close()
		_, err := NewOIDC(t.Context(), config.OIDC{Issuer: doc.URL, ClientID: "c",
			ClientSecret: "s", RedirectURL: "u"}, "secret")
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("NewOIDC err = %v, want issuer mismatch", err)
		}
	})

	t.Run("unreachable issuer fails the boot", func(t *testing.T) {
		_, err := NewOIDC(t.Context(), config.OIDC{Issuer: "http://127.0.0.1:1", ClientID: "c",
			ClientSecret: "s", RedirectURL: "u"}, "secret")
		if err == nil {
			t.Fatalf("NewOIDC err = nil, want fetch failure")
		}
	})

	t.Run("empty session secret fails the boot", func(t *testing.T) {
		_, err := NewOIDC(t.Context(), config.OIDC{Issuer: idp.Issuer}, "")
		if err == nil {
			t.Fatalf("NewOIDC err = nil, want empty-secret failure")
		}
	})
}

// appWithFlow mounts the real login/callback handlers with the fake IdP
// bouncing back to them, plus a landing page that reports the session.
func appWithFlow(t *testing.T, o *OIDC) (*httptest.Server, *string) {
	t.Helper()
	landing := "not visited"
	mux := http.NewServeMux()
	mux.Handle("GET /login", o.Login())
	mux.Handle("GET /auth/callback", o.Callback())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		s, ok := o.sessionFrom(r)
		if !ok {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		landing = s.Sub
		_, _ = w.Write([]byte("hello " + s.Sub))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &landing
}

func jarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("jar: %v", err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func TestLoginFlow(t *testing.T) {
	idp := oidctest.New(t)
	o, err := NewOIDC(t.Context(), config.OIDC{Issuer: idp.Issuer, ClientID: idp.ClientID,
		ClientSecret: idp.ClientSecret, RedirectURL: "https://app.test/auth/callback"}, "secret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	idp.RedirectURL = "https://app.test/auth/callback" // the fake IdP's bounce target

	login := httptest.NewServer(o.Login())
	defer login.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	t.Run("authorization request carries the OIDC contract", func(t *testing.T) {
		resp, err := noFollow.Get(login.URL)
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("login = %d", resp.StatusCode)
		}
		dest, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatalf("location: %v", err)
		}
		q := dest.Query()
		if q.Get("response_type") != "code" || q.Get("scope") != "openid email" ||
			q.Get("client_id") != idp.ClientID || q.Get("state") == "" || q.Get("nonce") == "" ||
			q.Get("redirect_uri") != "https://app.test/auth/callback" {
			t.Fatalf("authorization request = %v", q)
		}
		sc := resp.Header.Get("Set-Cookie")
		if !strings.Contains(sc, stateCookieName+"=") || !strings.Contains(sc, "HttpOnly") {
			t.Fatalf("state cookie = %q", sc)
		}
	})

	t.Run("full dance establishes a session", func(t *testing.T) {
		app, landing := appWithFlow(t, o)
		// Point both ends of the flow at the real test servers.
		o.redirectURL = app.URL + "/auth/callback"
		idp.RedirectURL = o.redirectURL
		resp, err := jarClient(t).Get(app.URL + "/login") // follows authorize → callback → /
		if err != nil {
			t.Fatalf("dance: %v", err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 128)
		n, _ := resp.Body.Read(buf)
		if !strings.Contains(string(buf[:n]), "hello user-1") {
			t.Fatalf("landing = %q, want authenticated greeting", string(buf[:n]))
		}
		if *landing != "user-1" {
			t.Fatalf("landing saw sub %q", *landing)
		}
	})

	t.Run("state mismatch is rejected and the cookie cleared", func(t *testing.T) {
		resp, err := noFollow.Get(login.URL)
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		resp.Body.Close()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/auth/callback?code=x&state=WRONG", nil)
		r.Header.Set("Cookie", resp.Header.Get("Set-Cookie"))
		o.Callback().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("mismatched state = %d, want 401", w.Code)
		}
		if sc := w.Header().Get("Set-Cookie"); !strings.Contains(sc, stateCookieName+"=;") {
			t.Fatalf("state cookie not cleared: %q", sc)
		}
	})

	t.Run("callback without the state cookie is rejected", func(t *testing.T) {
		w := httptest.NewRecorder()
		o.Callback().ServeHTTP(w, httptest.NewRequest("GET", "/auth/callback?code=x&state=y", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("cookieless callback = %d, want 401", w.Code)
		}
	})

	t.Run("IdP error surfaces as a failed login", func(t *testing.T) {
		w := httptest.NewRecorder()
		o.Callback().ServeHTTP(w, httptest.NewRequest("GET", "/auth/callback?error=access_denied", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("error callback = %d, want 401", w.Code)
		}
	})
}

func TestLogoutClearsSession(t *testing.T) {
	o := testOIDC(t)
	w := httptest.NewRecorder()
	o.Logout().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/logout", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("logout = %d", w.Code)
	}
	sc := w.Header().Get("Set-Cookie")
	if !strings.Contains(sc, sessionCookieName+"=;") || !strings.Contains(sc, "Max-Age=0") {
		t.Fatalf("logout cookie = %q, want an expired session cookie", sc)
	}
}

// TestValidateIDToken drives claim validation directly with crafted tokens
// signed by the fake IdP's real key.
func TestValidateIDToken(t *testing.T) {
	idp := oidctest.New(t)
	o, err := NewOIDC(t.Context(), config.OIDC{Issuer: idp.Issuer, ClientID: idp.ClientID,
		ClientSecret: idp.ClientSecret, RedirectURL: "u"}, "secret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	now := time.Now()
	good := func(mut func(map[string]any)) string {
		claims := map[string]any{
			"iss": idp.Issuer, "sub": "user-1", "aud": idp.ClientID,
			"exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": "n-123",
		}
		if mut != nil {
			mut(claims)
		}
		return idp.SignIDToken(claims)
	}

	if _, err := o.validateIDToken(t.Context(), good(nil), "n-123"); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}

	rejections := []struct {
		name  string
		token string
		nonce string
	}{
		{"expired", good(func(c map[string]any) { c["exp"] = now.Add(-2 * time.Minute).Unix() }), "n-123"},
		{"wrong audience", good(func(c map[string]any) { c["aud"] = "someone-else" }), "n-123"},
		{"wrong issuer", good(func(c map[string]any) { c["iss"] = "https://other" }), "n-123"},
		{"empty sub", good(func(c map[string]any) { c["sub"] = "" }), "n-123"},
		{"wrong nonce", good(nil), "n-999"},
		{"empty nonce", good(func(c map[string]any) { delete(c, "nonce") }), "n-123"},
		{"not yet valid", good(func(c map[string]any) { c["nbf"] = now.Add(time.Hour).Unix() }), "n-123"},
		{"HS256 rejected", hs256Token(idp.Issuer, idp.ClientID, now), "n-123"},
		{"garbage", "not.a.jwt", "n-123"},
	}
	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := o.validateIDToken(t.Context(), tt.token, tt.nonce); err == nil {
				t.Fatalf("token accepted, want rejection")
			}
		})
	}

	t.Run("aud as array is accepted", func(t *testing.T) {
		tok := good(func(c map[string]any) { c["aud"] = []string{idp.ClientID, "other"} })
		if _, err := o.validateIDToken(t.Context(), tok, "n-123"); err != nil {
			t.Fatalf("array aud rejected: %v", err)
		}
	})

	t.Run("token signed by a foreign key is rejected", func(t *testing.T) {
		other := oidctest.New(t) // honest claims, different RSA key
		_, err := o.validateIDToken(t.Context(), other.SignIDToken(map[string]any{
			"iss": idp.Issuer, "sub": "user-1", "aud": idp.ClientID,
			"exp": now.Add(time.Minute).Unix(), "nonce": "n-123",
		}), "n-123")
		if err == nil {
			t.Fatalf("foreign-signed token accepted")
		}
	})
}

func hs256Token(issuer, clientID string, now time.Time) string {
	claims := map[string]any{"iss": issuer, "sub": "x", "aud": clientID,
		"exp": now.Add(time.Minute).Unix(), "nonce": "n-123"}
	enc := mapToB64(map[string]any{"alg": "HS256", "kid": "test-key"}) + "." + mapToB64(claims)
	mac := hmac.New(sha256.New, []byte("attacker-key"))
	mac.Write([]byte(enc))
	return enc + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// TestJWKSRotation proves an unknown kid triggers a refetch (rotation
// convergence) and that a bogus kid inside the throttle window errors
// without hammering the IdP.
func TestJWKSRotation(t *testing.T) {
	oldKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	newKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rotated := false
	fetches := 0
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		key, kid := oldKey, "signing-1"
		if rotated {
			key, kid = newKey, "signing-2"
		}
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jwk{ecJWK(kid, &key.PublicKey)}})
	}))
	defer jwksSrv.Close()
	o := &OIDC{
		issuer: "https://idp.test", clientID: "c", secret: []byte("s"),
		keys: newJWKS(jwksSrv.URL, &http.Client{Timeout: 5 * time.Second}),
	}

	now := time.Now()
	signES := func(key *ecdsa.PrivateKey, kid string) string {
		claims := map[string]any{"iss": "https://idp.test", "sub": "u", "aud": "c",
			"exp": now.Add(time.Minute).Unix(), "nonce": "n"}
		enc := mapToB64(map[string]any{"alg": "ES256", "kid": kid}) + "." + mapToB64(claims)
		h := sha256.Sum256([]byte(enc))
		sig, err := ecdsa.SignASN1(rand.Reader, key, h[:])
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return enc + "." + base64.RawURLEncoding.EncodeToString(sig)
	}

	// The first key verifies after the initial fetch.
	if _, err := o.validateIDToken(t.Context(), signES(oldKey, "signing-1"), "n"); err != nil {
		t.Fatalf("old-key token: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1", fetches)
	}

	// Rotate. The rotated key's unknown kid triggers exactly one refetch
	// and then verifies — rotation never breaks a login.
	rotated = true
	if _, err := o.validateIDToken(t.Context(), signES(newKey, "signing-2"), "n"); err != nil {
		t.Fatalf("rotated-key token: %v", err)
	}
	if fetches != 2 {
		t.Fatalf("fetches = %d after rotation refetch, want 2", fetches)
	}
	// A kid the rotated JWKS does not carry errors after the refetch.
	if _, err := o.keys.key(t.Context(), "brand-new"); err == nil {
		t.Fatalf("bogus kid accepted")
	}
	if fetches != 3 {
		t.Fatalf("fetches = %d after bogus-kid refetch, want 3", fetches)
	}
}

// ecJWK builds a P-256 JWK from a public key.
func ecJWK(kid string, pub *ecdsa.PublicKey) jwk {
	return jwk{
		Kty: "EC", Crv: "P-256", Alg: "ES256", Kid: kid,
		X: base64.RawURLEncoding.EncodeToString(pub.X.Bytes()),
		Y: base64.RawURLEncoding.EncodeToString(pub.Y.Bytes()),
	}
}

// mapToB64 marshals and base64url-encodes a JSON object (JWT segments).
func mapToB64(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
