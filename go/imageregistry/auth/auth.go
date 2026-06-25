// Package auth supplies registry credentials to the ociregistry adapter. It
// exists so that short-lived, refreshable tokens (Google Artifact Registry,
// AWS ECR, …) can be used in place of static basic-auth: the adapter asks the
// Provider for credentials on every push/pull rather than capturing them once.
//
// The default is Static (basic auth, the historical behaviour). GCP (gcp.go)
// returns OAuth2 access tokens via Application Default Credentials.
package auth

import "context"

// Credentials is a registry username/password pair. For token-based registries
// the token is carried in Password (e.g. GCP uses Username "oauth2accesstoken").
type Credentials struct {
	Username string
	Password string
}

// Provider supplies registry credentials, refreshing short-lived tokens as
// needed. Credentials may be called once per push/pull, so implementations
// should cache and only refresh near expiry. Must be safe for concurrent use.
type Provider interface {
	Credentials(ctx context.Context) (Credentials, error)
}

// Static returns a Provider that always returns the same credentials. Empty
// username and password yield anonymous access (the adapter encodes that as the
// "{}" registry-auth the Docker daemon requires for unauthenticated registries).
func Static(username, password string) Provider {
	return staticProvider{Credentials{Username: username, Password: password}}
}

type staticProvider struct{ c Credentials }

func (s staticProvider) Credentials(context.Context) (Credentials, error) { return s.c, nil }
