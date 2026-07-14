package devclaims

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/golang-jwt/jwt/v5"
)

// parse is a test helper that parses a JWT signed with secret and returns its claims.
func parse(t *testing.T, tok string, secret []byte) jwt.MapClaims {
	t.Helper()
	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return secret, nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("parse err=%v valid=%v", err, parsed.Valid)
	}
	return parsed.Claims.(jwt.MapClaims)
}

func TestIssueProducesVerifiableToken(t *testing.T) {
	iss := New([]byte("dev-secret"))
	tok, err := iss.Issue(context.Background(), extension.ContextScope{Customer: "acme", Job: "j1", UserEmail: "u@x.y"}, "s1")
	if err != nil || tok == "" {
		t.Fatalf("issue err=%v tok=%q", err, tok)
	}
	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return []byte("dev-secret"), nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("parse err=%v valid=%v", err, parsed.Valid)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["sid"] != "s1" || claims["customer"] != "acme" {
		t.Fatalf("claims=%v", claims)
	}
	if claims["job"] != "j1" || claims["email"] != "u@x.y" {
		t.Fatalf("claims=%v", claims)
	}
	if exp, _ := claims["exp"].(float64); exp <= float64(time.Now().Unix()) {
		t.Fatalf("exp not in future: %v", claims["exp"])
	}
}

// TestNewWithTTL verifies the caller-chosen TTL lands in the exp claim.
func TestNewWithTTL(t *testing.T) {
	secret := []byte("dev-secret")
	iss := NewWithTTL(secret, 12*time.Hour)
	tok, err := iss.Issue(context.Background(), extension.ContextScope{Customer: "acme", UserEmail: "u@x.y"}, "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims := parse(t, tok, secret)
	exp, _ := claims["exp"].(float64)
	want := time.Now().Add(12 * time.Hour).Unix()
	if int64(exp) < want-60 || int64(exp) > want+60 {
		t.Fatalf("exp=%v, want ~%v (12h TTL)", int64(exp), want)
	}
}

// TestIssueClaimContents verifies every claim field is present and correct.
func TestIssueClaimContents(t *testing.T) {
	secret := []byte("test-secret-xyz")
	iss := New(secret)
	scope := extension.ContextScope{Customer: "contoso", Job: "wave2", UserEmail: "bob@contoso.com"}
	sessionID := "sess-abc-123"

	tok, err := iss.Issue(context.Background(), scope, sessionID)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if tok == "" {
		t.Fatal("Issue returned empty token")
	}

	claims := parse(t, tok, secret)

	// Verify each claim individually for clear diagnostics.
	if got := claims["sid"]; got != sessionID {
		t.Errorf("sid = %v, want %q", got, sessionID)
	}
	if got := claims["customer"]; got != scope.Customer {
		t.Errorf("customer = %v, want %q", got, scope.Customer)
	}
	if got := claims["job"]; got != scope.Job {
		t.Errorf("job = %v, want %q", got, scope.Job)
	}
	if got := claims["email"]; got != scope.UserEmail {
		t.Errorf("email = %v, want %q", got, scope.UserEmail)
	}
}

// TestIssueExpiry verifies the JWT expiry is roughly 1 hour in the future.
func TestIssueExpiry(t *testing.T) {
	secret := []byte("exp-secret")
	iss := New(secret)

	before := time.Now()
	tok, err := iss.Issue(context.Background(), extension.ContextScope{
		Customer: "acme", Job: "j1", UserEmail: "u@x.y",
	}, "s-exp")
	after := time.Now()
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	claims := parse(t, tok, secret)

	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("exp claim not a number: %T %v", claims["exp"], claims["exp"])
	}
	iat, ok := claims["iat"].(float64)
	if !ok {
		t.Fatalf("iat claim not a number: %T %v", claims["iat"], claims["iat"])
	}

	// exp must be strictly after now.
	if int64(exp) <= after.Unix() {
		t.Errorf("exp=%v is not in the future (now=%v)", exp, after.Unix())
	}

	// iat must be within the window of the Issue call.
	if int64(iat) < before.Unix() || int64(iat) > after.Unix()+1 {
		t.Errorf("iat=%v not in expected window [%v, %v]", iat, before.Unix(), after.Unix())
	}

	// TTL should be close to 1 hour (within 5 seconds tolerance for slow machines).
	ttl := int64(exp) - int64(iat)
	if ttl < int64(time.Hour/time.Second)-5 || ttl > int64(time.Hour/time.Second)+5 {
		t.Errorf("TTL = %ds, want ~3600s", ttl)
	}
}

