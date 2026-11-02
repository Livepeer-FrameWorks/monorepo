package resolvers

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func typedRefusal(t *testing.T, reason, message string) error {
	t.Helper()
	st, err := status.New(codes.FailedPrecondition, message).WithDetails(&errdetails.ErrorInfo{
		Reason: reason, Domain: billingErrorDomain, Metadata: map[string]string{"public_message": message},
	})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

// The dashboard and MCP branch on the refusal reason; the message is shown
// verbatim, so it must be Purser's tenant text and not the gRPC error string.
func TestChangeBillingTierPresentsTypedRefusals(t *testing.T) {
	message := "Your plan is arranged by FrameWorks. Contact us to change it before the arrangement ends."
	resolver := purserResolver(&clientstest.FakePurser{
		ChangeBillingTierFn: func(context.Context, string, string) (*purserpb.ChangeBillingTierResponse, error) {
			return nil, typedRefusal(t, "OUTSTANDING_GRANT", message)
		},
	})
	res, err := resolver.DoChangeBillingTier(clientstest.AuthedCtx("t1"), "tier-pro")
	if err != nil {
		t.Fatal(err)
	}
	refusal, ok := res.(*model.ValidationError)
	if !ok || refusal.Code == nil || *refusal.Code != "OUTSTANDING_GRANT" || refusal.Message != message {
		t.Fatalf("refusal = %+v, want OUTSTANDING_GRANT with the tenant message", res)
	}
}

func TestBillingMutationErrorsNeverShowTransportText(t *testing.T) {
	for _, err := range []error{
		status.Error(codes.Internal, "commit scheduled downgrade: pq: deadlock detected"),
		status.Error(codes.Unavailable, "connection refused"),
	} {
		got := billingMutationError(err, "Could not change the plan", "TIER_CHANGE_FAILED", "tier_id")
		if got.Message != "Could not change the plan" || strings.Contains(got.Message, "rpc error") {
			t.Fatalf("message for %v = %q", err, got.Message)
		}
	}
	precondition := billingMutationError(status.Error(codes.FailedPrecondition, "settle your outstanding invoices before selecting a paid tier"), "Could not change the plan", "TIER_CHANGE_FAILED", "tier_id")
	if precondition.Message != "settle your outstanding invoices before selecting a paid tier" || *precondition.Code != "TIER_CHANGE_FAILED" {
		t.Fatalf("precondition = %+v", precondition)
	}
}

func TestBillingLifecycleEnums(t *testing.T) {
	for in, want := range map[string]model.FreeFallbackStage{"scheduled": model.FreeFallbackStageScheduled, "applying": model.FreeFallbackStageApplying, "moved": model.FreeFallbackStageMoved} {
		if got, err := FreeFallbackStage(&purserpb.FreeFallbackStatus{Stage: in}); err != nil || got != want {
			t.Fatalf("stage %s = %s %v", in, got, err)
		}
	}
	if _, err := FreeFallbackReason(&purserpb.FreeFallbackStatus{Reason: "retired"}); err == nil {
		t.Fatal("an unknown reason must not map to a valid enum")
	}
	if PendingTierState(&purserpb.BillingStatusResponse{}) != nil {
		t.Fatal("no staged downgrade must be null")
	}
	if got := PendingTierState(&purserpb.BillingStatusResponse{PendingTierState: "final"}); got == nil || *got != model.PendingTierStateFinal {
		t.Fatalf("final = %v", got)
	}
}
