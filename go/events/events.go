// Package events defines the canonical agent event vocabulary, the SSE envelope,
// compaction, and the host-side event pipeline. This is the Go successor to the
// Platinum orchestrator's message-capture + compact-events plus the SSE relay in
// agent.go. The Go definitions here are canonical; the TS copies in sandbox/ and
// web/ mirror them.
//
// See docs/05-event-streaming.md.
package events

import (
	"context"
	"io"
)

// Type is an SSE event type. These are the GENERIC events every agent product
// needs. Application-specific events (table_rendered, dashboard_created, ...) are
// registered by tool/render plugins as extension types and dispatched by name —
// they are deliberately NOT in this core vocabulary.
type Type string

const (
	// Message lifecycle.
	MessageStart  Type = "message_start"
	ContentDelta  Type = "content_delta"
	ThinkingDelta Type = "thinking_delta"
	MessageEnd    Type = "message_end"
	// Tool lifecycle.
	ToolUseStart   Type = "tool_use_start"
	ToolUseEnd     Type = "tool_use_end"
	ToolProgress   Type = "tool_progress"
	ToolInputDelta Type = "tool_input_delta"
	// Interaction & artifacts (generic).
	AskUser            Type = "ask_user"
	ArtifactRegistered Type = "artifact_registered"
	ArtifactsUpdated   Type = "artifacts_updated"
	SkillHoisted       Type = "skill_hoisted"
	SkillInstalled     Type = "skill_installed"
	// Status / diagnostics.
	SystemStatus   Type = "system_status"
	SessionInfo    Type = "session_info"
	ActivityUpdate Type = "activity_update"
	HookEvent      Type = "hook_event"
	SubagentEvent  Type = "subagent_event"
	UserMessage    Type = "user_message"
	Heartbeat      Type = "heartbeat"
	// Terminal.
	QueryComplete Type = "query_complete"
	Error         Type = "error"
	// Connection marker emitted by the in-image stream service (dropped in compaction).
	Connected Type = "connected"
)

// Envelope is one server-sent event.
type Envelope struct {
	Type      Type           `json:"type"`
	Data      map[string]any `json:"data"`
	Timestamp string         `json:"timestamp,omitempty"`
}

// QueryContext identifies the turn an EventPipeline is processing.
type QueryContext struct {
	SessionID string
	QueryID   string

	// LeadingEvents are host-supplied envelopes that belong to this query but do
	// not originate from the in-image SSE stream (e.g. the user_message for the
	// prompt that started the turn). The pipeline seeds its collected buffer with
	// them so they are compacted + persisted in order, BEFORE the streamed events
	// of this query — but it does NOT tee them to the live client writer (the
	// frontend renders the user's prompt optimistically, so re-streaming it would
	// produce a duplicate bubble). On replay they render through the same reducer.
	LeadingEvents []Envelope
}

// Result summarises a completed pipeline run.
type Result struct {
	EventCount     int
	PersistedCount int
	Status         string // "complete" | "error" | "cancelled"
}

// Sink persists compacted events. It also carries the flush-guard hooks so the
// orchestration core can block archiving while a flush is in flight. Implemented
// over the host's SessionStore.
type Sink interface {
	BeginFlush(sessionID string)
	PersistQueryEvents(ctx context.Context, sessionID, queryID string, events []Envelope, searchText string) error
	EndFlush(sessionID string)
}

// MarkerHook is a host side-effect fired for a specific event type as it streams
// (e.g. artifact_registered -> pull bytes + ArtifactStore.Save). Registered on the
// pipeline so the generic core stays free of host specifics.
type MarkerHook func(ctx context.Context, q QueryContext, ev Envelope)

// EventPipeline consumes the in-image agent's SSE stream for one query, relays
// raw bytes to a client writer, and in parallel compacts + persists the events.
type EventPipeline interface {
	// Run reads SSE frames from src (the in-image agent response body), writes
	// them verbatim to client (the browser), accumulates + compacts them, and
	// persists via the Sink on a cadence and at end-of-query. Honours the flush
	// guard through the Sink hooks and fires registered MarkerHooks.
	Run(ctx context.Context, q QueryContext, src io.Reader, client io.Writer) (Result, error)
}
