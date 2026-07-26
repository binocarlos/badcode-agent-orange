package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

type createSessionBody struct {
	SessionID     string   `json:"sessionId"`
	Job           string   `json:"job"`
	Persona       string   `json:"persona"`
	Model         string   `json:"model"`
	SystemPrompt  string   `json:"systemPrompt"`
	Tools         []string `json:"tools"`
	Harness       string   `json:"harness"`
	CustomImageID string   `json:"customImageId"`
	Installation  string   `json:"installation"`
}

type createSessionResp struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	WorkflowID string `json:"workflowId"`
}

// newID returns a random 32-hex-char id (no external dep).
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateSession persists the session row (state=creating), provisions via the
// Runner, and returns {id,status,workflowId}. On Runner error it marks the row
// state=error and responds 500 (host owns durable delete via its own store).
func (h *Handlers) CreateSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	var body createSessionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sid := body.SessionID
	if sid == "" {
		sid = newID()
	}
	// Resolve installation → image reference when the host has wired an ImageResolver,
	// but only when no explicit CustomImageID is present. When both arrive (the frontend
	// always auto-sends an installation), the caller's custom image must win: leave
	// Image empty so resolveLaunchImage (runner.go) ranks CustomImageID above Image.
	var resolvedImage string
	if h.cfg.ImageResolver != nil && body.CustomImageID == "" {
		ref, err := h.cfg.ImageResolver(body.Installation)
		if err != nil {
			http.Error(w, "installation not available: "+err.Error(), http.StatusBadRequest)
			return
		}
		resolvedImage = ref
	}
	// Provisioning includes a force-pull of the launch image, which can take from
	// seconds to minutes. Rather than block the POST for that whole window, return
	// immediately with status "creating" and provision in the background; the
	// frontend polls GET /session/{id}/status to render download progress (the
	// runner streams image-pull bytes into the per-session progress store).
	//
	// MarkCreating pre-registers the "create" progress op synchronously (before we
	// background the work) so a status poll that races ahead of the goroutine still
	// sees an active op and keeps polling instead of treating the not-yet-running
	// session as settled. Capability-probed so non-runner stubs stay compatible.
	//
	// It runs BEFORE the row is written, and that order is load-bearing: it also
	// registers the create with the Runner so a DELETE landing mid-create can
	// abort it. A DELETE cannot reach us until the row exists (the ownership
	// check 404s without it), so registering first leaves no window in which a
	// delete is possible but invisible to the create.
	if mc, ok := h.cfg.Runner.(interface{ MarkCreating(string) }); ok {
		mc.MarkCreating(sid)
	}

	// Persist the row before provisioning (Runner contract).
	_, _ = h.cfg.Store.UpdateSession(r.Context(), &agentdb.Session{
		ID: sid, Customer: id.Customer, Job: body.Job,
		UserEmail: id.UserEmail, Persona: body.Persona, Status: "creating",
		WorkflowID: "agent", Installation: body.Installation,
		CustomImageID: body.CustomImageID,
	})

	createReq := agentkit.CreateSessionRequest{
		SessionID:     sid,
		Persona:       body.Persona,
		Customer:      id.Customer,
		Job:           body.Job,
		UserEmail:     id.UserEmail,
		Model:         body.Model,
		SystemPrompt:  body.SystemPrompt,
		Harness:       agentkit.Harness(body.Harness),
		CustomImageID: body.CustomImageID,
		Image:         resolvedImage,
	}

	// Detach from the request context: it is cancelled when this handler returns,
	// which would abort provisioning the instant we respond.
	bg := context.WithoutCancel(r.Context())
	go func() {
		if _, err := h.cfg.Runner.CreateSession(bg, createReq); err != nil {
			// The session was deleted while we were provisioning it. That is not
			// a failure and there is no row left to mark: the Runner has already
			// destroyed whatever container the create had built (and released its
			// host port), which is the whole point of the abort. Touching the row
			// here would only risk stamping "error" on a re-created session of
			// the same id.
			if errors.Is(err, agentkit.ErrSessionDeleted) {
				log.Printf("httpapi: session %s was deleted while it was being created; create aborted", sid)
				return
			}
			// This goroutine used to be where every create diagnostic died: the
			// error was neither logged nor persisted, and `status = "error"` was
			// all that survived — so the caller's next message took the
			// no-instance-and-no-snapshot path and was told the session was
			// LOST. The Runner now records the reason on the session row
			// (agent_sessions.create_error) before returning, and reads it back
			// on exactly that path. Log it here too: this is the request that
			// asked for the session, and an operator reading agentd's log needs
			// the session id next to the failure.
			log.Printf("httpapi: background create of session %s failed: %v", sid, err)
			// Get-patch-write so we only flip Status and don't clobber the rest of
			// the row (stores do a full replace) — in particular not the
			// create_error the Runner has just written.
			if sess, getErr := h.cfg.Store.GetSession(bg, sid); getErr == nil && sess != nil {
				sess.Status = "error"
				_, _ = h.cfg.Store.UpdateSession(bg, sess)
			}
			return
		}
		if sess, getErr := h.cfg.Store.GetSession(bg, sid); getErr == nil && sess != nil {
			sess.Status = "running"
			_, _ = h.cfg.Store.UpdateSession(bg, sess)
		}
	}()

	// The library has no separate workflow concept, so WorkflowID echoes the
	// session id (the frontend's useAgentSession expects a workflowId field).
	writeJSON(w, createSessionResp{ID: sid, Status: "creating", WorkflowID: sid})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
