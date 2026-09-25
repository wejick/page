// Package storagetest holds the conformance suite every Storage driver must
// pass. Unit tests run it against mem; integration tests run it against real
// MinIO via s3compat — the same suite proves the driver swap (design D7).
package storagetest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"page/internal/storage"
)

// Run executes the conformance suite against s.
func Run(t *testing.T, ctx context.Context, s storage.Storage) {
	t.Helper()

	t.Run("missing key is ErrNotFound", func(t *testing.T) {
		if _, err := s.Get(ctx, "nope/index.html"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Get missing = %v, want ErrNotFound", err)
		}
		if _, err := s.Stat(ctx, "nope/index.html"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Stat missing = %v, want ErrNotFound", err)
		}
	})

	t.Run("roundtrip preserves bytes, content type, size, etag", func(t *testing.T) {
		body := "<html>roundtrip</html>"
		if err := s.Put(ctx, "rt/index.html", "text/html; charset=utf-8", strings.NewReader(body)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		obj, err := s.Get(ctx, "rt/index.html")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		got := make([]byte, obj.Size)
		if _, err := obj.Reader.Read(got); err != nil && err.Error() != "EOF" {
			t.Fatalf("read: %v", err)
		}
		if string(got) != body {
			t.Fatalf("bytes = %q, want %q", got, body)
		}
		if obj.ContentType != "text/html; charset=utf-8" {
			t.Fatalf("content type = %q", obj.ContentType)
		}
		if obj.Size != int64(len(body)) {
			t.Fatalf("size = %d, want %d", obj.Size, len(body))
		}
		if obj.ETag == "" {
			t.Fatal("empty etag")
		}
		meta, err := s.Stat(ctx, "rt/index.html")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if meta.ContentType != obj.ContentType || meta.Size != obj.Size {
			t.Fatalf("Stat mismatch: %+v vs %+v", meta, obj)
		}
	})

	t.Run("content type is persisted metadata, extension notwithstanding", func(t *testing.T) {
		if err := s.Put(ctx, "ct/data.css", "text/css", strings.NewReader("a{color:red}")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		meta, err := s.Stat(ctx, "ct/data.css")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if meta.ContentType != "text/css" {
			t.Fatalf("content type = %q, want text/css", meta.ContentType)
		}
	})

	t.Run("copy prefix preserves objects and overwrites destinations", func(t *testing.T) {
		put := func(key, ct, body string) storage.ObjectMeta {
			t.Helper()
			if err := s.Put(ctx, key, ct, strings.NewReader(body)); err != nil {
				t.Fatalf("Put %s: %v", key, err)
			}
			meta, err := s.Stat(ctx, key)
			if err != nil {
				t.Fatalf("Stat %s: %v", key, err)
			}
			return meta
		}
		srcA := put("cp/index.html", "text/html", "<p>a</p>")
		srcB := put("cp/assets/hero.png", "image/png", "HERO")
		preExisting := put("cp2/index.html", "text/plain", "OLD")

		if err := s.Copy(ctx, "cp/", "cp2/"); err != nil {
			t.Fatalf("Copy: %v", err)
		}

		// Every copied object preserves bytes, content type, and ETag.
		for key, want := range map[string]struct {
			ct   string
			body string
			meta storage.ObjectMeta
		}{
			"cp2/index.html":      {"text/html", "<p>a</p>", srcA},
			"cp2/assets/hero.png": {"image/png", "HERO", srcB},
		} {
			obj, err := s.Get(ctx, key)
			if err != nil {
				t.Fatalf("Get %s: %v", key, err)
			}
			body := make([]byte, obj.Size)
			if _, err := obj.Reader.Read(body); err != nil && err.Error() != "EOF" {
				t.Fatalf("read %s: %v", key, err)
			}
			if string(body) != want.body {
				t.Errorf("%s bytes = %q, want %q", key, body, want.body)
			}
			if obj.ContentType != want.ct {
				t.Errorf("%s content type = %q, want %q", key, obj.ContentType, want.ct)
			}
			if obj.ETag != want.meta.ETag {
				t.Errorf("%s etag = %q, want %q (copy must preserve etag)", key, obj.ETag, want.meta.ETag)
			}
		}
		// Pre-existing destination keys are replaced, not duplicated.
		if preExisting.ContentType != "text/plain" {
			t.Fatalf("preexisting dst changed before copy: %+v", preExisting)
		}
		obj, err := s.Get(ctx, "cp2/index.html")
		if err != nil {
			t.Fatalf("Get overwritten dst: %v", err)
		}
		if obj.ContentType != "text/html" || obj.ETag != srcA.ETag {
			t.Errorf("dst not replaced: ct=%q etag=%q", obj.ContentType, obj.ETag)
		}
		// Sources are untouched.
		if meta, err := s.Stat(ctx, "cp/index.html"); err != nil || meta.ETag != srcA.ETag {
			t.Errorf("src changed after copy: %+v err=%v", meta, err)
		}
	})

	t.Run("copy with empty source prefix is a no-op", func(t *testing.T) {
		if err := s.Put(ctx, "keep-a/x", "text/plain", strings.NewReader("x")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := s.Copy(ctx, "", "elsewhere/"); err != nil {
			t.Fatalf("Copy empty prefix: %v", err)
		}
		if _, err := s.Stat(ctx, "elsewhere/keep-a/x"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("empty source prefix copied objects: err=%v, want ErrNotFound", err)
		}
	})

	t.Run("delete prefix is scoped to the prefix boundary", func(t *testing.T) {
		for _, k := range []string{"dp/one", "dp/two", "dpx/three", "keep/four"} {
			if err := s.Put(ctx, k, "application/octet-stream", strings.NewReader(k)); err != nil {
				t.Fatalf("Put %s: %v", k, err)
			}
		}
		if err := s.DeletePrefix(ctx, "dp/"); err != nil {
			t.Fatalf("DeletePrefix: %v", err)
		}
		for key, wantExists := range map[string]bool{
			"dp/one": false, "dp/two": false, "dpx/three": true, "keep/four": true,
		} {
			_, err := s.Stat(ctx, key)
			if (err == nil) != wantExists {
				t.Fatalf("key %s exists = %v, want %v", key, err == nil, wantExists)
			}
		}
	})
}
