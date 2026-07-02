package agentdb

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A DSN that fails at parse time — no network, deterministic, fast.
const badDSN = "postgres://user:pw@localhost:not-a-port/db"

func TestOpen_BadDSNErrors(t *testing.T) {
	s, err := Open(badDSN)
	if err == nil {
		t.Fatalf("expected error for malformed DSN")
	}
	if s != nil {
		t.Fatalf("expected nil store on error, got %v", s)
	}
	if !strings.Contains(err.Error(), "agentdb: connect") {
		t.Fatalf("expected agentdb: connect error, got %v", err)
	}
}

// MustOpen is documented as "like Open but panics on error" — hosts must be
// able to recover() it (e.g. in test harnesses / supervisors).
func TestMustOpen_PanicsOnBadDSN(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected MustOpen to panic on bad DSN")
		}
		if !strings.Contains(fmt.Sprint(r), "agentdb.MustOpen") {
			t.Fatalf("panic value should name agentdb.MustOpen, got %v", r)
		}
	}()
	MustOpen(badDSN)
}

func TestStoreDB_ReturnsUnderlyingGorm(t *testing.T) {
	s := newTestStore(t)
	if s.DB() != s.gdb {
		t.Fatalf("DB() must return the underlying *gorm.DB")
	}
}

func TestJSONArray_MarshalUnmarshal(t *testing.T) {
	// nil marshals as the empty array, not null.
	b, err := json.Marshal(JSONArray(nil))
	if err != nil || string(b) != "[]" {
		t.Fatalf("nil JSONArray: got %q err=%v", b, err)
	}

	src := JSONArray(`[{"a":1}]`)
	b, err = json.Marshal(src)
	if err != nil || string(b) != `[{"a":1}]` {
		t.Fatalf("marshal: got %q err=%v", b, err)
	}

	var back JSONArray
	if err := json.Unmarshal([]byte(`[1,2,3]`), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(back) != "[1,2,3]" {
		t.Fatalf("unmarshal round-trip: got %q", back)
	}
}

func TestTableNames(t *testing.T) {
	tests := []struct {
		got, want string
	}{
		{Session{}.TableName(), "agent_sessions"},
		{Message{}.TableName(), "agent_messages"},
		{QueryEvents{}.TableName(), "agent_query_events"},
		{Artifact{}.TableName(), "agent_artifacts"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Fatalf("table name: want %q, got %q", tc.want, tc.got)
		}
	}
}
