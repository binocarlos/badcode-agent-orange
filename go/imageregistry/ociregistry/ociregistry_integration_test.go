//go:build docker

package ociregistry

// Integration test for ociregistry (requires a real Docker daemon + registry).
//
// Run with:   go test -tags docker ./imageregistry/ociregistry/...
//
// Required env vars:
//   DOCKER_HOST      — Docker daemon address (default socket if unset)
//   OCIREGISTRY_URL  — Registry prefix e.g. "localhost:5000/agentkit" (required; test skips if unset)
//   OCIREGISTRY_USER — Registry username (optional, empty = unauthenticated)
//   OCIREGISTRY_PASS — Registry password (optional)
//
// The test pulls a tiny base image (hello-world), persists it (commit+push),
// then materializes (pull) and confirms the ref is returned. Uses hello-world
// as the "container" arg to ContainerCommit — in real usage this would be a
// running container ID. This validates the full registry round-trip.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

func TestIntegrationPersistMaterialize(t *testing.T) {
	registryURL := os.Getenv("OCIREGISTRY_URL")
	if registryURL == "" {
		t.Skip("OCIREGISTRY_URL not set — skipping integration test")
	}

	cfg := Config{
		DockerHost: os.Getenv("DOCKER_HOST"),
		Registry:   registryURL,
		Username:   os.Getenv("OCIREGISTRY_USER"),
		Password:   os.Getenv("OCIREGISTRY_PASS"),
	}

	reg, err := New(cfg)
	if err != nil {
		t.Skipf("could not create ociregistry (daemon unavailable?): %v", err)
	}

	ctx := context.Background()
	const baseImage = "hello-world:latest"

	if err := reg.EnsurePresent(ctx, execenv.ImageRef(baseImage)); err != nil {
		t.Fatalf("EnsurePresent(%s): %v", baseImage, err)
	}

	h, err := reg.Persist(ctx, execenv.ImageRef(baseImage), imageregistry.PersistOptions{
		SessionID: "integration-test-sess-1",
	})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if h.Kind != HandleKind {
		t.Fatalf("handle kind = %q, want %q", h.Kind, HandleKind)
	}
	t.Logf("Persisted handle: %+v", h)

	ref, err := reg.Materialize(ctx, h)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if string(ref) != h.Ref {
		t.Fatalf("Materialize ref = %q, want %q", ref, h.Ref)
	}
	t.Logf("Materialize returned: %q", ref)

	if err := reg.Remove(ctx, h); err != nil {
		t.Logf("Remove (non-fatal): %v", err)
	}
}

// TestIntegrationOnlyDiffLayerPushed asserts that when a derived image is persisted via
// Persist (tag + push), the registry stores only ONE layer that is not shared with the base
// image — i.e. exactly the diff layer. This proves that layer dedup is working end-to-end:
// the shared base layers are not re-uploaded because they already exist in the registry.
//
// Prerequisites:
//   - DinD daemon at DOCKER_HOST (default: local socket)
//   - Local registry at OCIREGISTRY_URL (e.g. localhost:5000/agentkit)
//   - alpine:3.20 reachable from the DinD daemon (pulled or available)
func TestIntegrationOnlyDiffLayerPushed(t *testing.T) {
	registryURL := os.Getenv("OCIREGISTRY_URL") // e.g. localhost:5000/agentkit
	if registryURL == "" {
		t.Skip("OCIREGISTRY_URL not set — skipping integration test")
	}
	dockerHost := os.Getenv("DOCKER_HOST")

	cli, err := client.NewClientWithOpts(clientOptsFromEnv(dockerHost)...)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer cli.Close()
	ctx := context.Background()

	// 1. Pull a small base and push it to the local registry so its layers exist there.
	const base = "alpine:3.20"
	pullDrain(t, cli, ctx, base)
	baseRemote := registryURL + "/diff-base:latest"
	if err := cli.ImageTag(ctx, base, baseRemote); err != nil {
		t.Fatalf("tag base: %v", err)
	}
	pushDrain(t, cli, ctx, baseRemote)

	// 2. Create a container from base, write a tiny file, commit -> a new top layer.
	derivedID := commitWithTinyChange(t, cli, ctx, base)

	// 3. Persist (tag + push) the derived image via the adapter under test.
	reg := mustNewReg(t, dockerHost, registryURL)
	if _, err := reg.Persist(ctx, execenv.ImageRef(derivedID), imageregistry.PersistOptions{SessionID: "diff-sess"}); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// 4. Assert the registry stored the derived manifest, and its non-shared layer
	//    count is exactly 1 (only the diff layer is unique to the derived image).
	uniqueLayers := layersUniqueToDerived(t, registryURL, "diff-sess", "diff-base")
	if uniqueLayers != 1 {
		t.Fatalf("derived image has %d layers not shared with base; want 1 (the diff)", uniqueLayers)
	}
}

// clientOptsFromEnv returns docker client options based on the given DOCKER_HOST value.
func clientOptsFromEnv(dockerHost string) []client.Opt {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if dockerHost != "" {
		opts = append(opts, client.WithHost(dockerHost))
	} else {
		opts = append(opts, client.FromEnv)
	}
	return opts
}

