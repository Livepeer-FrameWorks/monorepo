//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
)

func TestTenantClosureFencesWaitingPricingWrite_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	closing, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closing.Rollback()
	q := purserdb.New(closing)
	if err := q.LockSubscriptionForPeriodClose(ctx, tenant.id); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	app := "closure-race:" + uuid.NewString()
	go func() {
		result <- database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
			var isolation string
			if err := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
				return err
			}
			if isolation != "read committed" {
				return fmt.Errorf("pricing isolation=%s", isolation)
			}
			if _, err := tx.ExecContext(ctx, `SELECT set_config('application_name',$1,true)`, app); err != nil {
				return err
			}
			_, err := subscriptions.LockChange(ctx, tx, tenant.id, subscriptions.ChangePricing)
			return err
		})
	}()
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND state='active' AND query LIKE '%LockTenantSubscriptionForProviderChange%')`, app).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("pricing did not wait for closure: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := q.RecordTenantBillingClosure(ctx, purserdb.RecordTenantBillingClosureParams{TenantID: tenant.id, ClosedAt: now, EventID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	if err := q.HaltClosedTenantBilling(ctx, tenant.id); err != nil {
		t.Fatal(err)
	}
	if err := closing.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, subscriptions.ErrTenantBillingClosed) {
			t.Fatalf("waiting pricing write missed committed closure: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
