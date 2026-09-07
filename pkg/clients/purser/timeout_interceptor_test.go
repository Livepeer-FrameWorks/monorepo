package purser

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func observedTimeoutDeadline(t *testing.T, timeout time.Duration, callerCtx context.Context) (time.Time, bool) {
	t.Helper()
	var seen context.Context
	err := timeoutInterceptor(timeout)(callerCtx, "/test.Method", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			seen = ctx
			return nil
		})
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	return seen.Deadline()
}

func TestTimeoutInterceptorBoundsDeadlinelessPurserCalls(t *testing.T) {
	deadline, ok := observedTimeoutDeadline(t, 30*time.Second, context.Background())
	if !ok {
		t.Fatal("no deadline applied to a caller that supplied none")
	}
	if remaining := time.Until(deadline); remaining > 30*time.Second || remaining < 25*time.Second {
		t.Fatalf("deadline %v away, want ~30s", remaining)
	}
}

func TestTimeoutInterceptorKeepsShorterPurserDeadline(t *testing.T) {
	callerCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	deadline, ok := observedTimeoutDeadline(t, 30*time.Second, callerCtx)
	if !ok {
		t.Fatal("caller deadline lost")
	}
	if remaining := time.Until(deadline); remaining > 3*time.Second {
		t.Fatalf("deadline %v away, want the caller's ~2s", remaining)
	}
}

func TestTimeoutInterceptorCapsLongerPurserDeadline(t *testing.T) {
	callerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	deadline, ok := observedTimeoutDeadline(t, 5*time.Second, callerCtx)
	if !ok {
		t.Fatal("no deadline applied")
	}
	if remaining := time.Until(deadline); remaining > 6*time.Second || remaining < 4*time.Second {
		t.Fatalf("deadline %v away, want the configured ~5s ceiling", remaining)
	}
}