// pullDrain pulls an image and drains/closes the response stream.
func pullDrain(t *testing.T, cli *client.Client, ctx context.Context, ref string) {
	t.Helper()
	rc, err := cli.ImagePull(ctx, ref, dockertypes.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pull %s: %v", ref, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatalf("drain pull %s: %v", ref, err)
	}
}

// pushDrain pushes an image and drains/closes the response stream.
func pushDrain(t *testing.T, cli *client.Client, ctx context.Context, ref string) {
	t.Helper()
	rc, err := cli.ImagePush(ctx, ref, dockertypes.ImagePushOptions{RegistryAuth: encodeRegistryAuth("", "")})
	if err != nil {
		t.Fatalf("push %s: %v", ref, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatalf("drain push %s: %v", ref, err)
	}
}

// commitWithTinyChange creates a container from base, runs a command that writes a tiny
// file (adding a new layer), waits for it to exit, commits the container, and returns
// the resulting image ID.
func commitWithTinyChange(t *testing.T, cli *client.Client, ctx context.Context, base string) string {
	t.Helper()

	// Create a container that writes a tiny marker file, then exits.
	resp, err := cli.ContainerCreate(ctx,
		&dockercontainer.Config{
			Image: base,
			Cmd:   []string{"sh", "-c", "echo diff-layer-marker > /diff-marker.txt"},
		},
		nil, nil, nil,
		"ociregistry-diff-test",
	)
	if err != nil {
		// Name collision — remove and retry.
		_ = cli.ContainerRemove(ctx, "ociregistry-diff-test", dockertypes.ContainerRemoveOptions{Force: true})
		resp, err = cli.ContainerCreate(ctx,
			&dockercontainer.Config{
				Image: base,
				Cmd:   []string{"sh", "-c", "echo diff-layer-marker > /diff-marker.txt"},
			},
			nil, nil, nil,
			"ociregistry-diff-test",
		)
		if err != nil {
			t.Fatalf("ContainerCreate: %v", err)
		}
	}
	containerID := resp.ID
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), containerID, dockertypes.ContainerRemoveOptions{Force: true})
	})

	if err := cli.ContainerStart(ctx, containerID, dockertypes.ContainerStartOptions{}); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}

	// Wait for the container to finish executing.
	statusCh, errCh := cli.ContainerWait(ctx, containerID, dockercontainer.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ContainerWait: %v", err)
		}
	case <-statusCh:
	}

	// Commit the stopped container -> new image with one extra layer.
	commitResp, err := cli.ContainerCommit(ctx, containerID, dockertypes.ContainerCommitOptions{})
	if err != nil {
		t.Fatalf("ContainerCommit: %v", err)
	}
	return commitResp.ID
}

// mustNewReg creates an ociregistry.Registry or fails the test.
func mustNewReg(t *testing.T, dockerHost, registryURL string) *Registry {
	t.Helper()
	reg, err := New(Config{
		DockerHost: dockerHost,
		Registry:   registryURL,
		Username:   os.Getenv("OCIREGISTRY_USER"),
		Password:   os.Getenv("OCIREGISTRY_PASS"),
	})
	if err != nil {
		t.Fatalf("New registry: %v", err)
	}
	return reg
}

// registryManifestV2 is the minimal structure of a Docker Image Manifest v2 schema 2.
type registryManifestV2 struct {
	Layers []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
}

// fetchManifestDigests returns the set of layer digests for a given repo+tag from the registry.
// The registry host is extracted from registryURL (everything up to the first '/').
func fetchManifestDigests(t *testing.T, registryURL, repo, tag string) map[string]struct{} {
	t.Helper()

	// Extract the registry host (e.g. "localhost:5000") from the registry URL prefix.
	registryHost := strings.SplitN(registryURL, "/", 2)[0]

	// The repo path is the remainder of registryURL after the host, plus "/" + repo.
	// e.g. registryURL = "localhost:5000/agentkit", repo = "diff-sess" → name = "agentkit/diff-sess"
	registryPrefix := ""
	if idx := strings.Index(registryURL, "/"); idx >= 0 {
		registryPrefix = registryURL[idx+1:]
	}
	name := repo
	if registryPrefix != "" {
		name = fmt.Sprintf("%s/%s", registryPrefix, sanitizeName(repo))
	}

	url := fmt.Sprintf("http://%s/v2/%s/manifests/%s", registryHost, name, tag)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build manifest request: %v", err)
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("manifest GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("manifest GET %s: status %d body=%s", url, resp.StatusCode, body)
	}

	var m registryManifestV2
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	digests := make(map[string]struct{}, len(m.Layers))
	for _, l := range m.Layers {
		digests[l.Digest] = struct{}{}
	}
	return digests
}

// layersUniqueToDerived returns the count of layers in the derived image (derivedRepo)
// that are not present in the base image (baseRepo). Both are queried from registryURL
// at tag "latest". A count of 1 proves that only the diff layer was uploaded.
func layersUniqueToDerived(t *testing.T, registryURL, derivedRepo, baseRepo string) int {
	t.Helper()
	baseLayers := fetchManifestDigests(t, registryURL, baseRepo, "latest")
	derivedLayers := fetchManifestDigests(t, registryURL, derivedRepo, "latest")

	unique := 0
	for digest := range derivedLayers {
		if _, inBase := baseLayers[digest]; !inBase {
			unique++
		}
	}
	return unique
}
