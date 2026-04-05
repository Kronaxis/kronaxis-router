package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui
var uiFS embed.FS

func registerUI(mux *http.ServeMux) {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		logger.Printf("WARNING: failed to embed UI: %v", err)
		return
	}

	indexHTML, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		logger.Printf("WARNING: failed to read index.html: %v", err)
		return
	}

	// Serve index.html directly for root to avoid FileServer redirect loop
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(indexHTML)
			return
		}
		// Serve other static files via FileServer
		http.FileServer(http.FS(sub)).ServeHTTP(w, r)
	})

	logger.Println("UI registered at /")
}