// TestIssueEmptyScope verifies the issuer handles zero-value scope fields
// without error. The resulting claims will contain empty strings, which is
// correct for a dev-only issuer.
func TestIssueEmptyScope(t *testing.T) {
	secret := []byte("empty-scope-secret")
	iss := New(secret)

	tok, err := iss.Issue(context.Background(), extension.ContextScope{}, "sess-empty")
	if err != nil {
		t.Fatalf("Issue with empty scope: %v", err)
	}
	if tok == "" {
		t.Fatal("Issue with empty scope returned empty token")
	}

	claims := parse(t, tok, secret)

	// All scope fields should be empty strings.
	for _, field := range []string{"customer", "job", "email"} {
		if v, _ := claims[field].(string); v != "" {
			t.Errorf("empty-scope claim %q = %q, want empty string", field, v)
		}
	}
	// Session ID should still be set.
	if claims["sid"] != "sess-empty" {
		t.Errorf("sid = %v, want sess-empty", claims["sid"])
	}
}

// TestIssueDifferentSecrets verifies that a token signed by one secret cannot
// be verified by a different secret.
func TestIssueDifferentSecrets(t *testing.T) {
	iss := New([]byte("secret-A"))
	tok, err := iss.Issue(context.Background(), extension.ContextScope{
		Customer: "c", Job: "j", UserEmail: "e@e.e",
	}, "s1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Attempt to verify with a different secret — must fail.
	_, err = jwt.Parse(tok, func(*jwt.Token) (any, error) { return []byte("secret-B"), nil })
	if err == nil {
		t.Fatal("token signed with secret-A should NOT be verifiable with secret-B")
	}
}

// TestIssueScopeMappingMultipleSessions verifies that different session IDs
// produce distinct tokens with the correct sid claim each time.
func TestIssueScopeMappingMultipleSessions(t *testing.T) {
	secret := []byte("multi-sess-secret")
	iss := New(secret)
	scope := extension.ContextScope{Customer: "acme", Job: "j1", UserEmail: "u@acme.com"}

	sessions := []string{"sess-1", "sess-2", "sess-3"}
	seen := map[string]bool{}
	for _, sid := range sessions {
		tok, err := iss.Issue(context.Background(), scope, sid)
		if err != nil {
			t.Fatalf("Issue sid=%q: %v", sid, err)
		}
		if seen[tok] {
			t.Errorf("duplicate token for sid=%q", sid)
		}
		seen[tok] = true

		claims := parse(t, tok, secret)
		if claims["sid"] != sid {
			t.Errorf("sid claim = %v, want %q", claims["sid"], sid)
		}
	}
}

// TestIssueTokenIsHS256 verifies the signing algorithm header.
func TestIssueTokenIsHS256(t *testing.T) {
	iss := New([]byte("alg-secret"))
	tok, err := iss.Issue(context.Background(), extension.ContextScope{
		Customer: "c", Job: "j", UserEmail: "e@e.com",
	}, "s-alg")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The header segment is the first base64 part of the token.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3-part JWT, got %d parts", len(parts))
	}

	// Parse without verification just to read the header.
	parser := jwt.NewParser()
	parsed, _, err := parser.ParseUnverified(tok, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("ParseUnverified: %v", err)
	}
	if parsed.Method.Alg() != "HS256" {
		t.Errorf("alg = %q, want HS256", parsed.Method.Alg())
	}
}
