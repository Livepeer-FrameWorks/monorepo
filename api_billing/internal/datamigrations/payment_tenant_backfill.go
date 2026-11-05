package datamigrations

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/google/uuid"
)

const PaymentTenantBackfillID = "purser_payment_tenant_backfill_v0_3_14"

// PaymentTenantBackfill assigns historical payments to the invoice owner in
// bounded tenant batches. The ownership trigger fills new or updated rows.
func PaymentTenantBackfill() datamigrate.Migration {
	return datamigrate.Migration{ID: PaymentTenantBackfillID, Service: "purser", IntroducedIn: "v0.3.14", RequiredBeforePhase: "postdeploy", Description: "populate payment tenant ownership before validating the indexed financial evidence ledger", Run: runPaymentTenantBackfill, Verify: VerifyPaymentTenantBackfill, Scopes: paymentTenantBackfillScopes}
}

func paymentTenantBackfillScopes(ctx context.Context, db datamigrate.DB) ([]datamigrate.ScopeKey, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT invoice.tenant_id::text FROM purser.billing_payments payment JOIN purser.billing_invoices invoice ON invoice.id=payment.invoice_id WHERE payment.tenant_id IS NULL ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var scopes []datamigrate.ScopeKey
	for rows.Next() {
		var tenant string
		if err = rows.Scan(&tenant); err != nil {
			return nil, err
		}
		scopes = append(scopes, datamigrate.ScopeKey{Kind: "tenant", Value: tenant})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(scopes) == 0 {
		scopes = []datamigrate.ScopeKey{{}}
	}
	return scopes, nil
}

func runPaymentTenantBackfill(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	if opts.Scope.IsZero() {
		return datamigrate.Progress{Done: true}, VerifyPaymentTenantBackfill(ctx, db)
	}
	if opts.Scope.Kind != "tenant" {
		return datamigrate.Progress{}, fmt.Errorf("payment tenant backfill requires a tenant scope")
	}
	if _, err := uuid.Parse(opts.Scope.Value); err != nil {
		return datamigrate.Progress{}, fmt.Errorf("invalid payment tenant scope: %w", err)
	}
	if opts.DryRun {
		var count int64
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_payments payment JOIN purser.billing_invoices invoice ON invoice.id=payment.invoice_id WHERE invoice.tenant_id=$1::text::uuid AND payment.tenant_id IS NULL`, opts.Scope.Value).Scan(&count)
		return datamigrate.Progress{Scanned: count, Changed: count, Done: true}, err
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = 500
	}
	batch = min(batch, 5000)
	result, err := db.ExecContext(ctx, `WITH pending AS (
 SELECT payment.id FROM purser.billing_payments payment JOIN purser.billing_invoices invoice ON invoice.id=payment.invoice_id
 WHERE invoice.tenant_id=$1::text::uuid AND payment.tenant_id IS NULL
 ORDER BY payment.id LIMIT $2 FOR UPDATE OF payment
 ) UPDATE purser.billing_payments payment SET tenant_id=$1::text::uuid FROM pending
 WHERE payment.id=pending.id AND payment.tenant_id IS NULL`, opts.Scope.Value, batch)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	changed, err := result.RowsAffected()
	return datamigrate.Progress{Scanned: changed, Changed: changed, Done: changed < int64(batch)}, err
}

// VerifyPaymentTenantBackfill rejects missing or mismatched invoice ownership.
func VerifyPaymentTenantBackfill(ctx context.Context, db datamigrate.DB) error {
	var invalid int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_payments payment LEFT JOIN purser.billing_invoices invoice ON invoice.id=payment.invoice_id WHERE invoice.id IS NULL OR payment.tenant_id IS DISTINCT FROM invoice.tenant_id`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf("%d payments lack verified invoice tenant ownership", invalid)
	}
	return nil
}
