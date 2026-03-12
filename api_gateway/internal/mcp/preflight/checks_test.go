package preflight

import (
	"errors"
	"fmt"
	"testing"
)

func TestPreflightError_Error(t *testing.T) {
	pfe := &PreflightError{
		Blocker: Blocker{
			Code:    "INSUFFICIENT_BALANCE",
			Message: "Balance is 0 cents. Top up required.",
		},
	}
	if pfe.Error() != "Balance is 0 cents. Top up required." {
		t.Fatalf("unexpected: %q", pfe.Error())
	}
}

func TestIsPreflightError_Match(t *testing.T) {
	pfe := &PreflightError{
		Blocker: Blocker{Code: "TEST", Message: "test error"},
	}
	got, ok := IsPreflightError(pfe)
	if !ok {
		t.Fatal("expected match")
	}
	if got.Blocker.Code != "TEST" {
		t.Fatalf("code: got %q, want %q", got.Blocker.Code, "TEST")
	}
}

func TestIsPreflightError_NoMatch(t *testing.T) {
	got, ok := IsPreflightError(fmt.Errorf("other error"))
	if ok {
		t.Fatal("expected no match")
	}
	if got != nil {
		t.Fatal("expected nil")
	}
}

func TestIsPreflightError_Wrapped(t *testing.T) {
	pfe := &PreflightError{
		Blocker: Blocker{Code: "WRAPPED", Message: "inner"},
	}
	wrapped := fmt.Errorf("outer: %w", pfe)

	got, ok := IsPreflightError(wrapped)
	if !ok {
		t.Fatal("expected match through wrapping")
	}
	if got.Blocker.Code != "WRAPPED" {
		t.Fatalf("code: got %q, want %q", got.Blocker.Code, "WRAPPED")
	}

	// Verify it's the same error via errors.Is comparison
	if !errors.As(wrapped, &got) {
		t.Fatal("errors.As should find PreflightError in wrapped chain")
	}
}
