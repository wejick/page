package ingest

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// Pipeline transforms a pack into bucket objects: it scans references,
// classifies external ones (D3), fetches what must be baked, and rewrites
// everything to origin-absolute paths (D4).
type Pipeline struct {
	Keep  KeepRules
	Fetch *Fetcher
}

// planAction is what will happen to one unique reference identity.
type planAction int

const (
	planSkip planAction = iota
	planLocal
	planKeep
	planBake
)

// rewriteMode is how an HTML attribute value gets rebuilt at finalize time.
type rewriteMode int

const (
	modeSingle rewriteMode = iota
	modeSrcset
	modeCSS
)

// plan is the per-identity resolution. Identities are deduped: a URL
// referenced five times is fetched once and stored once.
type plan struct {
	identity  string // absolute URL, or "local:<path>"
	action    planAction
	fetchURL  string // planBake
	storePath string // local + successful bake
	newValue  string // final replacement value
	data      []byte
	ct        string
	status    string
	sourceURL string
	fetchDone bool
}

type cssJob struct {
	storePath string // local css file path ("" for external)
	data      []byte
	baseDir   string // pack dir for local, URL dir for external
}

type htmlTarget struct {
	n       *html.Node
	attr    string
	raw     string
	baseDir string
	mode    rewriteMode
}

type baker struct {
	slug            string
	files           map[string][]byte
	keep            KeepRules
	fetcher         *Fetcher
	plans           map[string]*plan
	toStore         map[string]File
	cssQueue        []*cssJob
	cssScanned      map[string]bool
	bakeOrder       []string
	kept            []*plan
	targets         []htmlTarget
	scannedLocalCSS []*cssJob
	bakedPaths      map[string]bool
}

// Process runs the full pipeline for a pack and returns the rewritten entry,
// files to store, and the manifest.
func (p *Pipeline) Process(ctx context.Context, files map[string][]byte, entryPath, slug string) (*Result, error) {
	entryDir := EntryDir(entryPath)

	b := &baker{
		slug:       slug,
		files:      files,
		keep:       p.Keep,
		fetcher:    p.Fetch,
		plans:      map[string]*plan{},
		toStore:    make(map[string]File, len(files)),
		cssScanned: map[string]bool{},
		bakedPaths: map[string]bool{},
	}
	for name, data := range files {
		if name == entryPath {
			continue
		}
		b.toStore[name] = File{Data: data, ContentType: ContentTypeFor(name, data)}
	}

	// Phase 1: parse the entry, collect reference plans (fetches deferred).
	doc, err := html.Parse(bytes.NewReader(files[entryPath]))
	if err != nil {
		return nil, fmt.Errorf("ingest: parse entry: %w", err)
	}
	var styleTexts []*html.Node
	walkRefs(doc, func(n *html.Node, attr, raw string, isCSS bool) {
		switch {
		case attr == "#text": // <style> element text
			styleTexts = append(styleTexts, n)
			b.scanCSS(&cssJob{data: []byte(raw), baseDir: entryDir})
		case attr == "style": // inline style attribute
			b.scanCSS(&cssJob{data: []byte(raw), baseDir: entryDir})
			b.targets = append(b.targets, htmlTarget{n: n, attr: attr, raw: raw, baseDir: entryDir, mode: modeCSS})
		case attr == "srcset":
			for _, cand := range srcsetURLs(raw) {
				b.planFor(cand, entryDir)
			}
			b.targets = append(b.targets, htmlTarget{n: n, attr: attr, raw: raw, baseDir: entryDir, mode: modeSrcset})
		default:
			pl := b.planFor(raw, entryDir)
			if isCSS || isStylesheetNode(n) {
				b.maybeEnqueueCSS(pl, pl.storePath)
			}
			b.targets = append(b.targets, htmlTarget{n: n, attr: attr, raw: raw, baseDir: entryDir, mode: modeSingle})
		}
	})

	// Phase 2: fetch rounds + CSS scanning until stable (D10 budget applies).
	if err := b.run(ctx); err != nil {
		return nil, err
	}

	// Phase 3: finalize — apply rewrites, render, build manifest.
	for _, t := range b.targets {
		switch t.mode {
		case modeSingle:
			if pl := b.plans[planIdentity(t.raw, t.baseDir, b.files)]; pl != nil &&
				pl.action != planSkip && pl.newValue != "" {
				setAttrValue(t.n, t.attr, pl.newValue)
			}
		case modeSrcset:
			setAttrValue(t.n, t.attr, rewriteSrcset(t.raw, func(raw string) string {
				if pl := b.plans[planIdentity(raw, t.baseDir, b.files)]; pl != nil &&
					pl.action != planSkip && pl.newValue != "" {
					return pl.newValue
				}
				return raw
			}))
		case modeCSS:
			setAttrValue(t.n, t.attr, b.renderCSS(t.raw, t.baseDir))
		}
	}
	for _, t := range styleTexts {
		for c := t.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				c.Data = b.renderCSS(c.Data, entryDir)
			}
		}
	}
	// Rewritten local stylesheets replace their original bytes.
	for _, job := range b.scannedLocalCSS {
		if f, ok := b.toStore[job.storePath]; ok {
			f.Data = []byte(b.renderCSS(string(job.data), job.baseDir))
			b.toStore[job.storePath] = f
		}
	}

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, fmt.Errorf("ingest: render entry: %w", err)
	}

	res := &Result{Entry: buf.Bytes(), Files: b.toStore}
	for p2, f := range b.toStore {
		if b.bakedPaths[p2] {
			continue // baked rows are emitted separately
		}
		res.Manifest = append(res.Manifest, ManifestRow{
			Path: p2, SourceURL: p2, ContentType: f.ContentType,
			Bytes: int64(len(f.Data)), Status: StatusLocal,
		})
	}
	for _, pl := range b.plans {
		if pl.action == planBake && pl.data != nil {
			res.Manifest = append(res.Manifest, ManifestRow{
				Path: pl.storePath, SourceURL: pl.sourceURL, ContentType: pl.ct,
				Bytes: int64(len(pl.data)), Status: StatusBaked,
			})
		}
	}
	// Kept refs (cdn by policy, or failed bake): one row each, no stored path.
	for _, pl := range b.kept {
		res.Manifest = append(res.Manifest, ManifestRow{
			Path: pl.sourceURL, SourceURL: pl.sourceURL,
			ContentType: guessTypeFromURL(pl.sourceURL),
			Status:      pl.status,
		})
	}
	sort.Slice(res.Manifest, func(i, j int) bool { return res.Manifest[i].Path < res.Manifest[j].Path })
	return res, nil
}

