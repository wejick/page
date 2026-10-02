package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"page/internal/config"
)

// OIDC is the relying-party side (auth-modes D2, D4, D5): boot-time
// discovery, the /login /auth/callback /logout handlers, the code exchange,
// and ID-token validation against the IdP's JWKS. Stdlib only — no OIDC
// dependency (auth-modes D4).
type OIDC struct {
	issuer       string
	clientID     string
	clientSecret string
	redirectURL  string
	authURL      string
	tokenURL     string
	secret       []byte
	client       *http.Client
	keys         *jwksCache
	log          *slog.Logger
}

// idpDiscovery is the slice of the discovery document the flow needs.
type idpDiscovery struct {
	Issuer   string `json:"issuer"`
	AuthURL  string `json:"authorization_endpoint"`
	TokenURL string `json:"token_endpoint"`
	JWKSURL  string `json:"jwks_uri"`
}

// NewOIDC runs discovery against the configured issuer. Any failure fails
// the boot (auth-modes D4): the same fail-fast as database and storage
// configuration, so a misconfigured IdP never half-boots. log may be nil →
// slog default (observability D1).
func NewOIDC(ctx context.Context, cfg config.OIDC, sessionSecret string, log *slog.Logger) (*OIDC, error) {
	if log == nil {
		log = slog.Default()
	}
	if sessionSecret == "" {
		return nil, errors.New("auth: empty session secret")
	}
	if cfg.Issuer == "" {
		return nil, errors.New("auth: empty OIDC issuer")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	wellKnown := strings.TrimSuffix(cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: discovery request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: discovery fetch %q: %w", wellKnown, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: discovery fetch %q: status %d", wellKnown, resp.StatusCode)
	}
	var d idpDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("auth: discovery document: %w", err)
	}
	// Mix-up defense: a discovery document that names another issuer is not
	// ours (auth-modes D4).
	if d.Issuer != cfg.Issuer {
		return nil, fmt.Errorf("auth: discovery issuer %q does not match configured issuer %q", d.Issuer, cfg.Issuer)
	}
	if d.AuthURL == "" || d.TokenURL == "" || d.JWKSURL == "" {
		return nil, errors.New("auth: discovery document is missing endpoints")
	}
	return &OIDC{
		issuer: cfg.Issuer, clientID: cfg.ClientID, clientSecret: cfg.ClientSecret,
		redirectURL: cfg.RedirectURL, authURL: d.AuthURL, tokenURL: d.TokenURL,
		secret: []byte(sessionSecret), client: client,
		keys: newJWKS(d.JWKSURL, client), log: log,
	}, nil
}

// Login starts the authorization-code flow: a fresh state+nonce pair bound
// to a short-lived HttpOnly cookie, then the redirect to the IdP
// (auth-modes D5).
func (o *OIDC) Login() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, err := randHex(16)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		nonce, err := randHex(16)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: stateCookieName, Value: state + "." + nonce, Path: "/",
			MaxAge: int(stateTTL / time.Second), HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Secure: overHTTPS(r),
		})
		q := url.Values{
			"response_type": {"code"},
			"client_id":     {o.clientID},
			"redirect_uri":  {o.redirectURL},
			"scope":         {"openid email"},
			"state":         {state},
			"nonce":         {nonce},
		}
		http.Redirect(w, r, o.authURL+"?"+q.Encode(), http.StatusFound)
	})
}

// Callback finishes the flow: verify the state against the cookie (single
// use), exchange the code, validate the ID token, mint the session, and go
// to the fixed post-login path (auth-modes D5).
func (o *OIDC) Callback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		clearCookie(w, r, stateCookieName)
		if e := q.Get("error"); e != "" {
			http.Error(w, "login failed: "+e, http.StatusUnauthorized)
			return
		}
		code, state := q.Get("code"), q.Get("state")
		ck, err := r.Cookie(stateCookieName)
		if err != nil || code == "" || state == "" {
			http.Error(w, "missing or invalid login state", http.StatusUnauthorized)
			return
		}
		cookieState, nonce, ok := strings.Cut(ck.Value, ".")
		if !ok || subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
			http.Error(w, "missing or invalid login state", http.StatusUnauthorized)
			return
		}

		raw, err := o.exchange(r.Context(), code)
		if err != nil {
			o.log.Error("auth", "what", "code exchange", "err", err)
			http.Error(w, "login failed", http.StatusUnauthorized)
			return
		}
		claims, err := o.validateIDToken(r.Context(), raw, nonce)
		if err != nil {
			o.log.Error("auth", "what", "id token", "err", err)
			http.Error(w, "login failed", http.StatusUnauthorized)
			return
		}
		o.mintSession(w, r, claims.Sub, claims.Email)
		http.Redirect(w, r, "/", http.StatusFound)
	})
}

