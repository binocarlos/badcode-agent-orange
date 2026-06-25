package main

import "testing"

// envMap returns an env-lookup func backed by a map.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveBlobConfig_DefaultFS(t *testing.T) {
	cfg, err := resolveBlobConfig(envMap(nil), "/data")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.backend != "fs" || cfg.fsRoot != "/data/blobs" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestResolveBlobConfig_GCS(t *testing.T) {
	cfg, err := resolveBlobConfig(envMap(map[string]string{
		"AGENTKIT_BLOB_BACKEND": "gcs",
		"GCS_BUCKET":            "my-bucket",
		"GCS_PREFIX":            "prod",
	}), "/data")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.backend != "gcs" || cfg.bucket != "my-bucket" || cfg.prefix != "prod" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestResolveBlobConfig_GCSMissingBucket(t *testing.T) {
	_, err := resolveBlobConfig(envMap(map[string]string{"AGENTKIT_BLOB_BACKEND": "gcs"}), "/data")
	if err == nil {
		t.Fatal("expected error when GCS_BUCKET unset")
	}
}

func TestResolveBlobConfig_Unknown(t *testing.T) {
	if _, err := resolveBlobConfig(envMap(map[string]string{"AGENTKIT_BLOB_BACKEND": "s3"}), "/data"); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestResolveRegistryConfig_DefaultBlobarchive(t *testing.T) {
	cfg, err := resolveRegistryConfig(envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.backend != "blobarchive" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestResolveRegistryConfig_OCIFromGCPParts(t *testing.T) {
	cfg, err := resolveRegistryConfig(envMap(map[string]string{
		"AGENTKIT_REGISTRY_BACKEND": "ociregistry",
		"GCP_REGION":                "europe-west1",
		"GCP_PROJECT":               "webkit-servers",
		"GCP_AR_REPO":               "agent-orange",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := "europe-west1-docker.pkg.dev/webkit-servers/agent-orange"
	if cfg.registry != want {
		t.Fatalf("registry = %q, want %q", cfg.registry, want)
	}
	if cfg.auth != "gcp" {
		t.Fatalf("auth = %q, want gcp (default)", cfg.auth)
	}
}

func TestResolveRegistryConfig_OCIExplicitURLAndBasic(t *testing.T) {
	cfg, err := resolveRegistryConfig(envMap(map[string]string{
		"AGENTKIT_REGISTRY_BACKEND": "ociregistry",
		"OCI_REGISTRY":              "registry:5000/agentkit",
		"AGENTKIT_REGISTRY_AUTH":    "basic",
		"OCI_USERNAME":              "u",
		"OCI_PASSWORD":              "p",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.registry != "registry:5000/agentkit" || cfg.auth != "basic" || cfg.username != "u" || cfg.password != "p" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestResolveRegistryConfig_OCIMissingURL(t *testing.T) {
	_, err := resolveRegistryConfig(envMap(map[string]string{"AGENTKIT_REGISTRY_BACKEND": "ociregistry"}))
	if err == nil {
		t.Fatal("expected error when no registry URL resolvable")
	}
}

func TestResolveRegistryConfig_UnknownAuth(t *testing.T) {
	_, err := resolveRegistryConfig(envMap(map[string]string{
		"AGENTKIT_REGISTRY_BACKEND": "ociregistry",
		"OCI_REGISTRY":              "r",
		"AGENTKIT_REGISTRY_AUTH":    "kerberos",
	}))
	if err == nil {
		t.Fatal("expected error for unknown auth mode")
	}
}

func TestResolveRegistryConfig_UnknownBackend(t *testing.T) {
	if _, err := resolveRegistryConfig(envMap(map[string]string{"AGENTKIT_REGISTRY_BACKEND": "ecr"})); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}
