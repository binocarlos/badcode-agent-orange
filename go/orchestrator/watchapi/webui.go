package watchapi

import (
	"embed"
	"net/http"
)

//go:embed webui/index.html webui/app.js
var webFS embed.FS

// serveWeb serves the thin single-page client. Registered on the Mux at GET / and
// GET /app.js. The page is a pure consumer of the §8 API — no business logic in JS.
func (h *Handlers) serveWeb(w http.ResponseWriter, r *http.Request) {
	path := "webui/index.html"
	if r.URL.Path == "/app.js" {
		path = "webui/app.js"
		w.Header().Set("Content-Type", "application/javascript")
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	b, err := webFS.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(b)
}
