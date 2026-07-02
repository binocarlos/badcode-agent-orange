package watchapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebUIServesIndex(t *testing.T) {
	h, _ := newTestHandlers(t)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: err=%v code=%v", err, statusOf(resp))
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<html") {
		t.Fatalf("index not served: %q", string(body[:min(200, len(body))]))
	}

	// app.js is served with a JS content type
	resp, err = http.Get(srv.URL + "/app.js")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /app.js: err=%v code=%v", err, statusOf(resp))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("app.js content-type = %q", ct)
	}
}
