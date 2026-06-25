package titlebot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	agentkit "github.com/bayes-price/agentkit"
	"github.com/bayes-price/agentkit/agentdb"
)

// mockChatClient implements agentkit.ChatClient for testing.
type mockChatClient struct {
	response *agentkit.ChatResponse
	err      error
	captured *agentkit.ChatRequest
}

func (m *mockChatClient) Complete(ctx context.Context, req agentkit.ChatRequest) (*agentkit.ChatResponse, error) {
	m.captured = &req
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

// mockSessionStore implements SessionStore for testing.
type mockSessionStore struct {
	sessions  map[string]*agentdb.Session
	updated   *agentdb.Session
	getErr    error
	updateErr error
}

func (m *mockSessionStore) GetSession(ctx context.Context, id string) (*agentdb.Session, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	sess, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	return sess, nil
}

func (m *mockSessionStore) UpdateSession(ctx context.Context, session *agentdb.Session) (*agentdb.Session, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	m.updated = session
	return session, nil
}

func TestGenerateHappyPath(t *testing.T) {
	store := &mockSessionStore{
		sessions: map[string]*agentdb.Session{
			"sess-1": {ID: "sess-1", Title: ""},
		},
	}
	client := &mockChatClient{
		response: &agentkit.ChatResponse{
			Content:      "Data Analysis Overview",
			InputTokens:  50,
			OutputTokens: 10,
		},
	}

	Generate(context.Background(), store, client, "sess-1", "Please analyze this data", "")

	if store.updated == nil {
		t.Fatal("expected session to be updated")
	}
	if store.updated.Title != "Data Analysis Overview" {
		t.Fatalf("expected title 'Data Analysis Overview', got %q", store.updated.Title)
	}
	// Verify the prompt structure sent to the LLM
	if client.captured == nil {
		t.Fatal("expected ChatRequest to be captured")
	}
	if client.captured.Model != "claude-haiku-4-5" {
		t.Fatalf("expected model claude-haiku-4-5, got %q", client.captured.Model)
	}
	if len(client.captured.Messages) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(client.captured.Messages))
	}
	if client.captured.Messages[0].Role != "system" {
		t.Fatalf("expected first message role 'system', got %q", client.captured.Messages[0].Role)
	}
}

func TestGenerateLongAssistantResponseTruncated(t *testing.T) {
	store := &mockSessionStore{
		sessions: map[string]*agentdb.Session{
			"sess-2": {ID: "sess-2", Title: ""},
		},
	}
	client := &mockChatClient{
		response: &agentkit.ChatResponse{Content: "Short Title"},
	}

	longResponse := strings.Repeat("x", 1000)
	Generate(context.Background(), store, client, "sess-2", "Hello", longResponse)

	// The user message sent to the LLM should contain the assistant response
	// truncated to 500 chars.
	userContent := client.captured.Messages[1].Content
	if !strings.Contains(userContent, "Assistant: ") {
		t.Fatal("expected user content to include assistant response")
	}
	// The assistant portion should be at most 500 chars
	parts := strings.SplitN(userContent, "Assistant: ", 2)
	if len(parts) < 2 {
		t.Fatal("expected 'Assistant: ' delimiter in user content")
	}
	if len(parts[1]) > 500 {
		t.Fatalf("expected assistant portion <= 500 chars, got %d", len(parts[1]))
	}
}

func TestGenerateLLMFailure(t *testing.T) {
	store := &mockSessionStore{
		sessions: map[string]*agentdb.Session{
			"sess-3": {ID: "sess-3", Title: "original"},
		},
	}
	client := &mockChatClient{
		err: fmt.Errorf("API rate limit"),
	}

	// Should not panic
	Generate(context.Background(), store, client, "sess-3", "Hello", "")

	// Session title should not have been updated
	if store.updated != nil {
		t.Fatal("expected session NOT to be updated on LLM failure")
	}
}

func TestGenerateTitleTruncation(t *testing.T) {
	store := &mockSessionStore{
		sessions: map[string]*agentdb.Session{
			"sess-4": {ID: "sess-4", Title: ""},
		},
	}
	longTitle := strings.Repeat("A", 80) // 80 chars, exceeds 60 limit
	client := &mockChatClient{
		response: &agentkit.ChatResponse{Content: longTitle},
	}

	Generate(context.Background(), store, client, "sess-4", "Hello", "")

	if store.updated == nil {
		t.Fatal("expected session to be updated")
	}
	if len(store.updated.Title) != 60 {
		t.Fatalf("expected title truncated to 60 chars, got %d", len(store.updated.Title))
	}
	if !strings.HasSuffix(store.updated.Title, "...") {
		t.Fatal("expected truncated title to end with '...'")
	}
}
