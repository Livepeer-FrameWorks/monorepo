//go:build schema_verify

package commodoredb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox/outboxtest"
)

func TestServiceEventOutboxPrunesDelivered_RealPG(t *testing.T) {
	verifyServiceEventOutboxPrune(t, startCommodoreQueryCatalogRealPG(t))
}

func TestServiceEventOutboxPrunesDelivered_RealYugabyte(t *testing.T) {
	verifyServiceEventOutboxPrune(t, startCommodoreQueryCatalogRealYugabyte(t))
}

func verifyServiceEventOutboxPrune(t *testing.T, db *sql.DB) {
	t.Helper()
	outboxtest.VerifyCompletedPrune(t, db, outboxtest.PruneTable{
		Table:   "commodore.service_event_outbox",
		Columns: "event_type, tenant_id",
		Values:  "'stream_created', '10000000-0000-4000-8000-0000000000a1'",
	}, func(ctx context.Context, retentionSeconds int64, batchLimit int32) (int64, error) {
		return New(db).PurgeCompletedServiceEventOutbox(ctx, PurgeCompletedServiceEventOutboxParams{
			RetentionSeconds: retentionSeconds, BatchLimit: batchLimit,
		})
	})
}
