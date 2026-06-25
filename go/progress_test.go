// agent-library/go/progress_test.go
package agentkit

import (
	"sync"
	"testing"
	"time"

	"github.com/bayes-price/agentkit/imageregistry"
)

func TestProgressStore_PhaseBytesFinish(t *testing.T) {
	s := newProgressStore()
	s.begin("sid", "snapshot")
	s.phase("sid", "uploading")
	s.bytes("sid", 45, 100, []LayerProg{{ID: "a", Current: 45, Total: 100, Status: "Pushing"}})
	got, ok := s.get("sid")
	if !ok {
		t.Fatal("expected progress present")
	}
	if got.Op != "snapshot" || got.Phase != "uploading" || got.BytesDone != 45 || got.BytesTotal != 100 {
		t.Fatalf("unexpected progress: %+v", got)
	}
	s.finish("sid", "")
	got, _ = s.get("sid")
	if got.Phase != "done" {
		t.Fatalf("expected phase done, got %q", got.Phase)
	}
}

func TestProgressStore_FinishWithErrorMarksFailed(t *testing.T) {
	s := newProgressStore()
	s.begin("sid", "restore")
	s.finish("sid", "pull denied")
	got, _ := s.get("sid")
	if got.Phase != "failed" || got.Err != "pull denied" {
		t.Fatalf("expected failed/err, got %+v", got)
	}
}

func TestProgressStore_TTLPurgesAfterDone(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newProgressStore()
	s.now = func() time.Time { return now }
	s.ttl = 10 * time.Second
	s.begin("sid", "snapshot")
	s.finish("sid", "")
	if _, ok := s.get("sid"); !ok {
		t.Fatal("expected progress still present immediately after finish")
	}
	now = now.Add(11 * time.Second)
	if _, ok := s.get("sid"); ok {
		t.Fatal("expected progress purged after TTL")
	}
}

func TestProgressStore_ConcurrentWrites(t *testing.T) {
	s := newProgressStore()
	s.begin("sid", "snapshot")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.bytes("sid", int64(n), 100, nil)
			_, _ = s.get("sid")
		}(i)
	}
	wg.Wait()
}

func TestSessionProgressSink_WritesBytesToStore(t *testing.T) {
	s := newProgressStore()
	s.begin("sid", "snapshot")
	sink := sessionProgressSink{store: s, sid: "sid"}
	sink.Bytes(80, 100, []imageregistry.LayerProgress{{ID: "a", Current: 80, Total: 100, Status: "Pushing"}})
	got, _ := s.get("sid")
	if got.BytesDone != 80 || got.BytesTotal != 100 || len(got.Layers) != 1 || got.Layers[0].ID != "a" {
		t.Fatalf("sink did not write through: %+v", got)
	}
}
