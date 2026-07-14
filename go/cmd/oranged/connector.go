package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// fileConnector is the v0 world seam: an approved post appends one JSON line to
// published.jsonl. It is reachable ONLY through ApprovalService.Approve (the
// publish gate stays intact); the real ChannelConnector swaps in behind the
// same Connector seam once the target channel is chosen (channel_connector.go,
// Open Decision #1).
type fileConnector struct {
	mu   sync.Mutex
	path string
}

type publishedRecord struct {
	Time      string `json:"time"`
	Channel   string `json:"channel"`
	Text      string `json:"text"`
	DedupeKey string `json:"dedupe_key"`
	Ref       string `json:"ref"`
}

// Publish appends the post, idempotently per DedupeKey (contracts §10b E-5): a
// redelivered approval returns the existing ref and never double-publishes.
func (c *fileConnector) Publish(_ context.Context, p orchestrator.Post) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if data, err := os.ReadFile(c.path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			var rec publishedRecord
			if json.Unmarshal([]byte(line), &rec) == nil && rec.DedupeKey == p.DedupeKey {
				return rec.Ref, nil
			}
		}
	}
	rec := publishedRecord{
		Time:      time.Now().UTC().Format(time.RFC3339),
		Channel:   p.Channel,
		Text:      p.Text,
		DedupeKey: p.DedupeKey,
		Ref:       "file://published/" + p.DedupeKey,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return "", err
	}
	log.Printf("published: channel=%s ref=%s", rec.Channel, rec.Ref)
	return rec.Ref, nil
}

var _ orchestrator.Connector = (*fileConnector)(nil)
