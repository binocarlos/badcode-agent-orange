package auth

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// gcpUsername is the fixed username Google Artifact Registry (and GCR) expect
// when authenticating a Docker push/pull with an OAuth2 access token.
const gcpUsername = "oauth2accesstoken"

// gcpScope is the OAuth2 scope required to push/pull from Artifact Registry.
const gcpScope = "https://www.googleapis.com/auth/cloud-platform"

// GCP returns a Provider that authenticates to Google Artifact Registry using
// Application Default Credentials — workload identity on GCP, gcloud locally, or
// a service-account key via GOOGLE_APPLICATION_CREDENTIALS. The username is
// always "oauth2accesstoken"; the password is a short-lived access token. The
// underlying oauth2.TokenSource caches the token and refreshes it before expiry,
// so each Credentials call is cheap and always returns a valid token.
//
// Use with ociregistry.Config{Auth: prov, Registry: "<region>-docker.pkg.dev/<project>/<repo>"}.
func GCP(ctx context.Context) (Provider, error) {
	creds, err := google.FindDefaultCredentials(ctx, gcpScope)
	if err != nil {
		return nil, fmt.Errorf("auth: gcp ADC: %w", err)
	}
	return &gcpProvider{ts: creds.TokenSource}, nil
}

// gcpProvider turns an oauth2.TokenSource into registry credentials.
type gcpProvider struct{ ts oauth2.TokenSource }

func (g *gcpProvider) Credentials(context.Context) (Credentials, error) {
	tok, err := g.ts.Token()
	if err != nil {
		return Credentials{}, fmt.Errorf("auth: gcp token: %w", err)
	}
	return Credentials{Username: gcpUsername, Password: tok.AccessToken}, nil
}
