//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox/outboxtest"
)

func TestOutboxesPruneDelivered_RealPG(t *testing.T) {
	verifyOutboxesPrune(t, startQuartermasterQueryCatalogRealPG(t))
}

func TestOutboxesPruneDelivered_RealYugabyte(t *testing.T) {
	verifyOutboxesPrune(t, startQuartermasterQueryCatalogRealYugabyte(t))
}

func verifyOutboxesPrune(t *testing.T, db *sql.DB) {
	t.Helper()
	outboxtest.VerifyCompletedPrune(t, db, outboxtest.PruneTable{
		Table:   "quartermaster.service_event_outbox",
		Columns: "event_type, tenant_id",
		Values:  "'tenant_updated', '10000000-0000-4000-8000-0000000000a2'",
	}, func(ctx context.Context, retentionSeconds int64, batchLimit int32) (int64, error) {
		return New(db).PurgeCompletedServiceEventOutbox(ctx, PurgeCompletedServiceEventOutboxParams{
			RetentionSeconds: retentionSeconds, BatchLimit: batchLimit,
		})
	})
	outboxtest.VerifyCompletedPrune(t, db, outboxtest.PruneTable{
		Table:   "quartermaster.navigator_custom_domain_outbox",
		Columns: "tenant_id, domain, action",
		Values:  "'10000000-0000-4000-8000-0000000000a2', 'video.example.com', 'ensure'",
	}, func(ctx context.Context, retentionSeconds int64, batchLimit int32) (int64, error) {
		return New(db).PurgeCompletedNavigatorCustomDomainOutbox(ctx, PurgeCompletedNavigatorCustomDomainOutboxParams{
			RetentionSeconds: retentionSeconds, BatchLimit: batchLimit,
		})
	})
}
