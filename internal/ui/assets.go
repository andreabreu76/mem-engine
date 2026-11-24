package ui

import (
	"embed"
	"net/http"
)

//go:embed assets/index.html assets/app.js assets/app.css
var assetFS embed.FS

var assetTypes = map[string]string{
	"app.js":  "text/javascript; charset=utf-8",
	"app.css": "text/css; charset=utf-8",
}

func serveIndex(w http.ResponseWriter, _ *http.Request) {
	serveFile(w, "index.html", "text/html; charset=utf-8")
}

func serveAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ct, ok := assetTypes[name]
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	serveFile(w, name, ct)
}

func serveFile(w http.ResponseWriter, name, contentType string) {
	b, err := assetFS.ReadFile("assets/" + name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(b)
}
