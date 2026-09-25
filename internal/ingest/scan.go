package ingest

import (
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// --- HTML walking ---------------------------------------------------------

// resourceAttrs maps tags to the attributes holding resource refs.
// Navigation hrefs (a, area, base) are deliberately excluded.
var resourceAttrs = map[string][]string{
	"img":    {"src", "srcset", "longdesc"},
	"script": {"src"},
	"source": {"src", "srcset"},
	"video":  {"src", "poster"},
	"audio":  {"src"},
	"track":  {"src"},
	"embed":  {"src"},
	"input":  {"src"},
	"image":  {"href", "xlink:href"}, // SVG
	"use":    {"href", "xlink:href"}, // SVG
}

// linkRelRewrite lists link rel values whose href is a bakeable resource.
func linkRelRewrite(rel string) bool {
	rel = strings.ToLower(rel)
	for _, want := range []string{"stylesheet", "icon", "apple-touch-icon", "preload"} {
		for _, part := range strings.Fields(rel) {
			if part == want {
				return true
			}
		}
	}
	return false
}

// walkRefs visits every resource reference in the document, calling visit for
// each (node, attrName, rawValue) with a css context flag.
func walkRefs(doc *html.Node, visit func(n *html.Node, attr string, raw string, isCSS bool)) {
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "style":
				// Inline CSS: the text child is a CSS context.
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode && strings.TrimSpace(c.Data) != "" {
						visit(n, "#text", c.Data, true)
					}
				}
			default:
				for _, attrName := range resourceAttrs[n.Data] {
					if v := attrValue(n, attrName); v != "" {
						visit(n, attrName, v, attrName == "style")
					}
				}
				if n.Data == "link" {
					if linkRelRewrite(attrValue(n, "rel")) {
						if v := attrValue(n, "href"); v != "" {
							visit(n, "href", v, mayBeCSS(v))
						}
					}
				}
			}
			if v := attrValue(n, "style"); v != "" {
				visit(n, "style", v, true)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttrValue(n *html.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
}

// mayBeCSS guesses whether a link href points at a stylesheet.
func mayBeCSS(href string) bool {
	return strings.EqualFold(path.Ext(strings.TrimSpace(href)), ".css")
}

// --- CSS scanning ---------------------------------------------------------

var (
	cssURLRe = regexp.MustCompile(`url\(\s*(['"]?)([^'")]+)(['"]?)\s*\)`)
	// RE2 has no backreferences: url-form and quoted forms are flat
	// alternatives (groups: 1,2,3 = url(); 4 = "double"; 5 = 'single').
	cssImportRe = regexp.MustCompile(`@import\s+(?:url\(\s*(['"]?)([^'")]+)(['"]?)\s*\)|"([^"]*)"|'([^']*)')\s*;?`)
)

// cssRefs extracts all raw references (url() and @import) from CSS text.
func cssRefs(css string) (urls []string, imports []string) {
	for _, m := range cssURLRe.FindAllStringSubmatch(css, -1) {
		raw := strings.TrimSpace(m[2])
		if raw != "" {
			urls = append(urls, raw)
		}
	}
	for _, m := range cssImportRe.FindAllStringSubmatch(css, -1) {
		raw := m[2]
		if raw == "" {
			raw = m[5]
		}
		if raw = strings.TrimSpace(raw); raw != "" {
			urls = append(urls, raw)
			imports = append(imports, raw)
		}
	}
	return urls, imports
}

// rewriteCSS substitutes every url()/@import target via lookup; refs the
// lookup does not resolve are left untouched.
func rewriteCSS(css string, lookup func(raw string) (string, bool)) string {
	out := cssImportRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssImportRe.FindStringSubmatch(m)
		raw := sub[2]
		if raw == "" {
			raw = sub[4]
		}
		if raw == "" {
			raw = sub[5]
		}
		raw = strings.TrimSpace(raw)
		if nv, ok := lookup(raw); ok {
			return `@import url(` + nv + `);`
		}
		return m
	})
	return cssURLRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := cssURLRe.FindStringSubmatch(m)
		raw := strings.TrimSpace(sub[2])
		if raw == "" {
			return m
		}
		if nv, ok := lookup(raw); ok {
			return `url(` + sub[1] + nv + sub[3] + `)`
		}
		return m
	})
}

// rewriteSrcset rewrites each candidate URL in a srcset value.
func rewriteSrcset(value string, fn func(raw string) string) string {
	parts := strings.Split(value, ",")
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		fields[0] = fn(fields[0])
		parts[i] = strings.Join(fields, " ")
	}
	return strings.Join(parts, ", ")
}

// --- ref classification ----------------------------------------------------

// skipRef reports refs that must never be touched (fragments, data URIs, …).
func skipRef(raw string) bool {
	low := strings.TrimSpace(strings.ToLower(raw))
	for _, p := range []string{"#", "data:", "mailto:", "javascript:", "tel:", "about:", "blob:"} {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// absoluteRef parses raw as an absolute http(s) URL (scheme-relative included).
func absoluteRef(raw string) (*url.URL, bool) {
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return u, true
	}
	return nil, false
}

// upgradeHTTPS rewrites an http:// kept URL to https:// (mixed-content guard).
func upgradeHTTPS(raw string) string {
	if strings.HasPrefix(raw, "http://") {
		return "https://" + strings.TrimPrefix(raw, "http://")
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}
