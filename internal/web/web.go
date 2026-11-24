// Package web раздаёт собранный SPA (web/ → internal/web/dist).
package web

import (
	"bytes"
	"context"
	"embed"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

type baseKey struct{}

// WithBase передаёт обработчику путь, под которым прокси публикует webmail
// ("/webmail"; "" — корень): index.html получает <base href>, и относительные
// адреса SPA (assets/…, api/…) ведут под этот путь.
func WithBase(ctx context.Context, base string) context.Context {
	return context.WithValue(ctx, baseKey{}, base)
}

// Handler отдаёт файлы SPA; неизвестные пути — index.html (клиентский роутинг).
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(root))
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "SPA is not built into this binary: run `make web build`", http.StatusServiceUnavailable)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(root, p); err != nil {
			p = "index.html"
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if p == "index.html" {
			base, _ := r.Context().Value(baseKey{}).(string)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(withBase(index, base))
			return
		}
		files.ServeHTTP(w, r)
	})
}

func withBase(index []byte, base string) []byte {
	tag := []byte(`<head>` + "\n" + `    <base href="` + html.EscapeString(base) + `/" />`)
	return bytes.Replace(index, []byte("<head>"), tag, 1)
}
