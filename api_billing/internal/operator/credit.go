// Package operator handles the cluster-operator credit ledger and the
// operator vetting (cluster_owners) state queries. ComputeAndPersistCredits
// writes invoice-line-sourced rows; PersistStripeSubscriptionCredit writes
// rows from monthly Stripe subscription invoices. Reads happen via gRPC
// RPCs in api_billing/internal/grpc. Payout settlement is a separate flow
// that promotes ledger rows from 'accruing' to 'eligible' / 'paid_out'.
package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ComputeAndPersistCredits inserts one operator_credit_ledger 'accrual' row
// per third_party_marketplace line on the invoice. Other cluster kinds
// (platform_official, tenant_private) write zero rows. The function is
// idempotent on (invoice_line_item_id) WHERE entry_type='accrual' — a
// re-run of the finalization tx skips lines that already have an accrual.
//
// Caller MUST pass tx, not the bare DB. The ledger writes are part of the
// invoice finalization atom and must roll back together.
//
// invoiceID identifies the invoice being finalized. status is checked: when
// 'manual_review', we write nothing (Decision 8: hard hold).
func ComputeAndPersistCredits(ctx context.Context, tx *sql.Tx, invoiceID, status string) error {
	if tx == nil {
		return errors.New("operator.ComputeAndPersistCredits: nil tx")
	}
	if invoiceID == "" {
		return errors.New("operator.ComputeAndPersistCredits: empty invoice_id")
	}
	if status == "manual_review" {
		// Hard hold: no payment, no Stripe push, no ledger. Resolution
		// flow is ops fixes pricing → re-finalize.
		return nil
	}

	// Pull every marketplace-attributed priced line on the invoice.
	rows, err := tx.QueryContext(ctx, `
		SELECT li.id, li.cluster_id, li.cluster_owner_tenant_id,
		       li.operator_credit_cents, li.platform_fee_cents,
		       li.currency,
		       inv.period_start, inv.period_end
		FROM purser.invoice_line_items li
		JOIN purser.billing_invoices inv ON inv.id = li.invoice_id
		WHERE li.invoice_id = $1
		  AND li.cluster_kind = 'third_party_marketplace'
		  AND li.cluster_owner_tenant_id IS NOT NULL
		  AND li.amount > 0
	`, invoiceID)
	if err != nil {
		return fmt.Errorf("query marketplace lines: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type accrual struct {
		LineItemID    uuid.UUID
		ClusterID     string
		OwnerTenantID uuid.UUID
		AmountCents   int64
		PlatformFee   int64
		Currency      string
		PeriodStart   time.Time
		PeriodEnd     time.Time
	}
	var pending []accrual
	for rows.Next() {
		var (
			lineItemID, ownerID string
			clusterID, currency string
			operatorCreditCents int64
			platformFeeCents    int64
			periodStart         time.Time
			periodEnd           sql.NullTime
		)
		if err := rows.Scan(&lineItemID, &clusterID, &ownerID, &operatorCreditCents, &platformFeeCents, &currency, &periodStart, &periodEnd); err != nil {
			return fmt.Errorf("scan marketplace line: %w", err)
		}
		liUUID, err := uuid.Parse(lineItemID)
		if err != nil {
			return fmt.Errorf("parse line_item_id %q: %w", lineItemID, err)
		}
		ownerUUID, err := uuid.Parse(ownerID)
		if err != nil {
			return fmt.Errorf("parse cluster_owner_tenant_id %q: %w", ownerID, err)
		}
		pe := periodEnd.Time
		if !periodEnd.Valid {
			pe = periodStart
		}
		pending = append(pending, accrual{
			LineItemID:    liUUID,
			ClusterID:     clusterID,
			OwnerTenantID: ownerUUID,
			AmountCents:   operatorCreditCents + platformFeeCents,
			PlatformFee:   platformFeeCents,
			Currency:      currency,
			PeriodStart:   periodStart,
			PeriodEnd:     pe,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate marketplace lines: %w", err)
	}

	for _, a := range pending {
		payable := a.AmountCents - a.PlatformFee
		ledgerStatus, err := initialLedgerStatus(ctx, tx, a.OwnerTenantID)
		if err != nil {
			return err
		}
		// ON CONFLICT skip silently: the partial unique index makes the
		// accrual idempotent on (invoice_line_item_id). Re-running the
		// finalization tx against the same line is a no-op.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO purser.operator_credit_ledger (
				source_type, invoice_line_item_id, entry_type,
				cluster_owner_tenant_id, cluster_id,
				invoice_id, period_start, period_end, currency,
				gross_cents, platform_fee_cents, payable_cents, status
			) VALUES (
				'invoice_line', $1, 'accrual', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
			)
			ON CONFLICT (invoice_line_item_id) WHERE entry_type = 'accrual' AND source_type = 'invoice_line' DO NOTHING
		`, a.LineItemID, a.OwnerTenantID, a.ClusterID, invoiceID,
			a.PeriodStart, a.PeriodEnd, a.Currency,
			a.AmountCents, a.PlatformFee, payable, ledgerStatus)
		if err != nil {
			return fmt.Errorf("insert accrual for line %s: %w", a.LineItemID, err)
		}
	}
	return nil
}

// PersistStripeSubscriptionCredit writes one accrual row sourced from a
// monthly Stripe cluster subscription invoice. Called from the
// invoice.paid webhook for cluster_subscription Stripe customers.
//
// Idempotent on (stripe_invoice_id) WHERE entry_type='accrual' AND
// source_type='stripe_subscription'. A retried webhook delivery collapses
// to a no-op.
func PersistStripeSubscriptionCredit(
	ctx context.Context,
	tx *sql.Tx,
	stripeInvoiceID string,
	ownerTenantID uuid.UUID,
	clusterID, currency string,
	grossCents int64,
	periodStart, periodEnd time.Time,
	pricingSource string, // typically "cluster_monthly"
) error {
	if tx == nil {
		return errors.New("operator.PersistStripeSubscriptionCredit: nil tx")
	}
	if stripeInvoiceID == "" {
		return errors.New("operator.PersistStripeSubscriptionCredit: empty stripe_invoice_id")
	}
	feeBps, err := lookupFeeBps(ctx, tx, ownerTenantID, pricingSource)
	if err != nil {
		return err
	}
	platformFee := (grossCents*int64(feeBps) + 5000) / 10000
	payable := grossCents - platformFee
	ledgerStatus, err := initialLedgerStatus(ctx, tx, ownerTenantID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.operator_credit_ledger (
			source_type, stripe_invoice_id, entry_type,
			cluster_owner_tenant_id, cluster_id,
			period_start, period_end, currency,
			gross_cents, platform_fee_cents, payable_cents, status
		) VALUES (
			'stripe_subscription', $1, 'accrual', $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
		ON CONFLICT (stripe_invoice_id) WHERE entry_type = 'accrual' AND source_type = 'stripe_subscription' DO NOTHING
	`, stripeInvoiceID, ownerTenantID, clusterID,
		periodStart, periodEnd, currency,
		grossCents, platformFee, payable, ledgerStatus)
	if err != nil {
		return fmt.Errorf("insert stripe-subscription accrual %s: %w", stripeInvoiceID, err)
	}
	return nil
}

// initialLedgerStatus resolves whether a new accrual should be 'accruing'
// (counted toward payout) or 'held' (recorded but parked). Held is the
// default for unvetted operators; only approved+payout_eligible owners
// produce accruing rows. This keeps the ledger complete for audit while
// preventing pre-launch / un-vetted operators from accumulating payable
// balances.
func initialLedgerStatus(ctx context.Context, tx *sql.Tx, ownerID uuid.UUID) (string, error) {
	var status string
	var payoutEligible bool
	err := tx.QueryRowContext(ctx, `
		SELECT status, payout_eligible
		FROM purser.cluster_owners
		WHERE tenant_id = $1
	`, ownerID).Scan(&status, &payoutEligible)
	if errors.Is(err, sql.ErrNoRows) {
		return "held", nil
	}
	if err != nil {
		return "", fmt.Errorf("query cluster_owners: %w", err)
	}
	if status == "approved" && payoutEligible {
		return "accruing", nil
	}
	return "held", nil
}

// lookupFeeBps resolves the platform fee basis points for a cluster owner.
// Lookup order: per-owner row → global default for third_party_marketplace.
// Returns 0 when no policy is configured (no fee is taken — fail-soft so
// invoice finalization doesn't block on missing policy).
func lookupFeeBps(ctx context.Context, tx *sql.Tx, ownerID uuid.UUID, pricingSource string) (int, error) {
	const q = `
		SELECT fee_basis_points
		FROM purser.platform_fee_policy
		WHERE cluster_kind = 'third_party_marketplace'
		  AND effective_to IS NULL
		  AND (cluster_owner_tenant_id = $1 OR cluster_owner_tenant_id IS NULL)
		  AND (pricing_source IS NULL OR pricing_source = $2)
		ORDER BY (cluster_owner_tenant_id = $1) DESC,
		         (pricing_source = $2) DESC,
		         effective_from DESC
		LIMIT 1
	`
	var bps int
	err := tx.QueryRowContext(ctx, q, ownerID, pricingSource).Scan(&bps)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query platform_fee_policy: %w", err)
	}
	return bps, nil
}

// dollarStringToCents converts a NUMERIC(20,2) text value to cents. The
// input format is the canonical decimal-as-string used by purser writers.
func dollarStringToCents(s string) (int64, error) {
	// Splitting on '.' avoids a float64 round-trip. NUMERIC(20,2) → at most
	// 2 fractional digits.
	neg := false
	if len(s) > 0 && s[0] == '-' {
		neg = true
		s = s[1:]
	}
	whole := s
	frac := "00"
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			whole = s[:i]
			frac = s[i+1:]
			break
		}
	}
	if len(frac) == 0 {
		frac = "00"
	}
	if len(frac) == 1 {
		frac = frac + "0"
	}
	if len(frac) > 2 {
		frac = frac[:2]
	}
	var w, f int64
	for _, c := range whole {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid amount %q", s)
		}
		w = w*10 + int64(c-'0')
	}
	for _, c := range frac {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid amount %q", s)
		}
		f = f*10 + int64(c-'0')
	}
	cents := w*100 + f
	if neg {
		cents = -cents
	}
	return cents, nil
}
