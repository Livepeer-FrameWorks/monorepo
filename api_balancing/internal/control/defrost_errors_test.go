package control

import "testing"

func TestDefrostingError(t *testing.T) {
	var nilErr *DefrostingError
	if nilErr.Error() != "defrosting" {
		t.Fatalf("expected default nil error message, got %q", nilErr.Error())
	}

	withMessage := &DefrostingError{RetryAfterSeconds: 30, Message: "warming"}
	if withMessage.Error() != "warming" {
		t.Fatalf("expected message override, got %q", withMessage.Error())
	}

	withRetry := &DefrostingError{RetryAfterSeconds: 45}
	if withRetry.Error() != "defrosting (retry after 45s)" {
		t.Fatalf("unexpected retry message: %q", withRetry.Error())
	}
}

func TestNewDefrostingError(t *testing.T) {
	err := NewDefrostingError(15, "soon")
	if err.RetryAfterSeconds != 15 {
		t.Fatalf("expected retry 15, got %d", err.RetryAfterSeconds)
	}
	if err.Message != "soon" {
		t.Fatalf("expected message soon, got %q", err.Message)
	}
}
