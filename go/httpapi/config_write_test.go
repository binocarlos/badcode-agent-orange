package httpapi

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Every configuration mutation reachable over HTTP threads a ConfigWrite down
// to the store, and for an HTTP request that ConfigWrite is empty: §15.2 says a
// human/UI/API edit records no acting worker and no acting session. The
// emptiness is the recorded fact, so it is worth a test — a handler that
// invented an actor would put a lie in the changelog.

func TestHTTPConfigMutationsPassAHumanEditActor(t *testing.T) {
	tests := []struct {
		name string
		// run performs one request and returns the recorded write plus the
		// number of store mutations the request made.
		run func(t *testing.T) (agentdb.ConfigWrite, int)
	}{
		{
			name: "PUT /agent/project-settings",
			run: func(t *testing.T) (agentdb.ConfigWrite, int) {
				store := newFakeProjectSettings()
				h := newProjectSettingsHandlers(t, store, identityFor("acme"))
				rec := doProjectSettings(h, http.MethodPut, `{"system_prompt":"be brief"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite, store.puts
			},
		},
		{
			name: "PUT /agent/workers/{name}",
			run: func(t *testing.T) (agentdb.ConfigWrite, int) {
				store := newFakeWorkerStore()
				h := workerHandlers(t, store, identityFor("acme"))
				rec := do(h, http.MethodPut, "/agent/workers/email-answerer", `{"description":"answers email"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite, store.writes
			},
		},
		{
			name: "DELETE /agent/workers/{name}",
			run: func(t *testing.T) (agentdb.ConfigWrite, int) {
				store := newFakeWorkerStore(agentdb.NewWorker("acme", "archivist"))
				h := workerHandlers(t, store, identityFor("acme"))
				rec := do(h, http.MethodDelete, "/agent/workers/archivist", "")
				if rec.Code != http.StatusNoContent {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite, store.writes
			},
		},
		{
			name: "POST /agent/subscriptions",
			run: func(t *testing.T) (agentdb.ConfigWrite, int) {
				store := newFakeEventStore()
				h := newEventHandlers(t, store, identityFor("acme"))
				rec := do(h, http.MethodPost, "/agent/subscriptions",
					`{"event_type":"email.received","worker":"email-answerer"}`)
				if rec.Code != http.StatusCreated {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite, store.writes
			},
		},
		{
			name: "PUT /agent/subscriptions/{id}",
			run: func(t *testing.T) (agentdb.ConfigWrite, int) {
				store := newFakeEventStore()
				h := newEventHandlers(t, store, identityFor("acme"))
				created := do(h, http.MethodPost, "/agent/subscriptions",
					`{"event_type":"email.received","worker":"email-answerer"}`)
				if created.Code != http.StatusCreated {
					t.Fatalf("seed status=%d body=%s", created.Code, created.Body)
				}
				var sub agentdb.Subscription
				decodeInto(t, created, &sub)
				store.writes = 0 // count only the update below

				rec := do(h, http.MethodPut, "/agent/subscriptions/"+sub.ID, `{"worker":"email-reviewer"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite, store.writes
			},
		},
		{
			name: "DELETE /agent/subscriptions/{id}",
			run: func(t *testing.T) (agentdb.ConfigWrite, int) {
				store := newFakeEventStore()
				h := newEventHandlers(t, store, identityFor("acme"))
				created := do(h, http.MethodPost, "/agent/subscriptions",
					`{"event_type":"email.received","worker":"email-answerer"}`)
				var sub agentdb.Subscription
				decodeInto(t, created, &sub)
				store.writes = 0

				rec := do(h, http.MethodDelete, "/agent/subscriptions/"+sub.ID, "")
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite, store.writes
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cw, writes := tc.run(t)
			if writes != 1 {
				t.Fatalf("one request must make exactly one configuration write, got %d", writes)
			}
			if cw != (agentdb.ConfigWrite{}) {
				t.Fatalf("an HTTP edit must log no actor and no rationale (§15.2), got %+v", cw)
			}
		})
	}
}

// The same property against the real store: one HTTP request appends exactly
// one config event, with an empty actor and the action §15.3 names. Skipped
// without a live database — the sqlite fakes above cover the handler wiring,
// this covers the wiring plus the seam plus the migration together.
func TestHTTPConfigMutationsAppendOneEvent_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	ctx := context.Background()
	project := "httpapi-cfg-" + t.Name()
	t.Cleanup(func() {
		_ = store.PurgeConfigEvents(context.Background(), project)
		_ = store.DB().Exec("DELETE FROM workers WHERE project = ?", project).Error
		_ = store.DB().Exec("DELETE FROM subscriptions WHERE project = ?", project).Error
		_ = store.DB().Exec("DELETE FROM project_settings WHERE project = ?", project).Error
	})

	h := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{},
		Identity: identityFor(project),
		AgentDB:  store,
	})

	if rec := do(h, http.MethodPut, "/agent/workers/email-answerer",
		`{"description":"answers inbound email"}`); rec.Code != http.StatusOK {
		t.Fatalf("put worker: status=%d body=%s", rec.Code, rec.Body)
	}

	evs, err := store.ListConfigEvents(ctx, agentdb.ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("one HTTP write must append exactly one config event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Action != agentdb.ActionWorkerCreate {
		t.Fatalf("action: want %q, got %q", agentdb.ActionWorkerCreate, ev.Action)
	}
	if ev.ActorWorker != "" || ev.ActorSession != "" || ev.Rationale != "" {
		t.Fatalf("an HTTP edit must log no actor and no rationale (§15.2): %+v", ev)
	}
	if ev.Payload["name"] != "email-answerer" || ev.Payload["description"] != "answers inbound email" {
		t.Fatalf("payload must be the full new row: %+v", ev.Payload)
	}

	// …and the delete appends its own record, carrying the final state (§15.3).
	if rec := do(h, http.MethodDelete, "/agent/workers/email-answerer", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete worker: status=%d body=%s", rec.Code, rec.Body)
	}
	evs, err = store.ListConfigEvents(ctx, agentdb.ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 2 || evs[0].Action != agentdb.ActionWorkerDelete {
		t.Fatalf("delete must append a worker_delete record, got %d events (newest %q)", len(evs), evs[0].Action)
	}
	if evs[0].Payload["description"] != "answers inbound email" {
		t.Fatalf("delete payload must carry the row as it last stood: %+v", evs[0].Payload)
	}
}
