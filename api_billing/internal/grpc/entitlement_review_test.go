package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEntitlementReviewRequiresOperatorPreconditionAndReason(t *testing.T) {
	server := &PurserServer{}
	operator := context.WithValue(t.Context(), ctxkeys.KeyPlatformOperator, true)
	for _, tc := range []struct {
		ctx  context.Context
		req  *purserpb.UpdateSubscriptionRequest
		code codes.Code
	}{
		{t.Context(), &purserpb.UpdateSubscriptionRequest{CheckEntitlementOverrides: true, Reason: "review"}, codes.PermissionDenied},
		{operator, &purserpb.UpdateSubscriptionRequest{Reason: "review"}, codes.InvalidArgument},
		{operator, &purserpb.UpdateSubscriptionRequest{CheckEntitlementOverrides: true, Reason: " "}, codes.InvalidArgument},
	} {
		if _, err := server.UpdateSubscriptionWithEntitlementReview(tc.ctx, tc.req); status.Code(err) != tc.code {
			t.Fatalf("review authorization: %v, want %v", err, tc.code)
		}
	}
}
