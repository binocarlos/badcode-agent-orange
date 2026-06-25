package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/events"
)

// Messages returns the message history for a session.
// When AgentDB is set, returns structured messages with pagination.
// When AgentDB is nil, falls back to the legacy query-events path.
func (h *Handlers) Messages(w http.ResponseWriter, r *http.Request) {
	if h.cfg.AgentDB == nil {
		h.listQueryEventsLegacy(w, r)
		return
	}

	_, ok := h.identify(w, r)
	if !ok {
		return
	}

	sid := r.PathValue("id")
	limit := 500
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = v
	}

	messages, total, err := h.cfg.AgentDB.ListMessages(r.Context(), &agentdb.MessageQuery{
		SessionID: sid,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(messages) == 0 && offset == 0 {
		qes, qErr := h.cfg.AgentDB.ListQueryEvents(r.Context(), sid)
		if qErr == nil && len(qes) > 0 {
			messages = reconstructMessagesFromQueryEvents(qes, sid)
			total = int64(len(messages))
		}
	}
	writeJSON(w, map[string]any{
		"messages": messages,
		"count":    len(messages),
		"total":    total,
	})
}

// QueryEvents returns the raw query event rows for a session.
// When AgentDB is set, returns structured event rows.
// When AgentDB is nil, falls back to the legacy path.
func (h *Handlers) QueryEvents(w http.ResponseWriter, r *http.Request) {
	if h.cfg.AgentDB == nil {
		h.listQueryEventsLegacy(w, r)
		return
	}

	_, ok := h.identify(w, r)
	if !ok {
		return
	}

	evts, err := h.cfg.AgentDB.ListQueryEvents(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"events": evts})
}

// ListSessions lists sessions for the authenticated principal's customer.
// When AgentDB is nil, returns an empty array (the host must override the route).
func (h *Handlers) ListSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if h.cfg.AgentDB == nil {
		writeJSON(w, []any{})
		return
	}

	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = v
	}
	userEmail := id.UserEmail
	if ue := r.URL.Query().Get("user_email"); ue == "*" {
		userEmail = ""
	} else if ue != "" {
		userEmail = ue
	}

	sessions, err := h.cfg.AgentDB.ListSessions(r.Context(), &agentdb.SessionQuery{
		UserEmail: userEmail,
		Customer:  id.Customer,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessions)
}

// SearchMessages searches messages for the authenticated principal's customer.
// When AgentDB is nil, returns an empty array (the host must override the route).
func (h *Handlers) SearchMessages(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if h.cfg.AgentDB == nil {
		writeJSON(w, []any{})
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" || len(query) < 2 {
		writeJSON(w, map[string]any{"results": []any{}})
		return
	}

	results, err := h.cfg.AgentDB.SearchMessages(r.Context(), &agentdb.MessageSearchQuery{
		Customer:  id.Customer,
		UserEmail: r.URL.Query().Get("user_email"),
		Query:     query,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"results": results})
}

// listQueryEventsLegacy is the legacy path when AgentDB is nil.
func (h *Handlers) listQueryEventsLegacy(w http.ResponseWriter, r *http.Request) {
	_, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	evs, err := h.cfg.Store.ListQueryEventsFlat(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if evs == nil {
		evs = []events.Envelope{}
	}
	writeJSON(w, evs)
}

// reconstructMessagesFromQueryEvents rebuilds conversation messages from
// query event envelopes when agent_messages is empty (legacy data).
func reconstructMessagesFromQueryEvents(queryEvents []*agentdb.QueryEvents, sessionID string) []*agentdb.Message {
	var messages []*agentdb.Message
	seq := 0

	for _, qe := range queryEvents {
		if len(qe.Events) == 0 {
			continue
		}
		var evts []struct {
			Type string                 `json:"type"`
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(qe.Events, &evts); err != nil {
			continue
		}

		var assistantContent strings.Builder
		inAssistantMessage := false

		for _, evt := range evts {
			switch evt.Type {
			case "user_message":
				if content, ok := evt.Data["content"].(string); ok && content != "" {
					seq++
					messages = append(messages, &agentdb.Message{
						ID:          qe.QueryID + "-user",
						SessionID:   sessionID,
						QueryID:     qe.QueryID,
						Role:        "user",
						Content:     content,
						SequenceNum: seq,
					})
				}
			case "message_start":
				role, _ := evt.Data["role"].(string)
				if role == "assistant" {
					inAssistantMessage = true
					assistantContent.Reset()
				}
			case "content_delta":
				if inAssistantMessage {
					if delta, ok := evt.Data["delta"].(string); ok {
						assistantContent.WriteString(delta)
					}
				}
			case "message_end":
				if inAssistantMessage && assistantContent.Len() > 0 {
					seq++
					messages = append(messages, &agentdb.Message{
						ID:          qe.QueryID + "-assistant",
						SessionID:   sessionID,
						QueryID:     qe.QueryID,
						Role:        "assistant",
						Content:     assistantContent.String(),
						SequenceNum: seq,
					})
				}
				inAssistantMessage = false
				assistantContent.Reset()
			}
		}

		if inAssistantMessage && assistantContent.Len() > 0 {
			seq++
			messages = append(messages, &agentdb.Message{
				ID:          qe.QueryID + "-assistant",
				SessionID:   sessionID,
				QueryID:     qe.QueryID,
				Role:        "assistant",
				Content:     assistantContent.String(),
				SequenceNum: seq,
			})
		}
	}
	return messages
}
