package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:static
var assets embed.FS

func Handler() http.Handler {
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := cleanPath(r.URL.Path)
		if name == "" {
			name = "index.html"
		}

		if strings.Contains(path.Base(name), ".") {
			if _, err := fs.Stat(staticFS, name); err == nil {
				http.FileServer(http.FS(staticFS)).ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}

		index, err := fs.ReadFile(staticFS, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

func cleanPath(requestPath string) string {
	cleaned := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}
