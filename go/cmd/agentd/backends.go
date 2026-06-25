package main

// backends.go — config-driven selection of the blob and image-registry backends.
//
// Defaults preserve the original local stack (filesystem blobs + blob-archive
// snapshots). Set the env vars below to run on Google Cloud:
//
//	AGENTKIT_BLOB_BACKEND=gcs  GCS_BUCKET=<bucket> [GCS_PREFIX=<prefix>]
//	AGENTKIT_REGISTRY_BACKEND=ociregistry
//	  GCP_REGION=<r> GCP_PROJECT=<p> GCP_AR_REPO=<repo>   (or OCI_REGISTRY=<url>)
//	  AGENTKIT_REGISTRY_AUTH=gcp                          (default; or basic)
//
// Config resolution (resolve*) is pure and unit-tested; the new* constructors do
// the network/docker I/O.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/filesblob"
	"github.com/binocarlos/badcode-agent-orange/extension/gcsblob"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
	"github.com/binocarlos/badcode-agent-orange/imageregistry/auth"
	"github.com/binocarlos/badcode-agent-orange/imageregistry/blobarchive"
	"github.com/binocarlos/badcode-agent-orange/imageregistry/ociregistry"
)

// ---- blob backend ------------------------------------------------------------

type blobConfig struct {
	backend string // "fs" | "gcs"
	fsRoot  string // fs
	bucket  string // gcs
	prefix  string // gcs (optional)
}

// resolveBlobConfig reads the blob-backend env into a validated config.
func resolveBlobConfig(env func(string) string, dataDir string) (blobConfig, error) {
	switch backend := getOr(env, "AGENTKIT_BLOB_BACKEND", "fs"); backend {
	case "fs":
		return blobConfig{backend: "fs", fsRoot: filepath.Join(dataDir, "blobs")}, nil
	case "gcs":
		bucket := env("GCS_BUCKET")
		if bucket == "" {
			return blobConfig{}, errors.New("AGENTKIT_BLOB_BACKEND=gcs requires GCS_BUCKET")
		}
		return blobConfig{backend: "gcs", bucket: bucket, prefix: env("GCS_PREFIX")}, nil
	default:
		return blobConfig{}, fmt.Errorf("unknown AGENTKIT_BLOB_BACKEND %q (want fs|gcs)", backend)
	}
}

// newBlobs constructs the BlobStore for cfg. The returned closeFn releases any
// backend client (a no-op for fs).
func newBlobs(ctx context.Context, cfg blobConfig) (blobs extension.BlobStore, closeFn func() error, err error) {
	switch cfg.backend {
	case "fs":
		if err := os.MkdirAll(cfg.fsRoot, 0o755); err != nil {
			return nil, nil, fmt.Errorf("agentd: blobs dir: %w", err)
		}
		return filesblob.NewBlobStore(cfg.fsRoot), func() error { return nil }, nil
	case "gcs":
		f, err := gcsblob.NewBlobStoreFactory(ctx, gcsblob.Config{Bucket: cfg.bucket, Prefix: cfg.prefix})
		if err != nil {
			return nil, nil, err
		}
		// Empty namespace → bytes rooted at the configured prefix, mirroring the
		// single shared blob root the fs backend uses for artifacts + snapshots.
		return f.Global(""), f.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown blob backend %q", cfg.backend)
	}
}

// ---- registry backend --------------------------------------------------------

type registryConfig struct {
	backend    string // "blobarchive" | "ociregistry"
	registry   string // ociregistry: full registry URL
	auth       string // ociregistry: "gcp" | "basic"
	username   string // ociregistry basic
	password   string // ociregistry basic
	alwaysPull bool
}

// resolveRegistryConfig reads the registry-backend env into a validated config.
func resolveRegistryConfig(env func(string) string) (registryConfig, error) {
	backend := getOr(env, "AGENTKIT_REGISTRY_BACKEND", "blobarchive")
	switch backend {
	case "blobarchive":
		return registryConfig{backend: "blobarchive"}, nil
	case "ociregistry":
		url, err := resolveOCIRegistryURL(env)
		if err != nil {
			return registryConfig{}, err
		}
		authMode := getOr(env, "AGENTKIT_REGISTRY_AUTH", "gcp")
		switch authMode {
		case "gcp":
			return registryConfig{backend: backend, registry: url, auth: "gcp", alwaysPull: env("AGENTKIT_REGISTRY_ALWAYS_PULL") == "true"}, nil
		case "basic":
			return registryConfig{backend: backend, registry: url, auth: "basic",
				username: env("OCI_USERNAME"), password: env("OCI_PASSWORD"),
				alwaysPull: env("AGENTKIT_REGISTRY_ALWAYS_PULL") == "true"}, nil
		default:
			return registryConfig{}, fmt.Errorf("unknown AGENTKIT_REGISTRY_AUTH %q (want gcp|basic)", authMode)
		}
	default:
		return registryConfig{}, fmt.Errorf("unknown AGENTKIT_REGISTRY_BACKEND %q (want blobarchive|ociregistry)", backend)
	}
}

// resolveOCIRegistryURL builds the registry URL from an explicit OCI_REGISTRY or
// from GCP Artifact Registry parts (GCP_REGION + GCP_PROJECT + GCP_AR_REPO).
func resolveOCIRegistryURL(env func(string) string) (string, error) {
	if u := env("OCI_REGISTRY"); u != "" {
		return u, nil
	}
	region, project, repo := env("GCP_REGION"), env("GCP_PROJECT"), env("GCP_AR_REPO")
	if region != "" && project != "" && repo != "" {
		return fmt.Sprintf("%s-docker.pkg.dev/%s/%s", region, project, repo), nil
	}
	return "", errors.New("AGENTKIT_REGISTRY_BACKEND=ociregistry requires OCI_REGISTRY or all of GCP_REGION+GCP_PROJECT+GCP_AR_REPO")
}

// newRegistry constructs the ImageRegistry for cfg. blobs is used only by the
// blob-archive backend.
func newRegistry(ctx context.Context, dockerHost string, blobs extension.BlobStore, cfg registryConfig) (imageregistry.ImageRegistry, error) {
	switch cfg.backend {
	case "blobarchive":
		return blobarchive.New(dockerHost, blobs)
	case "ociregistry":
		var prov auth.Provider
		switch cfg.auth {
		case "gcp":
			p, err := auth.GCP(ctx)
			if err != nil {
				return nil, err
			}
			prov = p
		case "basic":
			prov = auth.Static(cfg.username, cfg.password)
		default:
			return nil, fmt.Errorf("unknown registry auth %q", cfg.auth)
		}
		return ociregistry.New(ociregistry.Config{
			DockerHost: dockerHost,
			Registry:   cfg.registry,
			Auth:       prov,
			AlwaysPull: cfg.alwaysPull,
		})
	default:
		return nil, fmt.Errorf("unknown registry backend %q", cfg.backend)
	}
}

// getOr returns env(k) or d when empty.
func getOr(env func(string) string, k, d string) string {
	if v := env(k); v != "" {
		return v
	}
	return d
}
