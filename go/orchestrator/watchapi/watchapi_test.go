package watchapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRequiresPorts(t *testing.T) {
	if _, err := New(newTestConfig()); err != nil {
		t.Fatalf("valid config errored: %v", err)
	}
	// each required port missing → error
	missingBoard := newTestConfig()
	missingBoard.Board = nil
	if _, err := New(missingBoard); err == nil {
		t.Fatalf("expected error when Board nil")
	}
	missingTickets := newTestConfig()
	missingTickets.Tickets = nil
	if _, err := New(missingTickets); err == nil {
		t.Fatalf("expected error when Tickets nil")
	}
	missingRejecter := newTestConfig()
	missingRejecter.Rejecter = nil
	if _, err := New(missingRejecter); err == nil {
		t.Fatalf("expected error when Rejecter nil")
	}
}

func TestAuthGuard(t *testing.T) {
	cfg := newTestConfig()
	cfg.AuthToken = "secret"
	h, _ := New(cfg)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	// no token → 401
	resp, _ := http.Get(srv.URL + "/api/runs")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: got %d want 401", resp.StatusCode)
	}
	// wrong token → 401
	req, _ := http.NewRequest("GET", srv.URL+"/api/runs", nil)
	req.Header.Set("Authorization", "Bearer nope")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d want 401", resp.StatusCode)
	}
	// right token → 200
	req, _ = http.NewRequest("GET", srv.URL+"/api/runs", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("right token: got %d want 200", resp.StatusCode)
	}
}
