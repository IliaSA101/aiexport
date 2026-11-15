package server

import (
	"io/fs"
	"net/http"
	"path"
)

// staticHandler раздаёт веб-UI. Content-Type выставляется явно по расширению:
// иначе его берёт из реестра Windows, где .js бывает text/plain, и браузер
// отказывается грузить ES-модули.
func staticHandler(web fs.FS) http.Handler {
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "метод "+r.Method+" не поддерживается")
			return
		}
		switch path.Ext(r.URL.Path) {
		case ".js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case ".svg":
			w.Header().Set("Content-Type", "image/svg+xml")
		case ".html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		files.ServeHTTP(w, r)
	})
}
