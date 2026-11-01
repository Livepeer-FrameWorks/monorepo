package grpc

import (
	"context"
	"database/sql"
	"errors"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// compensateRefusedMollieFunding queues unused first payments on definitive
// activation refusals. Provider reads and transient failures leave funding reusable.
func (s *PurserServer) compensateRefusedMollieFunding(ctx context.Context, tenantID, tierID, reason, mandateID string) error {
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		q := purserdb.New(tx)
		ids, err := q.ListRefusedMollieFundingIntents(ctx, purserdb.ListRefusedMollieFundingIntentsParams{TenantID: tenantID, TierID: tierID, MandateID: mandateID})
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
