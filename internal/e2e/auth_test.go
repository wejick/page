//go:build integration

package e2e

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"page/internal/auth"
	"page/internal/auth/oidctest"
	"page/internal/config"
	"page/internal/ingest"
	"page/internal/lifecycle"
	"page/internal/serve"
	"page/internal/upload"
)

const (
	testSessionSecret = "e2e-session-secret"
	machineToken      = "machine-token"
)

func testCaps() config.Caps {
	return config.Caps{
		MaxRawBytes: 25 << 20, MaxDecompressedBytes: 100 << 20,
		MaxFiles: 2000, MaxAssetBytes: 10 << 20,
		FetchTimeout: time.Second, FetchBudget: 5 * time.Second, FetchConcurrency: 4,
	}
}

func testKeep() ingest.KeepRules {
	return ingest.KeepRules{
		Fonts: []string{"fonts.googleapis.com", "fonts.gstatic.com"},
		JS:    []string{"cdn.jsdelivr.net"},
	}
}

// oidcStack boots the full service (real Postgres + MinIO) in oidc mode
// against the fake IdP. The app's listener is reserved before the flow is
// constructed, so the configured redirect URL is the real callback address
// and the browser-style client needs no host rewriting.
func oidcStack(t *testing.T, ctx context.Context) string {
	t.Helper()
	pg := startPostgres(t, ctx)
	store, _ := startMinio(t, ctx)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	base := "http://" + lis.Addr().String()
	callback := base + "/auth/callback"

	idp := oidctest.New(t)
	idp.RedirectURL = callback
	flow, err := auth.NewOIDC(ctx, config.OIDC{
		Issuer: idp.Issuer, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
		RedirectURL: callback,
	}, testSessionSecret)
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	checker := auth.NewChecker(config.AuthModeOIDC, machineToken, flow)
	api := upload.New(upload.Options{
		Pool: pg.Pool, Store: store,
		Caps: testCaps(), Keep: testKeep(),
		Auth: checker,
	})
	lc := lifecycle.New(pg.Pool, store)
	handler := serve.New(serve.Options{
		Store: store, Upload: api,
		Lifecycle: lifecycle.NewAPI(lc, checker), Auth: checker, Ping: pg.Pool.Ping,
	})
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { _ = srv.Close() })
	return base
}