// Logout clears the session cookie (auth-modes D5): stateless sessions have
// no server-side state to remove.
func (o *OIDC) Logout() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clearCookie(w, r, sessionCookieName)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("logged out"))
	})
}

// exchange trades the authorization code for the ID token at the token
// endpoint.
func (o *OIDC) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {o.redirectURL},
		"client_id":     {o.clientID},
		"client_secret": {o.clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("auth: token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: token endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth: token endpoint status %d", resp.StatusCode)
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("auth: token response: %w", err)
	}
	if tok.IDToken == "" {
		return "", errors.New("auth: token response carries no id_token")
	}
	return tok.IDToken, nil
}

const clockSkew = 60 * time.Second

// idClaims carries the ID-token claims the flow validates and the session
// stores. aud can legally be a string or an array, hence RawMessage.
type idClaims struct {
	Iss   string          `json:"iss"`
	Sub   string          `json:"sub"`
	Aud   json.RawMessage `json:"aud"`
	Exp   int64           `json:"exp"`
	NBF   int64           `json:"nbf"`
	Nonce string          `json:"nonce"`
	Email string          `json:"email"`
}

// validateIDToken checks the JWT end to end: RS256/ES256 signature against
// the JWKS, iss, aud, exp/nbf with clock skew, and the flow's nonce
// (auth-modes D4). Anything else is a failed login.
func (o *OIDC) validateIDToken(ctx context.Context, raw, nonce string) (idClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return idClaims{}, errors.New("auth: not a JWT")
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return idClaims{}, fmt.Errorf("auth: jwt header: %w", err)
	}
	if err := json.Unmarshal(hb, &h); err != nil {
		return idClaims{}, fmt.Errorf("auth: jwt header: %w", err)
	}
	if h.Alg != "RS256" && h.Alg != "ES256" {
		return idClaims{}, fmt.Errorf("auth: unsupported signing alg %q", h.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return idClaims{}, fmt.Errorf("auth: jwt signature: %w", err)
	}
	key, err := o.keys.key(ctx, h.Kid)
	if err != nil {
		return idClaims{}, err
	}
	signing := parts[0] + "." + parts[1]
	digest := sha256Sum(signing)
	switch k := key.(type) {
	case *rsaPublicKey:
		if h.Alg != "RS256" {
			return idClaims{}, fmt.Errorf("auth: alg %q over an RSA key", h.Alg)
		}
		if err := verifyRS256(k, digest[:], sig); err != nil {
			return idClaims{}, err
		}
	case *ecdsaPublicKey:
		if h.Alg != "ES256" {
			return idClaims{}, fmt.Errorf("auth: alg %q over an EC key", h.Alg)
		}
		if err := verifyES256(k, digest[:], sig); err != nil {
			return idClaims{}, err
		}
	default:
		return idClaims{}, errors.New("auth: unsupported key type")
	}

	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idClaims{}, fmt.Errorf("auth: jwt claims: %w", err)
	}
	var c idClaims
	if err := json.Unmarshal(cb, &c); err != nil {
		return idClaims{}, fmt.Errorf("auth: jwt claims: %w", err)
	}
	now := time.Now()
	switch {
	case c.Iss != o.issuer:
		return idClaims{}, fmt.Errorf("auth: iss %q, want %q", c.Iss, o.issuer)
	case !audContains(c.Aud, o.clientID):
		return idClaims{}, errors.New("auth: aud does not include the client id")
	case c.Sub == "":
		return idClaims{}, errors.New("auth: empty sub")
	case c.Exp < now.Add(-clockSkew).Unix():
		return idClaims{}, errors.New("auth: token expired")
	case c.NBF != 0 && c.NBF > now.Add(clockSkew).Unix():
		return idClaims{}, errors.New("auth: token not yet valid")
	case c.Nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1:
		return idClaims{}, errors.New("auth: nonce mismatch")
	}
	return c, nil
}

// audContains handles both legal aud shapes.
func audContains(raw json.RawMessage, clientID string) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == clientID
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		for _, a := range arr {
			if a == clientID {
				return true
			}
		}
	}
	return false
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
