//go:build gcs

// Integration tests for the GCS BlobStore. They run only with `-tags gcs` and
// only when GCS_TEST_BUCKET is set. Point them at:
//
//   - a real bucket via ADC (GOOGLE_APPLICATION_CREDENTIALS / gcloud / WI), or
//
//   - a fake server, e.g. fsouza/fake-gcs-server, via STORAGE_EMULATOR_HOST.
//
//     docker run -p 4443:4443 fsouza/fake-gcs-server -scheme http
//     STORAGE_EMULATOR_HOST=localhost:4443 GCS_TEST_BUCKET=test-bucket \
//     go test -tags gcs ./extension/gcsblob/...
//
// (fake-gcs-server auto-creates buckets on first write.)
package gcsblob

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"google.golang.org/api/option"
)

func newTestFactory(t *testing.T) *BlobStoreFactory {
	t.Helper()
	bucket := os.Getenv("GCS_TEST_BUCKET")
	if bucket == "" {
		t.Skip("GCS_TEST_BUCKET not set — skipping GCS integration test")
	}
	var opts []option.ClientOption
	if os.Getenv("STORAGE_EMULATOR_HOST") != "" {
		// The emulator needs no credentials; ADC would fail without them.
		opts = append(opts, option.WithoutAuthentication())
	}
	f, err := NewBlobStoreFactory(context.Background(), Config{Bucket: bucket, Prefix: "agent-orange-test"}, opts...)
	if err != nil {
		t.Fatalf("NewBlobStoreFactory: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestGCSRoundTrip(t *testing.T) {
	f := newTestFactory(t)
	ctx := context.Background()
	bs, err := f.ForSession(ctx, "sess-rt")
	if err != nil {
		t.Fatal(err)
	}

	key := "a/b.txt"
	if err := bs.Write(ctx, key, bytes.NewBufferString("hello")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bs.Delete(ctx, key) })

	ok, err := bs.Exists(ctx, key)
	if err != nil || !ok {
		t.Fatalf("exists=%v err=%v", ok, err)
	}
	rc, err := bs.Read(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}

	if err := bs.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if ok, _ := bs.Exists(ctx, key); ok {
		t.Fatal("expected deleted")
	}
}

func TestGCSExistsNotFound(t *testing.T) {
	f := newTestFactory(t)
	ctx := context.Background()
	bs := f.Global("misc")
	ok, err := bs.Exists(ctx, "nope.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected not found")
	}
}

func TestGCSDeleteIdempotent(t *testing.T) {
	f := newTestFactory(t)
	ctx := context.Background()
	bs := f.Global("misc")
	if err := bs.Delete(ctx, "never-existed.txt"); err != nil {
		t.Fatalf("delete of missing object should be nil, got %v", err)
	}
}

func TestGCSListStoreRelative(t *testing.T) {
	f := newTestFactory(t)
	ctx := context.Background()
	bs := f.Global("list-ns")

	for _, k := range []string{"prefix/a.txt", "prefix/b.txt", "other/c.txt"} {
		if err := bs.Write(ctx, k, bytes.NewBufferString("x")); err != nil {
			t.Fatal(err)
		}
		k := k
		t.Cleanup(func() { _ = bs.Delete(ctx, k) })
	}

	keys, err := bs.List(ctx, "prefix/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys under prefix/, got %d: %v", len(keys), keys)
	}
	// List keys must be store-relative (round-trippable through Read).
	for _, k := range keys {
		if ok, err := bs.Exists(ctx, k); err != nil || !ok {
			t.Fatalf("listed key %q not readable back: ok=%v err=%v", k, ok, err)
		}
	}
}

func TestGCSSessionIsolation(t *testing.T) {
	f := newTestFactory(t)
	ctx := context.Background()
	a, err := f.ForSession(ctx, "iso-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.ForSession(ctx, "iso-b")
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Write(ctx, "f.txt", bytes.NewBufferString("a")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Delete(ctx, "f.txt") })

	if ok, _ := b.Exists(ctx, "f.txt"); ok {
		t.Fatal("session b must not see session a's blob")
	}
}
