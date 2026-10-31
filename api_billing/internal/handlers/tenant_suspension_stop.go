package handlers

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// stopSuspendedTenantStreams skips a subscription that already resumed. The
// short state-read transaction ends before fanout so slow cells cannot hold
// the subscription lock. An already dispatched stop can race resumption.
func (jm *JobManager) stopSuspendedTenantStreams(ctx context.Context, tenantID string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	suspended := false
	err := database.WithRetryablePostgresTx(ctx, jm.db, nil, func(tx *sql.Tx) error {
		suspended = false
		locked, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		suspended = locked.Current.Status == "suspended"
		return nil
	})
	if err != nil || !suspended {
		return err
	}
	if jm.commodoreClient == nil {
		return errors.New("stream suspension delivery is not configured")
	}
	_, err = jm.commodoreClient.TerminateTenantStreams(ctx, tenantID, "insufficient_balance")
	return err
}
