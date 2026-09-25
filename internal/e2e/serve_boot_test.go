//go:build integration

package e2e

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"page/internal/config"
	"page/internal/ingest"
	"page/internal/lifecycle"
	"page/internal/serve"
	"page/internal/upload"
)

// TestServeModeBootsWithoutDatabase proves serve mode boots and serves with
// no database configuration at all (deployment-modes D2, D4): a real
// cmd/server subprocess serves a page seeded over the admin path, with the
// Postgres container terminated and DATABASE_URL/AUTH_TOKEN stripped from
// its environment before the serve instance starts.
func TestServeModeBootsWithoutDatabase(t *testing.T) {
	ctx := context.Background()

	// Real Postgres for the admin-plane seeding step only.
	pg := startPostgres(t, ctx)
	pool := pg.Pool

	// Real MinIO: the shared bucket both instances see.
	store, endpoint := startMinio(t, ctx)

	// --- Seed through the admin/all path, exactly as cmd/server wires it.
	api := upload.New(upload.Options{
		Pool: pool, Store: store,
		Caps: config.Caps{
			MaxRawBytes: 25 << 20, MaxDecompressedBytes: 100 << 20,
			MaxFiles: 2000, MaxAssetBytes: 10 << 20,
			FetchTimeout: time.Second, FetchBudget: 5 * time.Second, FetchConcurrency: 4,
		},
		Keep: ingest.KeepRules{
			Fonts: []string{"fonts.googleapis.com", "fonts.gstatic.com"},
			JS:    []string{"cdn.jsdelivr.net"},
		},
		Token: "secret",
	})
	lc := lifecycle.New(pool, store)
	ts := httptest.NewServer(serve.New(serve.Options{
		Mode: config.ModeAll, Store: store, CacheTTL: time.Second,
		Upload: api, Lifecycle: lifecycle.NewAPI(lc, "secret"), Ping: pool.Ping,
	}))

	mb, contentType := uploadBody(t, seedPack, "serveboot")
	req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/api/pages", mb)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	ts.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d body=%s", resp.StatusCode, body)
	}
	const slug = "serveboot-1"
	if !strings.Contains(string(body), `"slug":"`+slug+`"`) {
		t.Fatalf("slug missing: %s", body)
	}

	// --- The admin plane is done: retire Postgres entirely. From here on
	// any database touch, by boot or by health, must fail the test.
	pool.Close()
	if err := pg.Container.Terminate(ctx); err != nil {
		t.Fatalf("terminate postgres: %v", err)
	}

	// --- Boot the real cmd/server binary in serve mode.
	bin := filepath.Join(t.TempDir(), "server")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/server")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/server: %v\n%s", err, out)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	lis.Close()
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	// Strip DATABASE_URL/AUTH_TOKEN from the inherited environment: serve
	// mode must have no database configuration, not merely ignore it.
	var serveEnv []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "DATABASE_URL=") || strings.HasPrefix(kv, "AUTH_TOKEN=") {
			continue
		}
		serveEnv = append(serveEnv, kv)
	}
	serveEnv = append(serveEnv,
		"ADDR="+strings.TrimPrefix(base, "http://"),
		"SERVER_MODE=serve",
		"STORAGE_DRIVER=s3compat",
		"S3_ENDPOINT="+endpoint,
		"S3_BUCKET=pages",
		"S3_ACCESS_KEY=minioadmin",
		"S3_SECRET_KEY=minioadmin",
		"S3_PATH_STYLE=true",
	)

	var output bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Env = serveEnv
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve instance: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Startup succeeding at all is the first assertion: a boot-time pool
	// open would crash against the terminated Postgres.
	deadline := time.Now().Add(30 * time.Second)
	healthy := false
	for !healthy && time.Now().Before(deadline) {
		r, err := http.Get(base + "/healthz")
		if err == nil {
			r.Body.Close()
			healthy = r.StatusCode == http.StatusOK
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("serve instance never became healthy; output:\n%s", output.String())
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	// The seeded page serves, rewritten to the /a/ asset mount.
	gres, gbody := get("/p/" + slug + "/")
	if gres.StatusCode != http.StatusOK {
		t.Fatalf("serve page status = %d, output:\n%s", gres.StatusCode, output.String())
	}
	for _, want := range []string{
		`href="/a/` + slug + `/style.css"`,
		`src="/a/` + slug + `/assets/hero.png"`,
	} {
		if !strings.Contains(gbody, want) {
			t.Errorf("serve page missing %q", want)
		}
	}

	// Slashless redirect and both asset mounts, identical bytes.
	if r, _ := get("/p/" + slug); r.StatusCode != http.StatusMovedPermanently ||
		r.Header.Get("Location") != "/p/"+slug+"/" {
		t.Errorf("slashless redirect wrong")
	}
	for _, p := range []string{"/a/" + slug + "/assets/hero.png", "/p/" + slug + "/assets/hero.png"} {
		r, b := get(p)
		if r.StatusCode != http.StatusOK || string(b) != "HERO" {
			t.Errorf("%s = %d %q", p, r.StatusCode, b)
		}
	}
	if r, css := get("/a/" + slug + "/style.css"); r.StatusCode != http.StatusOK ||
		!strings.Contains(css, "url(/a/"+slug+"/assets/bg.png)") {
		t.Errorf("css rewrite = %d %q", r.StatusCode, css)
	}

	// The serve plane mounts nothing else: no upload UI, no admin API.
	for _, p := range []string{"/", "/api/pages/" + slug} {
		if r, _ := get(p); r.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, r.StatusCode)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, _ := http.NewRequest(method, base+"/api/pages", nil)
		r, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s /api/pages: %v", method, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s /api/pages = %d, want 404", method, r.StatusCode)
		}
	}

	// Health is storage-only: still 200 with Postgres terminated.
	r, _ := get("/healthz")
	if r.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d, want 200", r.StatusCode)
	}

	// Shut down the way production does: SIGTERM must exit cleanly through
	// the signal-handling path.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sigterm: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("serve instance exit = %v, want clean shutdown; output:\n%s", err, output.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("serve instance did not exit after SIGTERM; output:\n%s", output.String())
	}
}
