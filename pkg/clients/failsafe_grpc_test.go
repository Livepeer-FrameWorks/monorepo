package clients

import (
	"errors"
	"testing"

	"github.com/failsafe-go/failsafe-go/circuitbreaker"
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
