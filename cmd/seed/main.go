// Command seed uploads a sample Framer-style pack through the real upload
// API (in-process) so a locally running stack has a page to serve.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"page/internal/config"
	"page/internal/db"
	"page/internal/ingest"
	"page/internal/serve"
	"page/internal/storage"
	"page/internal/storage/mem"
	"page/internal/storage/s3compat"
	"page/internal/upload"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadEnv()
	if err != nil {
		return err
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	var store storage.Storage
	switch cfg.Storage.Driver {
	case "mem":
		store = mem.New()
	case "s3compat":
		s3, err := s3compat.New(cfg.Storage)
		if err != nil {
			return err
		}
		if err := s3.EnsureBucket(ctx); err != nil {
			return err
		}
		store = s3
	}

	api := upload.New(upload.Options{
		Pool:  pool,
		Store: store,
		Caps:  cfg.Caps,
		Keep: ingest.KeepRules{
			Fonts: cfg.KeepExternal.Fonts, JS: cfg.KeepExternal.JS,
			Icons: cfg.KeepExternal.Icons, Misc: cfg.KeepExternal.Misc,
		},
		Token: cfg.AuthToken,
	})
	ts := httptest.NewServer(serve.New(serve.Options{Store: store, Upload: api, Ping: pool.Ping}))
	defer ts.Close()

	// Build the sample pack in memory: a Framer-style export with local
	// assets plus deliberately kept external references.
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	mustFile := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			panic(err)
		}
	}
	mustFile("index.html", `<!doctype html><html><head>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter">
<link rel="stylesheet" href="style.css">
<script src="https://cdn.jsdelivr.net/npm/framer-motion@11/dist/framer-motion.js"></script>
</head><body>
<main class="hero">
<h1>Framer-style sample page</h1>
<p>Baked locally: the stylesheet, the image, the font-face. Kept external: fonts + script CDNs.</p>
<img src="assets/hero.png" srcset="assets/hero.png 1x, assets/hero@2x.png 2x" alt="hero">
</main></body></html>`)
	mustFile("style.css", `@font-face{font-family:Inter;src:url(fonts/inter.woff2) format("woff2")}
@import "theme.css";
.hero{background:url(assets/bg.png);font-family:Inter,sans-serif}`)
	mustFile("theme.css", `.hero{color:teal}`)
	mustFile("assets/hero.png", "PNG-HERO-DATA")
	mustFile("assets/hero@2x.png", "PNG-HERO-2X-DATA")
	mustFile("assets/bg.png", "PNG-BG-DATA")
	mustFile("fonts/inter.woff2", "WOFF2-INTER-DATA")
	if err := zw.Close(); err != nil {
		return err
	}

	// POST through the real API (multipart, bearer token).
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("file", "sample.zip")
	if err != nil {
		return err
	}
	if _, err := fw.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := mw.WriteField("identifier", "sample"); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/pages", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out := &bytes.Buffer{}
	_, _ = out.ReadFrom(resp.Body)
	fmt.Printf("seed: HTTP %d %s\n", resp.StatusCode, out.String())
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("seed upload failed")
	}
	return nil
}
