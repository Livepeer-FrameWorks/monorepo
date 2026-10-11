package clients

import (
	"context"
	"errors"
	"testing"

	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNormalizeCircuitBreakerErrorMakesUnaryOutageRetryable(t *testing.T) {
	err := normalizeCircuitBreakerError(circuitbreaker.ErrOpen, "quartermaster")
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("breaker-open code = %s, want Unavailable", status.Code(err))
	}
	original := errors.New("domain failure")
	if got := normalizeCircuitBreakerError(original, "quartermaster"); !errors.Is(got, original) {
		t.Fatalf("non-breaker error changed: %v", got)
	}
}

func TestGRPCUnaryPolicyForMethodIsolatesConfiguredMethod(t *testing.T) {
	defaultPolicy := grpcUnaryFailsafePolicy{name: "foghorn-cell-a"}
	authorityPolicy := grpcUnaryFailsafePolicy{name: "foghorn-cell-a-media-authority"}
	policies := map[string]grpcUnaryFailsafePolicy{
		"/foghorn.MediaAuthorityControlService/ApplyMediaAuthority": authorityPolicy,
	}

	if got := grpcUnaryPolicyForMethod(defaultPolicy, policies, "/foghorn.MediaAuthorityControlService/ApplyMediaAuthority").name; got != authorityPolicy.name {
		t.Fatalf("authority policy = %q, want %q", got, authorityPolicy.name)
	}
	if got := grpcUnaryPolicyForMethod(defaultPolicy, policies, "/foghorn.ViewerControlService/ResolveViewerEndpoint").name; got != defaultPolicy.name {
		t.Fatalf("default policy = %q, want %q", got, defaultPolicy.name)
	}
}

// A single-attempt call runs once and asks gRPC not to wait for a ready
// connection, overriding a connection default of WaitForReady(true); an
// unmarked call keeps the retry policy.
func TestFailsafeUnaryInterceptorSingleAttempt(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "connection refused")
	for _, single := range []bool{true, false} {
		interceptor := FailsafeUnaryInterceptor("single-attempt-test", nil)
		attempts := 0
		failFast := false
		invoker := func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, opts ...grpc.CallOption) error {
			attempts++
			failFast = false
			for _, opt := range opts {
				if option, ok := opt.(grpc.FailFastCallOption); ok {
					failFast = option.FailFast
				}
			}
			return unavailable
		}
		ctx := context.Background()
		if single {
			ctx = WithSingleAttempt(ctx)
		}
		err := interceptor(ctx, "/svc/Method", nil, nil, nil, invoker, grpc.WaitForReady(true))
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("single=%v: err=%v, want Unavailable", single, err)
		}
		if single && (attempts != 1 || !failFast) {
			t.Fatalf("single attempt: attempts=%d failFast=%v, want one fail-fast attempt", attempts, failFast)
		}
		if !single && (attempts <= 1 || failFast) {
			t.Fatalf("unmarked call: attempts=%d failFast=%v, want retries waiting for ready", attempts, failFast)
		}
	}
}