// sessionClient returns a cookie-jar client that follows the whole
// /login → IdP → /auth/callback chain automatically.
func sessionClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("jar: %v", err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func machineClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

func do(t *testing.T, client *http.Client, method, path string, headers map[string]string, body io.Reader) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func bodySlug(t *testing.T, body string) string {
	t.Helper()
	const key = `"slug":"`
	i := strings.Index(body, key)
	if i < 0 {
		t.Fatalf("no slug in %s", body)
	}
	rest := body[i+len(key):]
	if j := strings.Index(rest, `"`); j >= 0 {
		return rest[:j]
	}
	t.Fatalf("unterminated slug in %s", body)
	return ""
}

// TestOIDCAuthEndToEnd walks the SSO journey over the real stack: the
// redirect to /login, the full dance through the fake IdP, session-API
// access, the CSRF rule, the machine token path, and logout (auth-modes
// D2/D5/D6, admin-auth spec scenarios).
func TestOIDCAuthEndToEnd(t *testing.T) {
	ctx := context.Background()
	base := oidcStack(t, ctx)

	// Unauthenticated: browser routes redirect, API answers 401.
	status, header, _ := do(t, noRedirectClient(), "GET", base+"/", nil, nil)
	if status != http.StatusFound || header.Get("Location") != "/login" {
		t.Fatalf("unauthenticated shell = %d %q, want 302 /login", status, header.Get("Location"))
	}
	status, _, _ = do(t, noRedirectClient(), "GET", base+"/api/pages", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API = %d, want 401", status)
	}

	// The full dance: /login → IdP authorize → callback → landing.
	browser := sessionClient(t)
	status, _, body := do(t, browser, "GET", base+"/login", nil, nil)
	if status != http.StatusOK || !strings.Contains(body, "Static pages") {
		t.Fatalf("post-login landing = %d %q, want the UI shell", status, body)
	}
	// The shell advertises oidc mode (auth-modes D8).
	status, _, body = do(t, browser, "GET", base+"/", nil, nil)
	if status != 200 || !strings.Contains(body, `data-auth-mode="oidc"`) {
		t.Fatalf("shell = %d %q, want 200 advertising oidc mode", status, body)
	}
	// Session-authenticated API access.
	status, _, body = do(t, browser, "GET", base+"/api/pages", nil, nil)
	if status != 200 || !strings.Contains(body, `"total"`) {
		t.Fatalf("session list = %d %q, want 200", status, body)
	}

	// Machine path uploads a page to act on (bearer is CSRF-exempt).
	mb, contentType := uploadBody(t, seedPack, "sso")
	status, _, body = do(t, machineClient(), "POST", base+"/api/pages", map[string]string{
		"Content-Type": contentType, "Authorization": "Bearer " + machineToken,
	}, mb)
	if status != http.StatusCreated {
		t.Fatalf("machine upload = %d %s", status, body)
	}
	slug := bodySlug(t, body)

	// CSRF: a session-authenticated state change without the header is 403.
	status, _, _ = do(t, browser, "POST", base+"/api/pages/"+slug+"/park", nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("park without CSRF header = %d, want 403", status)
	}
	// With the header — exactly what the UI sends — it succeeds.
	status, _, body = do(t, browser, "POST", base+"/api/pages/"+slug+"/park",
		map[string]string{auth.CSRFHeader: auth.CSRFValue}, nil)
	if status != 200 || !strings.Contains(body, `"parked"`) {
		t.Fatalf("park with CSRF header = %d %s", status, body)
	}

	// Logout clears the session; the next API call is 401 again.
	status, _, _ = do(t, browser, "POST", base+"/logout", nil, nil)
	if status != 200 {
		t.Fatalf("logout = %d", status)
	}
	status, _, _ = do(t, browser, "GET", base+"/api/pages", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("post-logout list = %d, want 401", status)
	}
}

// TestNoneModeEndToEnd proves the network-trust deployment: no credentials
// anywhere, the API and the UI simply work (auth-modes D1).
func TestNoneModeEndToEnd(t *testing.T) {
	ctx := context.Background()
	pg := startPostgres(t, ctx)
	store, _ := startMinio(t, ctx)
	checker := auth.NewChecker(config.AuthModeNone, "", nil)
	api := upload.New(upload.Options{
		Pool: pg.Pool, Store: store, Caps: testCaps(), Keep: testKeep(), Auth: checker,
	})
	lc := lifecycle.New(pg.Pool, store)
	ts := newTestServer(t, serve.New(serve.Options{
		Store: store, Upload: api,
		Lifecycle: lifecycle.NewAPI(lc, checker), Auth: checker, Ping: pg.Pool.Ping,
	}))
	defer ts.Close()

	status, _, body := do(t, noRedirectClient(), "GET", ts.URL+"/", nil, nil)
	if status != 200 || !strings.Contains(body, `data-auth-mode="none"`) {
		t.Fatalf("shell = %d %q, want 200 advertising none mode", status, body)
	}
	status, _, _ = do(t, noRedirectClient(), "GET", ts.URL+"/api/pages", nil, nil)
	if status != 200 {
		t.Fatalf("unauthenticated list = %d, want 200", status)
	}
	mb, contentType := uploadBody(t, seedPack, "open")
	status, _, _ = do(t, noRedirectClient(), "POST", ts.URL+"/api/pages",
		map[string]string{"Content-Type": contentType}, mb)
	if status != http.StatusCreated {
		t.Fatalf("unauthenticated upload = %d, want 201", status)
	}
}

// newTestServer wraps httptest.NewServer without inventing a name.
func newTestServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

// buildServer compiles the real cmd/server binary once for boot tests.
func buildServer(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "server")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/server")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/server: %v\n%s", err, out)
	}
	return bin
}

// bootEnv composes a full admin-mode environment for a cmd/server
// subprocess, stripping inherited auth variables first.
func bootEnv(dsn, endpoint string, extra ...string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "DATABASE_URL="), strings.HasPrefix(kv, "AUTH_TOKEN="),
			strings.HasPrefix(kv, "AUTH_MODE="), strings.HasPrefix(kv, "SESSION_SECRET="),
			strings.HasPrefix(kv, "OIDC_"):
			continue
		}
		env = append(env, kv)
	}
	return append(env, append([]string{
		"SERVER_MODE=admin",
		"STORAGE_DRIVER=s3compat",
		"S3_ENDPOINT=" + endpoint,
		"S3_BUCKET=pages",
		"S3_ACCESS_KEY=minioadmin",
		"S3_SECRET_KEY=minioadmin",
		"S3_PATH_STYLE=true",
		"DATABASE_URL=" + dsn,
	}, extra...)...)
}

// waitHealthy polls /healthz until it answers 200 or the deadline passes.
func waitHealthy(t *testing.T, base string) bool {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get(base + "/healthz")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// startServer runs the binary in the background and returns its base URL.
func startServer(t *testing.T, bin string, env []string) (string, *exec.Cmd, *bytes.Buffer) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	lis.Close()
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	output := &bytes.Buffer{}
	cmd := exec.Command(bin)
	cmd.Env = append(env, "ADDR="+strings.TrimPrefix(base, "http://"))
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return base, cmd, output
}

