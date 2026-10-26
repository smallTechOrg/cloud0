// Package webui embeds the built Next.js static export (internal/webui/dist,
// populated by `make web`) and serves it on the same origin as the API. When
// no build is present the committed placeholder page is served instead, and
// IsReal reports false so /api/v1/health can say ui: "placeholder".
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

//go:embed placeholder.html
var placeholder []byte

const indexName = "index.html"

// Mode is what the handler serves: "real" or "placeholder".
type Mode string

const (
	ModeReal        Mode = "real"
	ModePlaceholder Mode = "placeholder"
)

// IsReal reports whether a built frontend (dist/index.html) is embedded.
func IsReal() bool {
	_, err := fs.Stat(dist, path.Join("dist", indexName))
	return err == nil
}

// CurrentMode is "real" when IsReal, else "placeholder".
func CurrentMode() Mode {
	if IsReal() {
		return ModeReal
	}
	return ModePlaceholder
}

// Handler serves the embedded UI: "/" and any unknown non-/api path return
// index.html (SPA fallback, Cache-Control: no-cache); every other emitted
// file is served immutable for a year. Only GET and HEAD are accepted.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("webui: dist sub fs: " + err.Error())
	}
	real := IsReal()
	modTime := time.Now().UTC()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !real {
			serveBytes(w, r, indexName, placeholder, modTime, "no-cache")
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || name == indexName {
			serveIndex(w, r, sub, modTime)
			return
		}
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			// Next emits name.html for routes; try it before the SPA fallback.
			if alt, err2 := fs.ReadFile(sub, name+".html"); err2 == nil {
				serveBytes(w, r, name+".html", alt, modTime, "no-cache")
				return
			}
			serveIndex(w, r, sub, modTime)
			return
		}
		serveBytes(w, r, name, data, modTime, "public, max-age=31536000, immutable")
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS, modTime time.Time) {
	data, err := fs.ReadFile(sub, indexName)
	if err != nil {
		serveBytes(w, r, indexName, placeholder, modTime, "no-cache")
		return
	}
	serveBytes(w, r, indexName, data, modTime, "no-cache")
}

func serveBytes(w http.ResponseWriter, r *http.Request, name string, data []byte, modTime time.Time, cache string) {
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", cache)
	http.ServeContent(w, r, name, modTime, bytes.NewReader(data))
}
