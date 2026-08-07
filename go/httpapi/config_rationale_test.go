package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// Every configuration mutation reachable over HTTP may carry the operator's
// one-line reason (design B3 / decision K2): in the body on the writes, as
// `?rationale=` on the deletes, which have no body. The schedule routes have
// done this since §8.6 shipped; these cases pin the same behaviour on the
// workers, subscriptions and project-settings routes, so the changelog stops
// showing a human's own edits as reasonless.
//
// The companion property — that an edit with NO rationale logs an empty one,
// rather than a placeholder — is TestHTTPConfigMutationsPassAHumanEditActor in
// config_write_test.go, which asserts the whole ConfigWrite stays zero.

func TestHTTPConfigMutationsThreadTheRationale(t *testing.T) {
	const why = "customers complained the replies were long"

	tests := []struct {
		name string
		// run performs one request carrying `why` and returns the ConfigWrite
		// the store was handed.
		run func(t *testing.T) agentdb.ConfigWrite
	}{
		{
			name: "PUT /agent/project-settings",
			run: func(t *testing.T) agentdb.ConfigWrite {
				store := newFakeProjectSettings()
				h := newProjectSettingsHandlers(t, store, identityFor("acme"))
				rec := doProjectSettings(h, http.MethodPut,
					`{"system_prompt":"be brief","rationale":"`+why+`"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				// The rationale must not leak into the settings row itself.
				if got := decodeProjectSettings(t, rec); got.SystemPrompt != "be brief" {
					t.Fatalf("settings did not round-trip: %+v", got)
				}
				return store.lastWrite
			},
		},
		{
			name: "PUT /agent/workers/{name}",
			run: func(t *testing.T) agentdb.ConfigWrite {
				store := newFakeWorkerStore()
				h := workerHandlers(t, store, identityFor("acme"))
				rec := do(h, http.MethodPut, "/agent/workers/email-answerer",
					`{"description":"answers email","rationale":"`+why+`"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite
			},
		},
		{
			name: "DELETE /agent/workers/{name}",
			run: func(t *testing.T) agentdb.ConfigWrite {
				store := newFakeWorkerStore(agentdb.NewWorker("acme", "archivist"))
				h := workerHandlers(t, store, identityFor("acme"))
				rec := do(h, http.MethodDelete,
					"/agent/workers/archivist?rationale="+urlValue(why), "")
				if rec.Code != http.StatusNoContent {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite
			},
		},
		{
			name: "POST /agent/subscriptions",
			run: func(t *testing.T) agentdb.ConfigWrite {
				store := newFakeEventStore()
				h := newEventHandlers(t, store, identityFor("acme"))
				rec := do(h, http.MethodPost, "/agent/subscriptions",
					`{"event_type":"email.received","worker":"email-answerer","rationale":"`+why+`"}`)
				if rec.Code != http.StatusCreated {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite
			},
		},
		{
			name: "PUT /agent/subscriptions/{id}",
			run: func(t *testing.T) agentdb.ConfigWrite {
				store := newFakeEventStore()
				h := newEventHandlers(t, store, identityFor("acme"))
				sub := seedSubscription(t, h)
				rec := do(h, http.MethodPut, "/agent/subscriptions/"+sub.ID,
					`{"worker":"email-reviewer","rationale":"`+why+`"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite
			},
		},
		{
			name: "DELETE /agent/subscriptions/{id}",
			run: func(t *testing.T) agentdb.ConfigWrite {
				store := newFakeEventStore()
				h := newEventHandlers(t, store, identityFor("acme"))
				sub := seedSubscription(t, h)
				rec := do(h, http.MethodDelete,
					"/agent/subscriptions/"+sub.ID+"?rationale="+urlValue(why), "")
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite
			},
		},
		{
			name: "DELETE /agent/schedules/{id}",
			run: func(t *testing.T) agentdb.ConfigWrite {
				sch := agentdb.NewSchedule("acme", "tweet-author", "0 9 * * *", "")
				store := newFakeScheduleStore(sch)
				h := scheduleHandlers(t, store, nil)
				rec := httptest.NewRecorder()
				h.Schedule(rec, scheduleReq(http.MethodDelete,
					"/agent/schedules/"+sch.ID+"?rationale="+urlValue(why), sch.ID, ""))
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
				}
				return store.lastWrite
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cw := tc.run(t)
			if cw.Rationale != why {
				t.Fatalf("rationale not threaded into the config write: %+v", cw)
			}
			// A reason does not make the editor an actor: §15.2 still says a
			// human edit logs no acting worker and no acting session.
			if cw.Worker != "" || cw.Session != "" {
				t.Fatalf("an HTTP edit logs no actor (§15.2): %+v", cw)
			}
		})
	}
}

// A rationale of nothing but whitespace is an absent one, on every route: the
// changelog's "(no reason given)" must not be defeated by a stray space.
func TestHTTPRationaleIsTrimmedToEmpty(t *testing.T) {
	store := newFakeWorkerStore()
	h := workerHandlers(t, store, identityFor("acme"))
	if rec := do(h, http.MethodPut, "/agent/workers/email-answerer",
		`{"description":"answers email","rationale":"   "}`); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if store.lastWrite != (agentdb.ConfigWrite{}) {
		t.Fatalf("a blank rationale must record as absent, got %+v", store.lastWrite)
	}
}

// seedSubscription creates one subscription (with no rationale) so a case can
// edit or delete it and measure only its own write.
func seedSubscription(t *testing.T, h *Handlers) *agentdb.Subscription {
	t.Helper()
	rec := do(h, http.MethodPost, "/agent/subscriptions",
		`{"event_type":"email.received","worker":"email-answerer"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed status=%d body=%s", rec.Code, rec.Body)
	}
	var sub agentdb.Subscription
	decodeInto(t, rec, &sub)
	return &sub
}

// urlValue percent-encodes one query value.
func urlValue(s string) string { return url.QueryEscape(s) }

// The same property against the real store: the reason the operator typed is
// what ListConfigEvents hands back, on the routes that had no rationale before
// this item. Skipped without a live database.
func TestHTTPRationaleReachesTheConfigLog_LivePG(t *testing.T) {
	dsn := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(dsn)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	ctx := context.Background()
	project := "httpapi-why-" + t.Name()
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
		`{"description":"answers inbound email","rationale":"the inbox was drowning"}`); rec.Code != http.StatusOK {
		t.Fatalf("put worker: status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodDelete,
		"/agent/workers/email-answerer?rationale="+urlValue("superseded by the triage worker"),
		""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete worker: status=%d body=%s", rec.Code, rec.Body)
	}

	evs, err := store.ListConfigEvents(ctx, agentdb.ConfigEventQuery{Project: project})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("want two config events, got %d", len(evs))
	}
	// Newest first, so the delete leads.
	want := []struct {
		action    string
		rationale string
	}{
		{agentdb.ActionWorkerDelete, "superseded by the triage worker"},
		{agentdb.ActionWorkerCreate, "the inbox was drowning"},
	}
	for i, w := range want {
		if evs[i].Action != w.action || evs[i].Rationale != w.rationale {
			t.Fatalf("event %d: want %s/%q, got %s/%q",
				i, w.action, w.rationale, evs[i].Action, evs[i].Rationale)
		}
		if evs[i].ActorWorker != "" || evs[i].ActorSession != "" {
			t.Fatalf("an HTTP edit logs no actor (§15.2): %+v", evs[i])
		}
	}
}
