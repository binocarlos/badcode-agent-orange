package titlebot

import (
	"context"
	"log"
	"strings"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

const prompt = "You are a title generator. Given the start of a conversation, produce a short title (max 8 words, max 60 characters). Return ONLY the title text, no quotes or punctuation wrapping."

// SessionStore is the subset of agentdb.Store needed by titlebot.
// *agentdb.Store satisfies this interface.
type SessionStore interface {
	GetSession(ctx context.Context, id string) (*agentdb.Session, error)
	UpdateSession(ctx context.Context, session *agentdb.Session) (*agentdb.Session, error)
}

// Generate generates a title for a session from the user's first message.
// Runs synchronously — call in a goroutine for async use.
func Generate(ctx context.Context, store SessionStore, client agentkit.ChatClient, sessionID, userMessage, assistantResponse string) {
	input := userMessage
	if assistantResponse != "" {
		resp := assistantResponse
		if len(resp) > 500 {
			resp = resp[:500]
		}
		input += "\n\nAssistant: " + resp
	}

	chatResp, err := client.Complete(ctx, agentkit.ChatRequest{
		Model: "claude-haiku-4-5",
		Messages: []agentkit.ChatMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: input},
		},
		MaxTokens:   60,
		Temperature: 0.3,
	})
	if err != nil {
		log.Printf("[titlebot] LLM call failed for session %s: %v", sessionID, err)
		return
	}

	title := strings.TrimSpace(chatResp.Content)
	if len(title) > 60 {
		title = title[:57] + "..."
	}

	session, err := store.GetSession(ctx, sessionID)
	if err != nil {
		log.Printf("[titlebot] failed to get session %s: %v", sessionID, err)
		return
	}
	session.Title = title
	if _, err := store.UpdateSession(ctx, session); err != nil {
		log.Printf("[titlebot] failed to update session %s title: %v", sessionID, err)
		return
	}
	log.Printf("[titlebot] session %s titled: %s (in=%d, out=%d)", sessionID, title, chatResp.InputTokens, chatResp.OutputTokens)
}
