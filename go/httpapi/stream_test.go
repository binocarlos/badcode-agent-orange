package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bayes-price/agentkit"
)

func TestSendMessageStreamsSSE(t *testing.T) {
	h, _ := New(Config{
		Runner: stubRunner{sendFn: func(_ context.Context, _ agentkit.SessionRef, m agentkit.SendMessageRequest, w agentkit.Writer) error {
			_, _ = w.Write([]byte("event: connected\ndata: {\"queryId\":\"q1\"}\n\n"))
			_, _ = w.Write([]byte("event: query_complete\ndata: {\"status\":\"complete\"}\n\n"))
			return nil
		}},
		Store:    stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c"}, nil },
	})
	req := httptest.NewRequest("POST", "/agent/session/s1/message", strings.NewReader(`{"content":"hi"}`))
	req.SetPathValue("id", "s1")
	rec := httptest.NewRecorder()
	h.SendMessage(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	got := rec.Body.String()
	if !strings.Contains(got, "event: connected") || !strings.Contains(got, "query_complete") {
		t.Fatalf("frames missing: %q", got)
	}
}

func TestSendMessageErrorFrame(t *testing.T) {
	h, _ := New(Config{
		Runner: stubRunner{sendFn: func(_ context.Context, _ agentkit.SessionRef, _ agentkit.SendMessageRequest, _ agentkit.Writer) error {
			return errors.New("boom")
		}},
		Store:    stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c"}, nil },
	})
	req := httptest.NewRequest("POST", "/agent/session/s1/message", strings.NewReader(`{"content":"hi"}`))
	req.SetPathValue("id", "s1")
	rec := httptest.NewRecorder()
	h.SendMessage(rec, req)

	got := rec.Body.String()
	if !strings.Contains(got, "event: error") {
		t.Fatalf("expected error frame, got: %q", got)
	}
}
