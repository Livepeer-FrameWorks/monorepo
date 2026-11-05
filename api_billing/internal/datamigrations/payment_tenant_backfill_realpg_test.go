//go:build schema_verify

package datamigrations

import (
	"context"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/google/uuid"
)

func TestPaymentTenantBackfillIsBoundedAndRequiredBeforeValidation_RealPG(t *testing.T) {
	db := startPreContractPurserRealPG(t)
	ctx := context.Background()
	tenant, other := uuid.NewString(), uuid.NewString()
	invoice, otherInvoice := uuid.NewString(), uuid.NewString()
	for _, row := range []struct{ id, tenant string }{{invoice, tenant}, {otherInvoice, other}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_invoices(id,tenant_id,status,currency,amount,due_date) VALUES($1,$2,'pending','EUR',10,NOW())`, row.id, row.tenant); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_payments(invoice_id,method,amount,currency,status) VALUES($1,'card',10,'EUR','failed')`, invoice); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_payments(invoice_id,method,amount,currency,status) VALUES($1,'card',10,'EUR','failed')`, otherInvoice); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE purser.billing_payments DROP CONSTRAINT billing_payment_tenant_required; ALTER TABLE purser.billing_payments DISABLE TRIGGER billing_payment_tenant_guard; UPDATE purser.billing_payments SET tenant_id=NULL; ALTER TABLE purser.billing_payments ENABLE TRIGGER billing_payment_tenant_guard;`); err != nil {
		t.Fatal(err)
	}
	expand, err := dbsql.Content.ReadFile("migrations/purser/v0.3.14/expand/022_payment_tenant_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(expand)); err != nil {
		t.Fatal(err)
	}
	if err = VerifyPaymentTenantBackfill(ctx, db); err == nil {
		t.Fatal("incomplete payment ownership passed completeness verification")
	}
	if ready, err := purserdb.New(db).FinancialAuditPaymentOwnershipReady(ctx, tenant); err != nil || !ready.Valid || ready.Bool {
		t.Fatalf("indexed audit silently omits unbackfilled payment rows: %+v %v", ready, err)
	}
	post, err := dbsql.Content.ReadFile("migrations/purser/v0.3.14/postdeploy/014_validate_payment_tenant_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(post)); err == nil {
		t.Fatal("postdeploy accepted pending payment data migration")
	}
	migration := PaymentTenantBackfill()
	opts := datamigrate.RunOptions{Scope: datamigrate.ScopeKey{Kind: "tenant", Value: tenant}, BatchSize: 2, DryRun: true}
	preview, err := migration.Run(ctx, db, opts)
	if err != nil || preview.Changed != 5 || !preview.Done {
		t.Fatalf("dryrun=%+v %v", preview, err)
	}
	var missing int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_payments WHERE invoice_id=$1 AND tenant_id IS NULL`, invoice).Scan(&missing); err != nil || missing != 5 {
		t.Fatalf("dryrun changed data: %d %v", missing, err)
	}
	opts.DryRun = false
	first, err := migration.Run(ctx, db, opts)
	if err != nil || first.Changed != 2 || first.Done {
		t.Fatalf("first bounded batch=%+v %v", first, err)
	}
	var otherMissing int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_payments WHERE invoice_id=$1 AND tenant_id IS NULL`, otherInvoice).Scan(&otherMissing); err != nil || otherMissing != 1 {
		t.Fatalf("tenant batch escaped scope: %d %v", otherMissing, err)
	}
	// Old clients update payment status without supplying the new ownership column.
	if _, err = db.ExecContext(ctx, `UPDATE purser.billing_payments SET status='confirmed' WHERE invoice_id=$1`, otherInvoice); err != nil {
		t.Fatalf("expand blocked live legacy payment update: %v", err)
	}
	for {
		progress, err := migration.Run(ctx, db, opts)
		if err != nil {
			t.Fatal(err)
		}
		if progress.Done {
			break
		}
	}
	if err = migration.Verify(ctx, db); err != nil {
		t.Fatal(err)
	}
	if ready, err := purserdb.New(db).FinancialAuditPaymentOwnershipReady(ctx, tenant); err != nil || !ready.Valid || !ready.Bool {
		t.Fatalf("completed ownership did not unlock indexed audit: %+v %v", ready, err)
	}
	if _, err = db.ExecContext(ctx, string(post)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `DROP INDEX purser.idx_billing_payments_tenant_id_uuid`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(post)); err == nil {
		t.Fatal("postdeploy accepted missing concurrent payment index")
	}
	indexSQL, err := dbsql.Content.ReadFile("migrations/purser/v0.3.14/expand/023_payment_tenant_index.notx.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(indexSQL)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(post)); err != nil {
		t.Fatal(err)
	}
	foreignKeys, err := dbsql.Content.ReadFile("migrations/purser/v0.3.14/expand/026_prepaid_advance_tax.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(foreignKeys)); err != nil {
		t.Fatal(err)
	}
	var pendingKeys int
	keySQL := `SELECT COUNT(*) FROM pg_constraint WHERE connamespace='purser'::regnamespace AND conname IN ('pending_topups_tax_document_id_fkey','prepaid_funding_lots_source_document_id_fkey','prepaid_funding_lots_funding_balance_transaction_id_fkey','prepaid_funding_allocations_lot_id_fkey') AND NOT convalidated`
	if err = db.QueryRowContext(ctx, keySQL).Scan(&pendingKeys); err != nil || pendingKeys != 4 {
		t.Fatalf("expand did not defer all named foreign keys: %d %v", pendingKeys, err)
	}
	validateKeys, err := dbsql.Content.ReadFile("migrations/purser/v0.3.14/postdeploy/016_validate_prepaid_advance_foreign_keys.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(validateKeys)); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, keySQL).Scan(&pendingKeys); err != nil || pendingKeys != 0 {
		t.Fatalf("postdeploy left unvalidated foreign keys: %d %v", pendingKeys, err)
	}
	replay, err := migration.Run(ctx, db, opts)
	if err != nil || replay.Changed != 0 || !replay.Done {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE purser.billing_payments SET tenant_id=$1 WHERE invoice_id=$2`, other, invoice); err == nil {
		t.Fatal("payment ownership trigger allowed cross-tenant reassignment")
	}
}
