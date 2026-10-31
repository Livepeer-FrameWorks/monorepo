package handlers

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// stopSuspendedTenantStreams retries partial and unavailable delivery. The root
// lock orders each delivery with resumption, so a queued stop cannot act on a
// subscription that already resumed. Foghorn stops stream sessions only.
func (jm *JobManager) stopSuspendedTenantStreams(ctx context.Context, tenantID string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return database.WithRetryablePostgresTx(ctx, jm.db, nil, func(tx *sql.Tx) error {
		locked, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if locked.Current.Status != "suspended" {
			return nil
		}
		if jm.commodoreClient == nil {
			return errors.New("stream suspension delivery is not configured")
		}
		_, err = jm.commodoreClient.TerminateTenantStreams(ctx, tenantID, "insufficient_balance")
		return err
	})
}