// run drains CSS scanning and fetch rounds until no work remains.
func (b *baker) run(ctx context.Context) error {
	for {
		for len(b.cssQueue) > 0 {
			job := b.cssQueue[0]
			b.cssQueue = b.cssQueue[1:]
			b.scanCSS(job)
			if job.storePath != "" {
				b.scannedLocalCSS = append(b.scannedLocalCSS, job)
			}
		}
		if len(b.bakeOrder) == 0 {
			return nil
		}
		urls := b.bakeOrder
		b.bakeOrder = nil
		results := b.fetcher.FetchAll(ctx, urls)
		for _, u := range urls {
			pl := b.plans[u]
			if pl == nil || pl.fetchDone {
				continue
			}
			pl.fetchDone = true
			r := results[u]
			if r.Err != nil || r.Data == nil {
				// Best effort (D3): keep the original URL, record kept-external.
				pl.action = planKeep
				pl.status = StatusKeptExternal
				pl.newValue = upgradeHTTPS(pl.sourceURL)
				b.kept = append(b.kept, pl)
				continue
			}
			pl.data = r.Data
			pl.ct = ContentTypeFor(pl.storePath, r.Data)
			b.toStore[pl.storePath] = File{Data: r.Data, ContentType: pl.ct}
			b.bakedPaths[pl.storePath] = true
			if isCSPath(pl.storePath) || strings.Contains(pl.ct, "text/css") {
				if !b.cssScanned[pl.identity] {
					b.cssScanned[pl.identity] = true
					b.cssQueue = append(b.cssQueue, &cssJob{
						data: pl.data, baseDir: urlDir(pl.sourceURL),
					})
				}
			}
		}
	}
}

// scanCSS collects plans for one CSS context and queues local stylesheets.
func (b *baker) scanCSS(job *cssJob) {
	urls, imports := cssRefs(string(job.data))
	for _, raw := range append(urls, imports...) {
		pl := b.planFor(raw, job.baseDir)
		if pl.action == planLocal && isCSPath(pl.storePath) && !b.cssScanned[pl.identity] {
			b.cssScanned[pl.identity] = true
			b.cssQueue = append(b.cssQueue, &cssJob{
				storePath: pl.storePath, data: b.files[pl.storePath],
				baseDir: path.Dir(pl.storePath),
			})
		}
	}
}

