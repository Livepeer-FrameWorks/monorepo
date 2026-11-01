package grpc

import (
	"context"
	"database/sql"
	"errors"

	"frameworks/api_billing/internal/subscriptions"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *PurserServer) requireSelfServiceCheckout(ctx context.Context, tenantID, tierID, provider string) error {
	err := subscriptions.CheckSelfServiceCheckout(ctx, s.db, tenantID, tierID, provider)
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.FailedPrecondition, "tenant subscription is missing; bootstrap it before checkout")
	}
	if subscriptions.IsPricingRefusal(err) {
		return checkoutPolicyStatus(codes.FailedPrecondition, err.Error(), err)
	}
	if err != nil {
		return status.Errorf(codes.Internal, "check self-service tier eligibility: %v", err)
	}
	return nil
}
