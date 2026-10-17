//go:build schema_verify

package purserdb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox/outboxtest"
)

func TestBillingEventOutboxPrunesDelivered_RealPG(t *testing.T) {
	verifyBillingEventOutboxPrune(t, startQueryCatalogRealPG(t))
}

func TestBillingEventOutboxPrunesDelivered_RealYugabyte(t *testing.T) {
	verifyBillingEventOutboxPrune(t, startQueryCatalogRealYugabyte(t))
}

func verifyBillingEventOutboxPrune(t *testing.T, db *sql.DB) {
	t.Helper()
	outboxtest.VerifyCompletedPrune(t, db, outboxtest.PruneTable{
		Table:   "purser.billing_event_outbox",
		Columns: "event_type, tenant_id",
		Values:  "'payment_succeeded', '10000000-0000-4000-8000-0000000000a3'",
	}, func(ctx context.Context, retentionSeconds int64, batchLimit int32) (int64, error) {
		return New(db).PurgeCompletedBillingEventOutbox(ctx, PurgeCompletedBillingEventOutboxParams{
			RetentionSeconds: retentionSeconds, BatchLimit: batchLimit,
		})
	})
}
