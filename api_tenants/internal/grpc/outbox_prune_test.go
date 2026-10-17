package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

// The outbox worker prunes a table only when its Store implements outbox.CompletedPruner; each store must delete
// its own delivered rows past the shared retention.
func TestOutboxStoresPruneTheirDeliveredRows(t *testing.T) {
	for _, tc := range []struct {
		table string
		store func(*QuartermasterServer) any
	}{
		{`quartermaster\.service_event_outbox`, func(s *QuartermasterServer) any { return &qmOutboxStore{server: s} }},
		{`quartermaster\.navigator_custom_domain_outbox`, func(s *QuartermasterServer) any { return &navOutboxStore{server: s} }},
		{`quartermaster\.navigator_tenant_alias_outbox`, func(s *QuartermasterServer) any { return &aliasOutboxStore{server: s} }},
	} {
		t.Run(tc.table, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close() //nolint:errcheck
			pruner, ok := tc.store(&QuartermasterServer{db: db, logger: logrus.New()}).(outbox.CompletedPruner)
			if !ok {
				t.Fatalf("%s store does not prune delivered rows", tc.table)
			}
			mock.ExpectExec(`DELETE FROM `+tc.table+` o\s+USING old_completed`).
				WithArgs(int64(outbox.CompletedRetention.Seconds()), int32(outbox.PruneBatch)).
				WillReturnResult(sqlmock.NewResult(0, 3))
			n, err := pruner.PruneCompleted(context.Background())
			if err != nil || n != 3 {
				t.Fatalf("PruneCompleted = %d, %v; want 3 rows", n, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expectations: %v", err)
			}
		})
	}
}
