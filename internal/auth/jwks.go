package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// The verifier works over concrete stdlib key types, but rsa/ecdsa are
// aliased here so oidc.go reads as one uniform switch.
type (
	rsaPublicKey   = rsa.PublicKey
	ecdsaPublicKey = ecdsa.PublicKey
)

func sha256Sum(s string) []byte {
	d := sha256.Sum256([]byte(s))
	return d[:]
}

func verifyRS256(key *rsaPublicKey, digest, sig []byte) error {
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest, sig); err != nil {
		return fmt.Errorf("auth: signature: %w", err)
	}
	return nil
}

func verifyES256(key *ecdsaPublicKey, digest, sig []byte) error {
	if !ecdsa.VerifyASN1(key, digest, sig) {
		return fmt.Errorf("auth: signature: invalid")
	}
	return nil
}

// The JWKS cache (auth-modes D4): keys are held by kid and re-fetched when
// the staleness bound passes; an unknown kid always triggers one immediate
// re-fetch, so IdP rotation never breaks a login. Refetch-on-unknown-kid is
// safe to leave unthrottled: token validation only runs after a valid state
// check, so junk kids cannot reach the cache without a full login dance.
const jwksMaxAge = 10 * time.Minute

type jwksCache struct {
	url    string
	client *http.Client

	mu         sync.Mutex
	keys       map[string]any // *rsa.PublicKey or *ecdsa.PublicKey
	fetchedAt  time.Time
	hasFetched bool
}

func newJWKS(url string, client *http.Client) *jwksCache {
	return &jwksCache{url: url, client: client, keys: map[string]any{}}
}

type jwkSet struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// key returns the signing key for kid, fetching the JWKS on first use and
// re-fetching on staleness or an unknown kid.
func (j *jwksCache) key(ctx context.Context, kid string) (any, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.hasFetched || time.Since(j.fetchedAt) > jwksMaxAge {
		if err := j.fetch(ctx); err != nil {
			return nil, err
		}
	}
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	// Unknown kid: the IdP may have rotated since the last fetch.
	if err := j.fetch(ctx); err != nil {
		return nil, err
	}
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("auth: unknown signing key %q", kid)
}

func (j *jwksCache) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return fmt.Errorf("auth: jwks request: %w", err)
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return fmt.Errorf("auth: jwks fetch %q: %w", j.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: jwks fetch %q: status %d", j.url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("auth: jwks read: %w", err)
	}
	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return fmt.Errorf("auth: jwks document: %w", err)
	}
	keys := make(map[string]any, len(set.Keys))
	for _, k := range set.Keys {
		pub, err := k.publicKey()
		if err != nil {
			return fmt.Errorf("auth: jwks key %q: %w", k.Kid, err)
		}
		if pub != nil {
			keys[k.Kid] = pub
		}
	}
	j.keys = keys
	j.fetchedAt = time.Now()
	j.hasFetched = true
	return nil
}

// publicKey converts one JWK. Unsupported key types are skipped (nil, nil)
// rather than poisoning the whole set.
func (k jwk) publicKey() (any, error) {
	switch {
	case k.Kty == "RSA" && k.Alg == "RS256":
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("modulus: %w", err)
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("exponent: %w", err)
		}
		e := new(big.Int).SetBytes(eb)
		if !e.IsInt64() || e.Int64() <= 0 || e.Int64() > int64(^uint32(0)) {
			return nil, fmt.Errorf("exponent out of range")
		}
		return &rsaPublicKey{N: new(big.Int).SetBytes(nb), E: int(e.Int64())}, nil
	case k.Kty == "EC" && k.Crv == "P-256" && k.Alg == "ES256":
		xb, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("x: %w", err)
		}
		yb, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("y: %w", err)
		}
		return &ecdsaPublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(xb),
			Y:     new(big.Int).SetBytes(yb),
		}, nil
	default:
		return nil, nil
	}
}
