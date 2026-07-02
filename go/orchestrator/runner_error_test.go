package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func newErrTestRunner(t *testing.T) (*Runner, *MemBoard) {
	t.Helper()
	board := NewMemBoard()
	if _, err := board.Append(context.Background(), SeedFragment("role-writer", "Be witty.")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return &Runner{
		Board:     board,
		Model:     &ScriptedModel{Default: "a draft"},
		Telemetry: NewTelemetry(),
	}, board
}

func TestRunScopeBoardFailure(t *testing.T) {
	r, _ := newErrTestRunner(t)
	r.Board = failBoard{err: errBoom}
	_, err := r.RunScope(context.Background(), Scope{Name: "w", Template: "{{input}}", Input: "x"})
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "current board") {
		t.Fatalf("board failure: err = %v", err)
	}
}

func TestRunScopeComposeFailureUnknownFragment(t *testing.T) {
	r, _ := newErrTestRunner(t)
	_, err := r.RunScope(context.Background(), Scope{Name: "w", Template: "{{fragment:ghost}}", Input: "x"})
	if err == nil || !strings.Contains(err.Error(), `unknown fragment "ghost"`) {
		t.Fatalf("compose failure: err = %v", err)
	}
}

func TestRunScopeModelFailure(t *testing.T) {
	r, _ := newErrTestRunner(t)
	r.Model = failModel{err: errBoom}
	_, err := r.RunScope(context.Background(), Scope{Name: "w", Template: "{{input}}", Input: "x"})
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "model") {
		t.Fatalf("model failure: err = %v", err)
	}
}

func TestRunScopeTelemetryFailure(t *testing.T) {
	r, _ := newErrTestRunner(t)
	r.Telemetry = failTelemetry{err: errBoom}
	_, err := r.RunScope(context.Background(), Scope{Name: "w", Template: "{{input}}", Input: "x"})
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "telemetry") {
		t.Fatalf("telemetry failure: err = %v", err)
	}
}
