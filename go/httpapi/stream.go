package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/bayes-price/agentkit"
	"github.com/bayes-price/agentkit/titlebot"
)

type sendMessageBody struct {
	Content     string                `json:"content"`
	Model       string                `json:"model"`
	Persona     string                `json:"persona"`
	Job         string                `json:"job"`
	Attachments []agentkit.Attachment `json:"attachments"`
}

// flushWriter flushes the underlying ResponseWriter after every Write so SSE
// frames reach the client immediately instead of buffering until handler return.
type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

// Stream attaches a new SSE client to the session's ongoing event stream.
func (h *Handlers) Stream(w http.ResponseWriter, r *http.Request) {
	_, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	ref := agentkit.SessionRef{SessionID: sid}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	fw := flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.f = f
		f.Flush()
	}
	opts := agentkit.StreamOptions{
		QueryID:     r.URL.Query().Get("queryId"),
		IsReconnect: false,
	}
	if err := h.cfg.Runner.Stream(r.Context(), ref, opts, fw); err != nil {
		_, _ = fw.Write([]byte("event: error\ndata: " + jsonStr(err.Error()) + "\n\n"))
	}
}

// Reconnect reattaches a disconnected SSE client to an in-flight query stream.
func (h *Handlers) Reconnect(w http.ResponseWriter, r *http.Request) {
	_, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	ref := agentkit.SessionRef{SessionID: sid}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	fw := flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.f = f
		f.Flush()
	}
	opts := agentkit.StreamOptions{
		QueryID:     r.URL.Query().Get("queryId"),
		IsReconnect: true,
	}
	if err := h.cfg.Runner.Stream(r.Context(), ref, opts, fw); err != nil {
		_, _ = fw.Write([]byte("event: error\ndata: " + jsonStr(err.Error()) + "\n\n"))
	}
}

// SendMessage runs one turn and streams its SSE to the client. It owns the SSE
// lifecycle: headers, per-frame flush, and disconnect teardown via r.Context()
// (cancelled when the client goes away → Runner's pipeline stops the turn).
func (h *Handlers) SendMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	var body sendMessageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	fw := flushWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		fw.f = f
		f.Flush()
	}
	err := h.cfg.Runner.SendMessage(r.Context(), agentkit.SessionRef{SessionID: sid}, agentkit.SendMessageRequest{
		Content:     body.Content,
		Customer:    id.Customer,
		Job:         body.Job,
		Persona:     body.Persona,
		Model:       body.Model,
		Attachments: body.Attachments,
	}, fw)
	if err != nil {
		// Best-effort error frame (headers already sent — can't change status).
		_, _ = fw.Write([]byte("event: error\ndata: " + jsonStr(err.Error()) + "\n\n"))
	}

	if h.cfg.AgentDB != nil && h.cfg.ChatClient != nil {
		sess, getErr := h.cfg.AgentDB.GetSession(r.Context(), sid)
		if getErr == nil && sess.Title == "" && body.Content != "" {
			go titlebot.Generate(context.Background(), h.cfg.AgentDB, h.cfg.ChatClient, sid, body.Content, "")
		}
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(map[string]string{"message": s})
	return string(b)
}
