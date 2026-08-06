package main

import (
	"errors"
	"strings"
	"testing"
)

// The whole point of RD22's code half is that the raw driver error names
// neither the compose service nor the once-initialised volume. These tests pin
// that both are named, and that gorm's own text still comes through.
func TestDatabaseConnectErrorNamesTheTrap(t *testing.T) {
	const dsn = "postgres://agentorange:hunter2@postgres:5432/agentorange?sslmode=disable"

	cases := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "auth failure points at the stale volume first",
			err:  errors.New(`failed to connect to \x60host=postgres user=agentorange\x60: password authentication failed for user "agentorange"`),
			want: []string{
				"docker compose down -v",
				"POSTGRES_PASSWORD",
				"pg-data",
				"password authentication failed",
			},
		},
		{
			name: "dial failure points at the postgres service",
			err:  errors.New("dial tcp 172.18.0.2:5432: connect: connection refused"),
			want: []string{
				"postgres",
				"docker compose ps postgres",
				"connection refused",
				"docker compose down -v",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := databaseConnectError(dsn, tc.err)
			if got == nil {
				t.Fatal("databaseConnectError returned nil for a non-nil error")
			}
			msg := got.Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("message does not mention %q:\n%s", want, msg)
				}
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("wrapped error is not unwrappable to the original")
			}
			if strings.Contains(msg, "hunter2") {
				t.Errorf("password leaked into the boot error:\n%s", msg)
			}
		})
	}
}

func TestDatabaseConnectErrorPassesNilThrough(t *testing.T) {
	if err := databaseConnectError("postgres://u:p@h:5432/d", nil); err != nil {
		t.Fatalf("want nil for a successful open, got %v", err)
	}
}

func TestRedactDBURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"postgres://agentorange:hunter2@postgres:5432/agentorange?sslmode=disable",
			"postgres://agentorange:xxxxx@postgres:5432/agentorange?sslmode=disable"},
		{"postgres://agentorange@postgres:5432/agentorange",
			"postgres://agentorange@postgres:5432/agentorange"},
		{"not a url at all", "<unparseable>"},
	}
	for _, tc := range cases {
		if got := redactDBURL(tc.in); got != tc.want {
			t.Errorf("redactDBURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
