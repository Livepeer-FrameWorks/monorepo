package commodore

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// observedDeadline runs the interceptor and reports the deadline the invoker
// actually saw.
func observedDeadline(t *testing.T, timeout time.Duration, callerCtx context.Context) (time.Time, bool) {
	t.Helper()
	var seen context.Context
	err := timeoutInterceptor(timeout)(callerCtx, "/test.Method", nil, nil, nil,
		func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
			seen = ctx
			return nil
		})
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	return seen.Deadline()
}

// Calls dial with WaitForReady, so a caller with no deadline of its own — the
// PUSH_REWRITE ingest path uses context.Background() — would otherwise park
// indefinitely against an unreachable Commodore.
func TestTimeoutInterceptorBoundsDeadlinelessCallers(t *testing.T) {
	deadline, ok := observedDeadline(t, 30*time.Second, context.Background())
	if !ok {
		t.Fatal("no deadline applied to a caller that supplied none")
	}
	if remaining := time.Until(deadline); remaining > 30*time.Second || remaining < 25*time.Second {
		t.Fatalf("deadline %v away, want ~30s", remaining)
	}
}

// A caller asking for less time keeps its own, shorter deadline: the configured
// value is a ceiling, not an override.
func TestTimeoutInterceptorKeepsShorterCallerDeadline(t *testing.T) {
	callerCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	deadline, ok := observedDeadline(t, 30*time.Second, callerCtx)
	if !ok {
		t.Fatal("caller deadline lost")
	}
	if remaining := time.Until(deadline); remaining > 3*time.Second {
		t.Fatalf("deadline %v away, want the caller's ~2s", remaining)
	}
}

// A caller asking for more time is capped: without this, one caller with a
// generous deadline reintroduces the unbounded wait the timeout exists to stop.
func TestTimeoutInterceptorCapsLongerCallerDeadline(t *testing.T) {
	callerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	deadline, ok := observedDeadline(t, 5*time.Second, callerCtx)
	if !ok {
		t.Fatal("no deadline applied")
	}
	if remaining := time.Until(deadline); remaining > 6*time.Second {
		t.Fatalf("deadline %v away, want it capped at the configured ~5s", remaining)
	}
}

// Zero means "no client-side bound", leaving the caller's context untouched.
func TestTimeoutInterceptorDisabled(t *testing.T) {
	if _, ok := observedDeadline(t, 0, context.Background()); ok {
		t.Fatal("a deadline was applied despite no configured timeout")
	}
}
