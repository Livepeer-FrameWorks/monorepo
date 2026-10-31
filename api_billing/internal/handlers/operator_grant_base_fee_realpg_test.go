//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
)

func TestPostpaidGrantBoundaryKeepsAdvanceFeeForNetting_RealPG(t *testing.T) {
	f := newTierChangeFixture(t, "100", firstBaseFeePaid)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	closing, err := PreparePostpaidGrantBoundary(ctx, f.db, f.jobs.logger, f.service, f.tenantID, at, false)
	if err != nil {
		t.Fatal(err)
	}
	err = withTx(ctx, f.db, func(tx *sql.Tx) error {
		if lockErr := purserdb.New(tx).LockSubscriptionForPeriodClose(ctx, f.tenantID); lockErr != nil {
			return lockErr
		}
		_, closeErr := closing.CommitTx(ctx, tx)
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	var refunds int
	if err = f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id=$1 AND reference_type='base_fee_unused_share'`, f.tenantID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if refunds != 0 {
		t.Fatalf("postpaid grant boundary wrote %d prepaid-switch refunds, want none", refunds)
	}
	err = withTx(ctx, f.db, func(tx *sql.Tx) error {
		previous, planErr := planPreviousBaseFee(ctx, purserdb.New(tx), f.tenantID, at)
		if planErr != nil {
			return planErr
		}
		if !previous.found || previous.credit.cents <= 0 {
			t.Fatalf("advance fee was not available for postpaid netting: %+v", previous)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
