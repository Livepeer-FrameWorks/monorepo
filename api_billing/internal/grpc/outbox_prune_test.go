package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

// The outbox worker prunes a table only when its Store implements outbox.CompletedPruner; the billing-event store
// must delete its delivered rows past the shared retention.
func TestBillingEventOutboxStorePrunesDeliveredRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck
	var store any = &billingOutboxStore{server: &PurserServer{db: db, logger: logging.NewLogger()}}
	pruner, ok := store.(outbox.CompletedPruner)
	if !ok {
		t.Fatal("purser billing_event_outbox store does not prune delivered rows")
	}
	mock.ExpectExec(`DELETE FROM purser\.billing_event_outbox o\s+USING old_completed`).
		WithArgs(int64(outbox.CompletedRetention.Seconds()), int32(outbox.PruneBatch)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	n, err := pruner.PruneCompleted(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("PruneCompleted = %d, %v; want 3 rows", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
