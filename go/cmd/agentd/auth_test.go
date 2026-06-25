package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bayes-price/agentkit/extension/devclaims"
)

func TestJWTAuthMiddleware_ValidTokenSetsPrincipal(t *testing.T) {
	secret := []byte("test-secret")
	tok, err := devclaims.New(secret).Issue(context.Background(),
		extensionScope("alice@acme.com", "acme"), "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	var gotEmail, gotCustomer string
	h := jwtAuthMiddleware(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := identityFromRequest(r)
		gotEmail, gotCustomer = id.UserEmail, id.Customer
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotEmail != "alice@acme.com" || gotCustomer != "acme" {
		t.Fatalf("principal = (%q,%q), want (alice@acme.com,acme)", gotEmail, gotCustomer)
	}
}

func TestJWTAuthMiddleware_RejectsMissingAndBadToken(t *testing.T) {
	secret := []byte("test-secret")
	h := jwtAuthMiddleware(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct{ name, auth string }{
		{"missing", ""},
		{"garbage", "Bearer not-a-jwt"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.name, rr.Code)
		}
	}
}

func TestJWTAuthMiddleware_EmptySecretIsDevOpen(t *testing.T) {
	var gotCustomer string
	h := jwtAuthMiddleware(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := identityFromRequest(r)
		gotCustomer = id.Customer
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || gotCustomer == "" {
		t.Fatalf("dev-open should pass with a default principal; status=%d customer=%q", rr.Code, gotCustomer)
	}
}
