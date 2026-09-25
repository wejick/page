package ingest

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func mustZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}
func TestOpenPackSafety(t *testing.T) {
	lim := Limits{MaxFiles: 10, MaxDecompressed: 1 << 20, MaxAssetBytes: 1 << 20}

	t.Run("valid pack", func(t *testing.T) {
		files, err := OpenPack(mustZip(t, map[string]string{
			"index.html": "<h1>", "assets/a.png": "png", "css/x.css": "a{}",
		}), lim)
		if err != nil {
			t.Fatalf("OpenPack: %v", err)
		}
		if len(files) != 3 || string(files["index.html"]) != "<h1>" {
			t.Fatalf("files = %v", files)
		}
	})

	t.Run("traversal rejected", func(t *testing.T) {
		if _, err := OpenPack(mustZip(t, map[string]string{"../evil.txt": "x"}), lim); err == nil {
			t.Fatal("traversal accepted")
		}
	})

	t.Run("absolute path rejected", func(t *testing.T) {
		if _, err := OpenPack(mustZip(t, map[string]string{"/etc/evil.txt": "x"}), lim); err == nil {
			t.Fatal("absolute path accepted")
		}
	})

	t.Run("nested zip rejected", func(t *testing.T) {
		if _, err := OpenPack(mustZip(t, map[string]string{"inner.zip": "PK"}), lim); err == nil {
			t.Fatal("nested zip accepted")
		}
	})

	t.Run("file count capped", func(t *testing.T) {
		many := map[string]string{}
		for i := 0; i < 11; i++ {
			many[fmt.Sprintf("f%d.txt", i)] = "x"
		}
		if _, err := OpenPack(mustZip(t, many), lim); err == nil {
			t.Fatal("file count overrun accepted")
		}
	})

	t.Run("decompressed size capped", func(t *testing.T) {
		small := Limits{MaxFiles: 10, MaxDecompressed: 10, MaxAssetBytes: 100}
		if _, err := OpenPack(mustZip(t, map[string]string{"big.txt": "0123456789abcdef"}), small); err == nil {
			t.Fatal("decompressed overrun accepted")
		}
	})

	t.Run("per-asset cap enforced", func(t *testing.T) {
		small := Limits{MaxFiles: 10, MaxDecompressed: 1 << 20, MaxAssetBytes: 4}
		if _, err := OpenPack(mustZip(t, map[string]string{"big.txt": "0123456789"}), small); err == nil {
			t.Fatal("per-asset overrun accepted")
		}
	})
}

func TestDetectEntry(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		want    string
		wantErr error
	}{
		{name: "root index preferred", files: []string{"index.html", "deep/page.html"}, want: "index.html"},
		{name: "shallowest fallback", files: []string{"deep/nested/x.html", "page.html"}, want: "page.html"},
		{name: "tie broken lexicographically", files: []string{"b/page.html", "a/page.html"}, want: "a/page.html"},
		{name: "no html", files: []string{"a.png", "b.css"}, wantErr: ErrNoEntry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string][]byte{}
			for _, f := range tt.files {
				files[f] = []byte("x")
			}
			got, err := DetectEntry(files)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("entry = %q err = %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestLocalStoresAssetsWithSelfDeterminedTypes(t *testing.T) {
	files := map[string][]byte{
		"index.html":  []byte("<html></html>"),
		"style.css":   []byte("body{}"),
		"app.js":      []byte("let x=1"),
		"hero.png":    []byte("\x89PNG\r\n\x1a\n"),
		"mystery.bin": []byte{0x00, 0x01, 0x02},
	}
	res := Local(files, "index.html")

	if string(res.Entry) != "<html></html>" {
		t.Fatalf("entry = %q", res.Entry)
	}
	if len(res.Files) != 4 || len(res.Manifest) != 4 {
		t.Fatalf("files=%d manifest=%d, want 4/4", len(res.Files), len(res.Manifest))
	}
	want := map[string]string{
		"style.css": "text/css", "app.js": "text/javascript",
		"hero.png": "image/png",
	}
	for path, ct := range want {
		if res.Files[path].ContentType != ct {
			t.Fatalf("%s content type = %q, want %q", path, res.Files[path].ContentType, ct)
		}
	}
	for _, row := range res.Manifest {
		if row.Status != StatusLocal {
			t.Fatalf("%s status = %q, want local", row.Path, row.Status)
		}
		if row.Bytes != int64(len(files[row.Path])) {
			t.Fatalf("%s bytes = %d", row.Path, row.Bytes)
		}
	}
}

func TestContentTypeForSniffsUnknown(t *testing.T) {
	if ct := ContentTypeFor("x.unknownext", []byte("<html>hi</html>")); ct != "text/html; charset=utf-8" {
		t.Fatalf("sniff = %q", ct)
	}
}
