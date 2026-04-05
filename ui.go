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

	fileServer := http.FileServer(http.FS(sub))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Serve index.html for the root path, file server for everything else
		if r.URL.Path == "/" {
			r.URL.Path = "/index.html"
		}
		fileServer.ServeHTTP(w, r)
	})

	logger.Println("UI registered at /")
}