// TestOIDCBootJourney boots the real binary in oidc mode: unreachable and
// mismatched issuers fail startup (auth-modes D4); a live fake IdP boots and
// serves the login flow.
func TestOIDCBootJourney(t *testing.T) {
	ctx := context.Background()
	pg := startPostgres(t, ctx)
	_, endpoint := startMinio(t, ctx)
	dsn, err := pg.Container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	bin := buildServer(t, ctx)
	oidcVars := []string{
		"AUTH_MODE=oidc",
		"OIDC_CLIENT_ID=page",
		"OIDC_CLIENT_SECRET=s3cret",
		"OIDC_REDIRECT_URL=http://127.0.0.1:8080/auth/callback",
		"SESSION_SECRET=" + testSessionSecret,
		"AUTH_TOKEN=" + machineToken,
	}

	t.Run("unreachable issuer fails the boot", func(t *testing.T) {
		cmd := exec.Command(bin)
		cmd.Env = bootEnv(dsn, endpoint, append(oidcVars, "OIDC_ISSUER=http://127.0.0.1:1")...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("boot succeeded, want discovery failure; output:\n%s", out)
		}
		if !strings.Contains(string(out), "discovery") {
			t.Fatalf("boot failure does not mention discovery; output:\n%s", out)
		}
	})

	t.Run("mismatched issuer fails the boot", func(t *testing.T) {
		doc := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"issuer":"https://someone-else","authorization_endpoint":"a","token_endpoint":"t","jwks_uri":"j"}`))
		}))
		cmd := exec.Command(bin)
		cmd.Env = bootEnv(dsn, endpoint, append(oidcVars, "OIDC_ISSUER="+doc.URL)...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("boot succeeded, want issuer mismatch; output:\n%s", out)
		}
		if !strings.Contains(string(out), "does not match") {
			t.Fatalf("boot failure does not mention the issuer mismatch; output:\n%s", out)
		}
	})

	t.Run("live IdP boots and serves the login flow", func(t *testing.T) {
		idp := oidctest.New(t)
		base, _, output := startServer(t, bin, bootEnv(dsn, endpoint,
			append(oidcVars, "OIDC_ISSUER="+idp.Issuer)...))
		if !waitHealthy(t, base) {
			t.Fatalf("oidc instance never became healthy; output:\n%s", output.String())
		}
		client := noRedirectClient()
		r, err := client.Get(base + "/")
		if err != nil {
			t.Fatalf("GET /: %v", err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "/login" {
			t.Fatalf("GET / = %d %q, want 302 /login", r.StatusCode, r.Header.Get("Location"))
		}
		r, err = client.Get(base + "/login")
		if err != nil {
			t.Fatalf("GET /login: %v", err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusFound || !strings.Contains(r.Header.Get("Location"), "/authorize") {
			t.Fatalf("GET /login = %d %q, want a redirect to the IdP", r.StatusCode, r.Header.Get("Location"))
		}
	})
}

// TestNoneModeBootWarns proves a none-mode instance boots without any
// credentials and says so loudly (auth-modes D1, none mode accept rule).
func TestNoneModeBootWarns(t *testing.T) {
	ctx := context.Background()
	pg := startPostgres(t, ctx)
	_, endpoint := startMinio(t, ctx)
	dsn, err := pg.Container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	bin := buildServer(t, ctx)
	base, _, output := startServer(t, bin, bootEnv(dsn, endpoint, "AUTH_MODE=none"))
	if !waitHealthy(t, base) {
		t.Fatalf("none instance never became healthy; output:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "unauthenticated") {
		t.Fatalf("boot output lacks the unauthenticated warning; output:\n%s", output.String())
	}
	// And without credentials the API answers.
	status, _, _ := do(t, noRedirectClient(), "GET", base+"/api/pages", nil, nil)
	if status != 200 {
		t.Fatalf("unauthenticated list = %d, want 200", status)
	}
}

// TestSeedWithoutToken runs the real seed command in none mode: no
// Authorization header is sent, and the upload still lands (auth-modes D9).
func TestSeedWithoutToken(t *testing.T) {
	ctx := context.Background()
	pg := startPostgres(t, ctx)
	dsn, err := pg.Container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "seed")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/seed")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/seed: %v\n%s", err, out)
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "DATABASE_URL=") || strings.HasPrefix(kv, "AUTH_TOKEN=") ||
			strings.HasPrefix(kv, "AUTH_MODE=") {
			continue
		}
		env = append(env, kv)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(env, "DATABASE_URL="+dsn, "STORAGE_DRIVER=mem", "AUTH_MODE=none")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "HTTP 201") {
		t.Fatalf("seed = err %v, output:\n%s", err, out)
	}
}
