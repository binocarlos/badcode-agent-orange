package auth

import (
	"context"
	"testing"

	"golang.org/x/oauth2"
)

func TestStatic(t *testing.T) {
	c, err := Static("user", "pass").Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "user" || c.Password != "pass" {
		t.Fatalf("got %+v", c)
	}
}

func TestStaticAnonymous(t *testing.T) {
	c, _ := Static("", "").Credentials(context.Background())
	if c.Username != "" || c.Password != "" {
		t.Fatalf("expected anonymous, got %+v", c)
	}
}

// TestGCPProviderCredentials drives the GCP provider with a static token source,
// verifying the oauth2accesstoken username and token-as-password plumbing without
// touching ADC or the network.
func TestGCPProviderCredentials(t *testing.T) {
	p := &gcpProvider{ts: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok-123"})}
	c, err := p.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != gcpUsername {
		t.Fatalf("username = %q, want %q", c.Username, gcpUsername)
	}
	if c.Password != "tok-123" {
		t.Fatalf("password = %q, want token", c.Password)
	}
}
