package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// The daily token budget against a REAL Postgres ledger (TOK1).
//
// router_test.go proves the gate's POLICY — soft notifies once, hard queues,
// midnight resets — against a fake store that returns whatever number the test
// says. That is the whole reason TOK1 survived to production: every policy test
// was green while the real ledger, `agentdb.CountProjectTokensSince`, summed a
// jsonb path no stored row has ever had and therefore answered 0 for every
// project, forever. A ceiling measured against a constant zero cannot fire.
//
// This test closes that gap: the real store, real session rows, and a real
// captured `query_complete` envelope, through the gate's own Allow().
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://... go test ./cmd/agentd/ -run TestLiveTokenBudget
//
// ---------------------------------------------------------------------------

// liveCapturedQueryEvents is a REAL stored `agent_query_events.events` value,
// read out of the running e2e stack's Postgres on 2026-07-28
// (`agent-orange-stack-e2e-postgres-1`). Middle envelopes are elided for
// length; the `query_complete` is verbatim apart from the two numbers. Note
// where the usage lives — nested under `data.usage`, camelCase, on the LAST
// envelope, not flat snake_case on the first.
func liveCapturedQueryEvents(in, out int) string {
	return fmt.Sprintf(`[
	  {"type":"user_message","timestamp":"2026-07-25T23:29:03Z","data":{"content":"Event: schedule.fired"}},
	  {"type":"message_start","timestamp":"2026-07-25T23:29:03.604Z","data":{"role":"assistant","messageId":"e322c9b0-6ccd-4170-aa14-a82faf70fc4f"}},
	  {"type":"query_complete","timestamp":"2026-07-25T23:29:06.243Z",
	   "data":{"model":"claude-opus-4-5",
	           "usage":{"inputTokens":%d,"outputTokens":%d},
	           "result":"Hello from the agentd mock model proxy. Set ANTHROPIC_API_KEY for a real agent.",
	           "status":"completed",
	           "queryId":"1e74bdb0-5666-4c21-8c67-8e928355c84a",
	           "totalCostUsd":0.0004}}
	]`, in, out)
}

// spendLiveTokens writes one stored query for `project`, exactly as a finished
// job would leave it behind.
func spendLiveTokens(t *testing.T, s *agentdb.Store, project string, in, out int) {
	t.Helper()
	ctx := context.Background()
	sess, err := s.CreateSession(ctx, &agentdb.Session{
		UserEmail: "u@x.y", Customer: project, WorkflowID: "chat", Worker: "spender",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteSession(context.Background(), sess.ID) })
	if err := s.DB().WithContext(ctx).Exec(`
		INSERT INTO agent_query_events (id, session_id, query_id, events, search_text, created_at)
		VALUES (?, ?, ?, ?::jsonb, '', ?)`,
		uuid.New().String(), sess.ID, "q-"+uuid.New().String(),
		liveCapturedQueryEvents(in, out), time.Now().Unix(),
	).Error; err != nil {
		t.Fatalf("insert usage: %v", err)
	}
}

// TestLiveTokenBudgetRefusesOnceRealSpendCrossesHard is the case that could
// never have passed before TOK1: the ledger reads a real envelope, the number
// is non-zero, and the gate says no.
func TestLiveTokenBudgetRefusesOnceRealSpendCrossesHard(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project := "proj-" + uuid.New().String()

	settings := agentdb.DefaultProjectSettings(project)
	settings.DailyTokensHard = 20

	budget := newTokenBudget(tokenBudgetConfig{Store: store, Logf: func(string, ...any) {}})

	// Nothing spent yet: an unspent project is allowed, and the ledger says 0
	// because it really is 0 — not because the SQL cannot see anything.
	allowed, err := budget.Allow(ctx, project, settings)
	if err != nil || !allowed {
		t.Fatalf("an unspent project must be allowed: allowed=%v err=%v", allowed, err)
	}

	// One finished job's worth of real usage: 16 + 8 = 24, over the ceiling of 20.
	spendLiveTokens(t, store, project, 16, 8)

	used, err := store.CountProjectTokensSince(ctx, project, startOfDay(time.Now()).Unix())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if used != 24 {
		t.Fatalf("the ledger must read the stored envelope: got %d tokens, want 24", used)
	}

	allowed, err = budget.Allow(ctx, project, settings)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if allowed {
		t.Fatalf("daily_tokens_hard=%d with %d spent must refuse the dispatch", settings.DailyTokensHard, used)
	}

	// Raising the ceiling above the same spend re-allows it — proving the
	// refusal was the budget arithmetic and not some unrelated veto.
	settings.DailyTokensHard = 1000
	allowed, err = budget.Allow(ctx, project, settings)
	if err != nil || !allowed {
		t.Fatalf("under the ceiling must be allowed: allowed=%v err=%v", allowed, err)
	}

	// 0 = off (§5): unmetered never even queries the ledger.
	settings.DailyTokensHard = 0
	settings.DailyTokensSoft = 0
	if allowed, err := budget.Allow(ctx, project, settings); err != nil || !allowed {
		t.Fatalf("an unmetered project is always allowed: allowed=%v err=%v", allowed, err)
	}
}

// TestLiveTokenBudgetSoftTierSeesRealSpend: the soft tier's notification is the
// only warning an operator gets before the hard tier stops their workforce, and
// it fired on a constant zero — i.e. never — for the same reason.
func TestLiveTokenBudgetSoftTierSeesRealSpend(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project := "proj-" + uuid.New().String()

	settings := agentdb.DefaultProjectSettings(project)
	settings.DailyTokensSoft = 10

	var notices []int64
	budget := newTokenBudget(tokenBudgetConfig{
		Store: store, Logf: func(string, ...any) {},
		Notify: func(_ context.Context, _ string, _ *agentdb.ProjectSettings, used int64) {
			notices = append(notices, used)
		},
	})

	spendLiveTokens(t, store, project, 10, 6)

	// Soft alone never stops anything, but it must notify — twice through the
	// gate, once on the wire (§5's "exactly one per day").
	for i := 0; i < 2; i++ {
		allowed, err := budget.Allow(ctx, project, settings)
		if err != nil || !allowed {
			t.Fatalf("soft tier must not stop a job: allowed=%v err=%v", allowed, err)
		}
	}
	if len(notices) != 1 || notices[0] != 16 {
		t.Fatalf("want exactly one notice for 16 real tokens, got %v", notices)
	}

	// Another project's spend is invisible: a shared ledger that leaked across
	// projects would stop innocent workforces.
	other := "proj-" + uuid.New().String()
	otherSettings := agentdb.DefaultProjectSettings(other)
	otherSettings.DailyTokensHard = 1
	if allowed, err := budget.Allow(ctx, other, otherSettings); err != nil || !allowed {
		t.Fatalf("a project that has spent nothing must be allowed: allowed=%v err=%v", allowed, err)
	}
}
