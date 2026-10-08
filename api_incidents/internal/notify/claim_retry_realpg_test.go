//go:build schema_verify

package notify

import (
	"context"
	"testing"
	"time"

	"frameworks/api_incidents/internal/incidents"
	"frameworks/api_incidents/internal/lookouttest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

type unreachableDispatcher struct{ t *testing.T }

func (d unreachableDispatcher) Dispatch(context.Context, Delivery) ([]string, error) {
	d.t.Error("a delivery was dispatched although every claim aborted")
	return nil, nil
}

// TestLookoutDeliveryClaimRunsOneRetryLayer_RealPG aborts every claim
// transaction with SQLSTATE 40001 when it leases the row, as a YugabyteDB
// conflict does, and counts the transactions one outbox pass runs. The store
// replays its claim transaction and the worker calls the claim once, so a
// pass runs DefaultRetryAttempts transactions, not that number squared.
func TestLookoutDeliveryClaimRunsOneRetryLayer_RealPG(t *testing.T) {
	db := lookouttest.StartPostgres(t)
	ctx := context.Background()
	svc := &incidents.Service{DB: db, Router: slackOnlyRouter{}}
	ingestPlatformIncident(t, svc, "contended-claim")

	// nextval is not rolled back with the aborted transaction, so the
	// sequence counts the claim transactions that reached the lease.
	for _, stmt := range []string{
		`CREATE SEQUENCE lookout.test_claim_attempts`,
		`CREATE FUNCTION lookout.test_abort_lease() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM nextval('lookout.test_claim_attempts');
			RAISE EXCEPTION 'restart read required' USING ERRCODE = '40001';
		END $$`,
		`CREATE TRIGGER test_abort_lease BEFORE UPDATE OF lease_token ON lookout.notification_outbox
		FOR EACH ROW EXECUTE FUNCTION lookout.test_abort_lease()`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	worker := &outbox.Worker[Delivery]{
		Config:     outbox.Config{BaseBackoff: time.Second, MaxBackoff: time.Minute, BatchSize: 10, PollPeriod: time.Hour, Lease: time.Minute},
		Store:      &Store{DB: db, Channels: allEnabled{}},
		Dispatcher: unreachableDispatcher{t: t},
		Logger:     logging.NewLogger(),
	}
	worker.ProcessBatch(ctx)

	var attempts int64
	if err := db.QueryRowContext(ctx, `SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM lookout.test_claim_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != database.DefaultRetryAttempts {
		t.Fatalf("one outbox pass ran %d claim transactions, want %d: one retry layer", attempts, database.DefaultRetryAttempts)
	}
}
