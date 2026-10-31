//go:build schema_verify

package grpc

import (
	"database/sql"
	"errors"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestTierUpgradeStartsFreshMonth_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := t.Context()
	tenant, oldTier, newTier := uuid.NewString(), uuid.NewString(), uuid.NewString()
	start := time.Now().UTC().Add(-15 * 24 * time.Hour).Truncate(time.Second)
	end := start.Add(30 * 24 * time.Hour)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled)
VALUES ($1,$3,'Old',2,20,'EUR',false),($2,$4,'New',3,50,'EUR',false)`, oldTier, newTier, "old-"+oldTier, "new-"+newTier)
	exec(`INSERT INTO purser.tenant_subscriptions (tenant_id,tier_id,status,billing_model,payment_method,stripe_customer_id,
billing_name,billing_email,billing_address,presentment_currency,billing_period_start,billing_period_end,next_billing_date)
VALUES ($1,$2,'active','postpaid','stripe','cus_upgrade','Customer','billing@example.com',
'{"street":"Street 1","city":"Leiden","postal_code":"2332ED","country":"NL"}','USD',$3,$4,$4)`, tenant, oldTier, start, end)
	exec(`INSERT INTO purser.fx_rates (currency,reference_date,units_per_eur,source) VALUES ('USD',CURRENT_DATE,1.25,'ecb') ON CONFLICT DO NOTHING`)
	exec(`INSERT INTO purser.billing_invoices (tenant_id,status,amount,base_amount,due_date,
base_fee_period_start,base_fee_period_end,presentment_amount_cents,presentment_currency,presentment_units_per_eur,presentment_reference_date,finalized_at)
VALUES ($1,'paid',20,20,NOW(),$2,$3,2500,'USD',1.25,CURRENT_DATE,NOW())`, tenant, start, end)
	logger := logging.NewLogger()
	service := handlers.NewService(db, logger, nil, nil, nil, nil, nil)
	handlers.NewJobManager(db, logger, nil, nil, nil, nil, service, nil)
	server := &PurserServer{db: db, logger: logger, billing: service}
	response, err := server.ChangeBillingTier(ctx, &purserpb.ChangeBillingTierRequest{TenantId: tenant, TierId: newTier})
	if err != nil {
		t.Fatal(err)
	}
	var actualStart, actualEnd, expectedEnd time.Time
	if err = db.QueryRowContext(ctx, `SELECT billing_period_start,billing_period_end,billing_period_start + INTERVAL '1 month' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&actualStart, &actualEnd, &expectedEnd); err != nil {
		t.Fatal(err)
	}
	if !actualStart.After(start) || !actualEnd.Equal(expectedEnd) {
		t.Fatalf("paid upgrade period=%s..%s, expected capped end %s and start after %s; old end %s, response=%+v", actualStart, actualEnd, expectedEnd, start, end, response)
	}
	var amount, credit string
	if err = db.QueryRowContext(ctx, `SELECT i.amount::text,(-line.amount)::text FROM purser.billing_invoices i
JOIN purser.invoice_line_items line ON line.invoice_id=i.id AND line.line_key='unused_base_fee_credit'
WHERE i.tenant_id=$1 AND i.base_fee_period_start=$2`, tenant, actualStart).Scan(&amount, &credit); err != nil {
		t.Fatal(err)
	}
	wantCredit := decimal.NewFromInt(2000).Mul(decimal.NewFromInt(end.Sub(actualStart).Microseconds())).Div(decimal.NewFromInt(end.Sub(start).Microseconds())).Round(0).Shift(-2)
	if credit != wantCredit.StringFixed(2) || amount != decimal.NewFromInt(50).Sub(wantCredit).StringFixed(2) {
		t.Fatalf("new base fee/credit=%s/%s, expected %s/%s", amount, credit, decimal.NewFromInt(50).Sub(wantCredit), wantCredit)
	}
	var invoices int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND base_fee_period_start IS NULL AND period_start=$2 AND period_end=$3 AND status<>'draft'`, tenant, start, actualStart).Scan(&invoices); err != nil || invoices != 1 {
		t.Fatalf("old usage period was not finalized once: invoices=%d err=%v", invoices, err)
	}
	if _, err = server.ChangeBillingTier(ctx, &purserpb.ChangeBillingTierRequest{TenantId: tenant, TierId: newTier}); err != nil {
		t.Fatal(err)
	}
	var replayStart, replayEnd sql.NullTime
	if err = db.QueryRowContext(ctx, `SELECT billing_period_start,billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&replayStart, &replayEnd); err != nil || !replayStart.Time.Equal(actualStart) || !replayEnd.Time.Equal(actualEnd) {
		t.Fatalf("retry reset the period again: %v..%v, err=%v", replayStart, replayEnd, err)
	}
}

func TestTierUpgradeStartsFreshMonth_RealYugabyte(t *testing.T) {
	TestTierUpgradeStartsFreshMonth_RealPG(t)
}

func TestTierUpgradeClockAfterGrantBoundary_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tenant, _, target := seedGrantRace(t, db)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			t.Error(rollbackErr)
		}
	}()
	var transactionStart time.Time
	if err = tx.QueryRowContext(ctx, `SELECT NOW()`).Scan(&transactionStart); err != nil {
		t.Fatal(err)
	}
	logger := logging.NewLogger()
	server := &PurserServer{db: db, logger: logger, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	if _, err = server.AdminRevokeBillingGrant(operatorAssignCtx(uuid.NewString()), &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenant, Reason: "end trial before upgrade"}); err != nil {
		t.Fatal(err)
	}
	_, err = subscriptions.LockChange(ctx, tx, tenant, subscriptions.ChangePricing)
	if err != nil {
		t.Fatal(err)
	}
	var start, end time.Time
	if err = tx.QueryRowContext(ctx, `SELECT billing_period_start,billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	if !start.After(transactionStart) {
		t.Fatalf("fixture did not advance the phase after transaction start: %v <= %v", start, transactionStart)
	}
	service := handlers.NewService(db, logger, nil, nil, nil, nil, nil)
	id, err := service.StageTierUpgradeTx(ctx, tx, tenant, target, start, end)
	if err != nil {
		t.Fatal(err)
	}
	row, err := purserdb.New(tx).GetTierUpgrade(ctx, purserdb.GetTierUpgradeParams{TenantID: tenant, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if row.RequestedAt.Before(start) {
		t.Fatalf("upgrade used transaction start before its locked billing phase: requested=%v phase=%v", row.RequestedAt, start)
	}
}

func TestTierUpgradeClockAfterGrantBoundary_RealYugabyte(t *testing.T) {
	TestTierUpgradeClockAfterGrantBoundary_RealPG(t)
}
