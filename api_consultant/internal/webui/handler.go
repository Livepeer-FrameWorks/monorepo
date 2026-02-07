package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/*
var webFS embed.FS

// Config holds runtime values injected into the web UI's HTML meta tags.
type Config struct {
	APIURL string
}

// Handler returns an http.Handler that serves the embedded web UI.
// It injects Config values into index.html via meta tag placeholders.
func Handler(cfg Config) http.Handler {
	sub, _ := fs.Sub(webFS, "web")
	fileServer := http.FileServer(http.FS(sub))

	// Read index.html once at startup and cache the template.
	indexBytes, _ := fs.ReadFile(sub, "index.html")
	indexTemplate := string(indexBytes)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" || path == "/index.html" {
			serveIndex(w, indexTemplate, cfg)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, tmpl string, cfg Config) {
	html := tmpl
	html = strings.Replace(html, "__SKIPPER_API_URL__", cfg.APIURL, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}
