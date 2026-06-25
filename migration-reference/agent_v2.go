package server

// agent_v2.go — agentkit v2 route registration and SSE streaming handlers. (rebuild trigger)

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog/log"

	agentkit "github.com/bayes-price/agentkit"
	"github.com/bayes-price/agentkit/artifacts"
	"github.com/bayes-price/agentkit/httpapi"
	"github.com/bayes-price/agentkit/titlebot"

	"github.com/Bayes-Price/Platinum/goapi/pkg/installations"
)

// platinumEndpoints are the agentkit httpapi route patterns with {customer} in
// the path for Platinum tenancy. platinumIdentityFromRequest reads {customer}
// via r.PathValue("customer") after the ServeMux routes the request.
var platinumEndpoints = httpapi.Endpoints{
	CreateSession:  "POST /agent/{customer}/session",
	SendMessage:    "POST /agent/{customer}/session/{id}/message",
	Stream:         "GET /agent/{customer}/session/{id}/stream",
	Reconnect:      "GET /agent/{customer}/session/{id}/reconnect",
	Cancel:         "POST /agent/{customer}/session/{id}/cancel",
	Status:         "GET /agent/{customer}/session/{id}/status",
	GetSession:     "GET /agent/{customer}/session/{id}",
	DeleteSession:  "DELETE /agent/{customer}/session/{id}",
	Restore:        "POST /agent/{customer}/session/{id}/restore",
	Messages:       "GET /agent/{customer}/session/{id}/messages",
	QueryEvents:    "GET /agent/{customer}/session/{id}/query-events",
	ListSessions:   "GET /agent/{customer}/sessions",
	SearchMessages: "GET /agent/{customer}/messages/search",
	Artifacts:      "GET /agent/{customer}/session/{id}/artifacts",
	CreateArtifact: "POST /agent/{customer}/session/{id}/artifacts",
	Upload:         "POST /agent/{customer}/session/{id}/upload",
	Snapshot:       "POST /agent/{customer}/session/{id}/snapshot",
	Archive:        "POST /agent/{customer}/session/{id}/archive",
}

// RegisterAgentKitRoutes mounts the agentkit v2 route tree under /api/v1/agentkit.
// Call only when deps.AgentRunner is set (COMPOSITION_AGENT=v2).
//
// SSE routes (SendMessage, Stream, Reconnect) call Runner methods directly using
// Fiber's native BodyWriter streaming — each frame flushes immediately.
//
// All other routes bridge to the httpapi net/http mux via fasthttpadaptor.
func (apiServer *PlatinumAPIServer) RegisterAgentKitRoutes() {
	runner := apiServer.deps.AgentRunner
	artStore := apiServer.deps.AgentArtifacts

	// Auth + email injection as group-level Use middleware — Fiber v3 requires
	// this for SendStreamWriter to produce Transfer-Encoding: chunked.
	agentKit := apiServer.router.Group("/agentkit", apiServer.requireAuthMiddleware, agentKitAuthInjector)

	// SSE routes — registered BEFORE the catch-all bridge so they win.
	agentKit.Post("/agent/:customer/session/:id/message",
		apiServer.agentKitSendMessage(runner))
	agentKit.Get("/agent/:customer/session/:id/stream",
		apiServer.agentKitStream(runner))
	agentKit.Get("/agent/:customer/session/:id/reconnect",
		apiServer.agentKitReconnect(runner))

	// Non-SSE routes require AgentDB (agentdb.Store). Skip if not wired (e.g. tests).
	if apiServer.deps.AgentDB == nil {
		return
	}

	// Build httpapi Handlers for non-SSE routes.
	var artStoreIface artifacts.ArtifactStore
	if artStore != nil {
		artStoreIface = artStore
	}
	api, err := httpapi.New(httpapi.Config{
		Runner:     runner,
		Store:      apiServer.deps.AgentDB,
		Artifacts:  artStoreIface,
		Identity:   platinumIdentityFromRequest,
		Endpoints:  platinumEndpoints,
		AgentDB:    apiServer.deps.AgentDB,
		ChatClient: apiServer.deps.AgentChatClient,
		ImageResolver: func(name string) (string, error) {
			mode := apiServer.cfg.Agent.V2ImageRegistry
			regURL := apiServer.cfg.Agent.V2RegistryURL
			if name == "" {
				ref, ok := installations.Default(mode, apiServer.cfg.Agent.V2DefaultInstallation, regURL)
				if !ok {
					return "", nil // fall back to Policy.BaseImage
				}
				return ref, nil
			}
			ref, ok := installations.Resolve(name, mode, regURL)
			if !ok {
				return "", fmt.Errorf("installation %q not built", name)
			}
			return ref, nil
		},
	})
	if err != nil {
		log.Fatal().Err(err).Msg("RegisterAgentKitRoutes: httpapi.New failed")
	}
	mux := api.Mux()

	// Non-SSE catch-all — bridge to httpapi mux via fasthttpadaptor.
	agentKit.All("/*", newAgentKitBridgeHandler(mux))
}

