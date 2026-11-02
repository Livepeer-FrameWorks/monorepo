package grpc

import (
	"context"
	"database/sql"
	"errors"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// compensateRefusedMollieFunding queues unused first payments on definitive
// activation refusals. Provider reads and transient failures leave funding reusable.
func (s *PurserServer) compensateRefusedMollieFunding(
	ctx context.Context, tenantID, tierID, reason, mandateID string,
) error {
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		q := purserdb.New(tx)
		ids, err := q.ListRefusedMollieFundingIntents(ctx, purserdb.ListRefusedMollieFundingIntentsParams{
			TenantID: tenantID, TierID: tierID, MandateID: mandateID,
		})
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err = q.StartCheckoutCompensation(ctx, purserdb.StartCheckoutCompensationParams{TenantID: tenantID, IntentID: id}); err != nil {
				return err
			}
			if err = q.RequestCheckoutRecovery(ctx, purserdb.RequestCheckoutRecoveryParams{TenantID: tenantID, IntentID: id, Reason: reason}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Repairable refusals keep the paid funding; a retry resumes activation after
// native cleanup without directing the customer through a second payment.
func mollieActivationStatus(err error) error {
	message := err.Error()
	switch {
	case errors.Is(err, subscriptions.ErrMollieFundingRequired):
		message = subscriptions.ErrMollieFundingRequired.Error()
	case errors.Is(err, subscriptions.ErrMollieFundingMismatch):
		message = subscriptions.ErrMollieFundingMismatch.Error()
	case errors.Is(err, subscriptions.ErrCheckoutExpired) && !subscriptions.IsDefinitiveMollieActivationRefusal(err):
		message = "activation setup needs cleanup; retry activation afterwards using your retained first payment"
	}
	refusal, typed := classifyPricingRefusal(err)
	if typed && !errors.Is(err, subscriptions.ErrCheckoutExpired) {
		message = refusal.message
	}
	st := status.New(codes.FailedPrecondition, message)
	if typed {
		st = refusalStatus(refusal.reason, message)
	}
	if errors.Is(err, subscriptions.ErrCheckoutCompensationQueued) {
		// The committed transition already owns funding compensation.
		return st.Err()
	}
	return &checkoutPolicyError{cause: err, status: st}
}
