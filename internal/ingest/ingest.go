// Package ingest transforms an uploaded pack (single HTML or zip) into the
// bucket layout: entry normalized to index.html, local assets stored,
// external refs classified and best-effort baked (design D2/D3).
package ingest

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
)

// osModeSymlink marks symlink entries in zip file modes.
const osModeSymlink = os.ModeSymlink

// Asset statuses (manifest taxonomy, D3).
const (
	StatusLocal        = "local"
	StatusBaked        = "baked"
	StatusKeptCDN      = "kept-cdn"
	StatusKeptExternal = "kept-external"
)

// ErrNoEntry means the pack contains no HTML at all.
var ErrNoEntry = fmt.Errorf("ingest: pack contains no HTML entry")

// File is an object ready to store.
type File struct {
	Data        []byte
	ContentType string
}

// ManifestRow records one processed asset.
type ManifestRow struct {
	Path        string
	SourceURL   string
	ContentType string
	Bytes       int64
	Status      string
}

// Result is the outcome of processing a pack.
type Result struct {
	Entry    []byte          // rewritten entry HTML, stored as {slug}/index.html
	Files    map[string]File // additional files to store under {slug}/{path}
	Manifest []ManifestRow
}

// Limits bounds zip expansion (design D10/D11).
type Limits struct {
	MaxFiles        int
	MaxDecompressed int64
	MaxAssetBytes   int64
}

// OpenPack returns the pack's files keyed by their cleaned relative paths.
// It enforces zip-safety: no traversal, absolute paths, symlinks, nested
// zips, oversized entries, or file-count/decompressed-size overruns.
func OpenPack(zipBytes []byte, lim Limits) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("ingest: open zip: %w", err)
	}
	if len(zr.File) > lim.MaxFiles {
		return nil, fmt.Errorf("ingest: pack has %d entries, max %d", len(zr.File), lim.MaxFiles)
	}
	files := make(map[string][]byte, len(zr.File))
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.Mode()&osModeSymlink != 0 {
			return nil, fmt.Errorf("ingest: symlink entry rejected: %s", f.Name)
		}
		name := path.Clean(f.Name)
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) ||
			strings.Contains(name, "..") || strings.Contains(name, `\`) ||
			path.IsAbs(name) {
			return nil, fmt.Errorf("ingest: unsafe entry path rejected: %s", f.Name)
		}
		if strings.EqualFold(path.Ext(name), ".zip") {
			return nil, fmt.Errorf("ingest: nested zip rejected: %s", name)
		}
		limit := lim.MaxAssetBytes
		if total+limit > lim.MaxDecompressed {
			limit = lim.MaxDecompressed - total
		}
		data, err := readLimited(f, limit)
		if err != nil {
			return nil, fmt.Errorf("ingest: read %s: %w", name, err)
		}
		total += int64(len(data))
		if total > lim.MaxDecompressed {
			return nil, fmt.Errorf("ingest: decompressed size exceeds cap (%d)", lim.MaxDecompressed)
		}
		files[name] = data
	}
	return files, nil
}

// readLimited reads at most limit+1 bytes; a full limit+1 read means over.
func readLimited(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("entry exceeds %d bytes", limit)
	}
	return data, nil
}

// DetectEntry returns the pack's entry path: root index.html, else the
// shallowest .html (ties broken lexicographically).
func DetectEntry(files map[string][]byte) (string, error) {
	if _, ok := files["index.html"]; ok {
		return "index.html", nil
	}
	best := ""
	bestDepth := -1
	for name := range files {
		if !isHTML(name) {
			continue
		}
		depth := strings.Count(name, "/")
		if bestDepth == -1 || depth < bestDepth || (depth == bestDepth && name < best) {
			best, bestDepth = name, depth
		}
	}
	if best == "" {
		return "", ErrNoEntry
	}
	return best, nil
}

func isHTML(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".html" || ext == ".htm"
}

// EntryDir returns the directory refs in the entry resolve against.
func EntryDir(entryPath string) string {
	return path.Dir(entryPath)
}

// Local processes pack-local assets: every non-entry file becomes a stored
// file with a self-determined content type and a `local` manifest row.
// (The scanner/classifier extends this in D3; this is the zip-local core.)
func Local(files map[string][]byte, entryPath string) *Result {
	res := &Result{Entry: files[entryPath], Files: make(map[string]File)}
	names := make([]string, 0, len(files))
	for name := range files {
		if name != entryPath {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		ct := ContentTypeFor(name, data)
		res.Files[name] = File{Data: data, ContentType: ct}
		res.Manifest = append(res.Manifest, ManifestRow{
			Path: name, SourceURL: name, ContentType: ct,
			Bytes: int64(len(data)), Status: StatusLocal,
		})
	}
	return res
}

var extTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".css":   "text/css",
	".js":    "text/javascript",
	".mjs":   "text/javascript",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".pdf":   "application/pdf",
	".txt":   "text/plain; charset=utf-8",
	".xml":   "application/xml",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
	".mp3":   "audio/mpeg",
	".wav":   "audio/wav",
}

// ContentTypeFor determines a content type ourselves — never from source
// headers: curated extension table first, then byte sniffing.
func ContentTypeFor(name string, data []byte) string {
	if ct, ok := extTypes[strings.ToLower(path.Ext(name))]; ok {
		return ct
	}
	// Sniff the bytes; DetectContentType needs up to 512 of them.
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	return http.DetectContentType(sample)
}