func (apiServer *PlatinumAPIServer) agentKitSendMessage(runner agentkit.Runner) fiber.Handler {
	type body struct {
		Content     string                `json:"content"`
		Job         string                `json:"job"`
		Persona     string                `json:"persona"`
		Model       string                `json:"model"`
		Attachments []agentkit.Attachment `json:"attachments"`
	}
	return func(c fiber.Ctx) error {
		user, ok := GetJWTUserFromContext(c)
		if !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		_ = user // user.Email available if needed later
		var b body
		if err := json.Unmarshal(c.Body(), &b); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "bad request"})
		}
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		sessionID := string([]byte(c.Params("id")))
		customer := string([]byte(c.Params("customer")))
		content := b.Content

		agentDB := apiServer.deps.AgentDB
		chatClient := apiServer.deps.AgentChatClient

		return c.SendStreamWriter(func(w *bufio.Writer) {
			sw := &fiberSSEWriter{bw: w}
			err := runner.SendMessage(context.Background(),
				agentkit.SessionRef{SessionID: sessionID},
				agentkit.SendMessageRequest{
					Content:     content,
					Customer:    customer,
					Job:         b.Job,
					Persona:     b.Persona,
					Model:       b.Model,
					Attachments: b.Attachments,
				}, sw)
			if err != nil {
				_, _ = fmt.Fprintf(sw, "event: error\ndata: {\"message\":%q}\n\n", err.Error())
			}

			if agentDB != nil && chatClient != nil && content != "" {
				sess, getErr := agentDB.GetSession(context.Background(), sessionID)
				if getErr == nil && sess.Title == "" {
					go titlebot.Generate(context.Background(), agentDB, chatClient, sessionID, content, "")
				}
			}
		})
	}
}

func (apiServer *PlatinumAPIServer) agentKitStream(runner agentkit.Runner) fiber.Handler {
	return func(c fiber.Ctx) error {
		_, ok := GetJWTUserFromContext(c)
		if !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		sessionID := string([]byte(c.Params("id")))
		queryID := string([]byte(c.Query("queryId")))

		return c.SendStreamWriter(func(w *bufio.Writer) {
			sw := &fiberSSEWriter{bw: w}
			err := runner.Stream(context.Background(),
				agentkit.SessionRef{SessionID: sessionID},
				agentkit.StreamOptions{QueryID: queryID, IsReconnect: false}, sw)
			if err != nil {
				_, _ = fmt.Fprintf(sw, "event: error\ndata: {\"message\":%q}\n\n", err.Error())
			}
		})
	}
}

func (apiServer *PlatinumAPIServer) agentKitReconnect(runner agentkit.Runner) fiber.Handler {
	return func(c fiber.Ctx) error {
		_, ok := GetJWTUserFromContext(c)
		if !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		sessionID := string([]byte(c.Params("id")))
		queryID := string([]byte(c.Query("queryId")))

		return c.SendStreamWriter(func(w *bufio.Writer) {
			sw := &fiberSSEWriter{bw: w}
			err := runner.Stream(context.Background(),
				agentkit.SessionRef{SessionID: sessionID},
				agentkit.StreamOptions{QueryID: queryID, IsReconnect: true}, sw)
			if err != nil {
				_, _ = fmt.Fprintf(sw, "event: error\ndata: {\"message\":%q}\n\n", err.Error())
			}
		})
	}
}


