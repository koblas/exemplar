// Package webassets serves the stylesheets the /setup pages and the /ui/ web
// UI share: a vendored Bulma release (static/README.md) and the few
// overrides in static/app.css. They are embedded, so a cluster without
// internet access renders the pages the same.
package webassets

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"time"
)

// Prefix is the path both page sets load their stylesheets from. setup's
// handler serves it in setup mode; exemplar-api's mux serves it after.
const Prefix = "/static/"

// The stylesheet paths the pages link to. Bulma's file name carries its
// version, so an upgrade is a new URL.
const (
	Bulma = Prefix + "bulma-1.0.4.min.css"
	App   = Prefix + "app.css"
)

//go:embed static/*.css
var files embed.FS

type asset struct {
	body []byte
	etag string
}

var assets = load()

func load() map[string]asset {
	out := map[string]asset{}
	entries, err := fs.ReadDir(files, "static")
	if err != nil {
		panic(err) // the embed directive guarantees the directory
	}
	for _, e := range entries {
		body, err := fs.ReadFile(files, "static/"+e.Name())
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(body)
		out[e.Name()] = asset{body: body, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	}
	return out
}

// Handler serves GET Prefix+name. It needs no sign-in: the sign-in page
// loads it. Browsers revalidate with the ETag, so each binary's stylesheets
// download once.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		a, ok := assets[name]
		if !ok || r.URL.Path != Prefix+name {
			http.NotFound(w, r)
			return
		}
		hd := w.Header()
		hd.Set("Content-Type", "text/css; charset=utf-8")
		hd.Set("Cache-Control", "no-cache")
		hd.Set("ETag", a.etag)
		hd.Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(a.body))
	})
}