// maybeEnqueueCSS is the html-side queue helper for stylesheet references.
func (b *baker) maybeEnqueueCSS(pl *plan, _ string) {
	if pl == nil || pl.action != planLocal || !isCSPath(pl.storePath) {
		return
	}
	if !b.cssScanned[pl.identity] {
		b.cssScanned[pl.identity] = true
		b.cssQueue = append(b.cssQueue, &cssJob{
			storePath: pl.storePath, data: b.files[pl.storePath],
			baseDir: path.Dir(pl.storePath),
		})
	}
}

// isStylesheetNode reports whether a <link> element's rel marks a resource.
func isStylesheetNode(n *html.Node) bool {
	return n.Data == "link" && linkRelRewrite(attrValue(n, "rel"))
}

// renderCSS rewrites CSS text using plans known at call time.
func (b *baker) renderCSS(css, baseDir string) string {
	return rewriteCSS(css, func(raw string) (string, bool) {
		pl := b.plans[planIdentity(raw, baseDir, b.files)]
		if pl == nil || pl.action == planSkip || pl.newValue == "" {
			return "", false
		}
		return pl.newValue, true
	})
}

// planIdentity is the dedupe key for a raw reference in a base directory.
func planIdentity(raw, baseDir string, files map[string][]byte) string {
	raw = strings.TrimSpace(raw)
	if skipRef(raw) {
		return "skip:" + raw
	}
	if u, ok := absoluteRef(raw); ok {
		return u.String()
	}
	var resolved string
	if strings.HasPrefix(raw, "/") {
		resolved = path.Clean(raw)
	} else {
		resolved = path.Clean(path.Join(baseDir, raw))
	}
	resolved = strings.TrimPrefix(strings.TrimPrefix(resolved, "./"), "/")
	return "local:" + resolved
}

// planFor resolves one raw reference to its plan, deduped by identity.
func (b *baker) planFor(raw, baseDir string) *plan {
	raw = strings.TrimSpace(raw)
	id := planIdentity(raw, baseDir, b.files)
	if pl, ok := b.plans[id]; ok {
		return pl
	}
	if strings.HasPrefix(id, "skip:") {
		pl := &plan{identity: id, action: planSkip}
		b.plans[id] = pl
		return pl
	}
	if u, ok := absoluteRef(raw); ok {
		source := u.String()
		pl := &plan{identity: source, sourceURL: source}
		b.plans[id] = pl
		if !IsSignedURL(u) && b.keep.ShouldKeep(u) {
			pl.action = planKeep
			pl.status = StatusKeptCDN
			pl.newValue = upgradeHTTPS(source)
			b.kept = append(b.kept, pl)
			return pl
		}
		pl.action = planBake
		pl.fetchURL = source
		pl.storePath = externalStorePath(u)
		pl.newValue = "/a/" + b.slug + "/" + pl.storePath
		b.bakeOrder = append(b.bakeOrder, source)
		return pl
	}
	resolved := strings.TrimPrefix(id, "local:")
	data, ok := b.files[resolved]
	if !ok {
		// Broken local ref: nothing we can bake — leave untouched.
		pl := &plan{identity: id, action: planSkip}
		b.plans[id] = pl
		return pl
	}
	pl := &plan{
		identity: id, action: planLocal, storePath: resolved,
		newValue: "/a/" + b.slug + "/" + resolved,
		data:     data, ct: ContentTypeFor(resolved, data), sourceURL: resolved,
	}
	b.plans[id] = pl
	return pl
}

// externalStorePath derives a stable bucket path for a fetched external asset.
func externalStorePath(u *url.URL) string {
	p := u.Path
	if p == "" || strings.HasSuffix(p, "/") {
		p += "index"
	}
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == '/':
			return r
		default:
			return '-'
		}
	}, strings.TrimPrefix(path.Clean(p), "/"))
	return "external/" + strings.ToLower(u.Hostname()) + "/" + clean
}

func srcsetURLs(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	return out
}

func isCSPath(p string) bool {
	return strings.EqualFold(path.Ext(p), ".css")
}

func urlDir(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	dir, _ := path.Split(parsed.Path)
	return dir
}

// guessTypeFromURL types kept (unstored) refs from their extension.
func guessTypeFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return "application/octet-stream"
	}
	if ct, ok := extTypes[strings.ToLower(path.Ext(parsed.Path))]; ok {
		return ct
	}
	return "application/octet-stream"
}
