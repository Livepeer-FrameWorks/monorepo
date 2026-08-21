// Package operator handles the cluster-operator credit ledger and the
// operator vetting (cluster_owners) state queries. ComputeAndPersistCredits
// writes invoice-line and storage-provider-sourced rows;
// PersistStripeSubscriptionCredit writes rows from monthly Stripe
// subscription invoices. Reads happen via gRPC RPCs in
// api_billing/internal/grpc. This package accrues and reads operator revenue;
// payment-rail payout batching is handled by settlement tooling outside this
// package.
package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/google/uuid"
)

// ComputeAndPersistCredits inserts one operator_credit_ledger 'accrual' row
// per third_party_marketplace line on a paid invoice. Other cluster kinds
// (platform_official, tenant_private) write zero rows. The function is
// idempotent on (invoice_line_item_id) WHERE entry_type='accrual' — a
// re-run after payment reconciliation skips lines that already have an
// accrual.
//
// Caller MUST pass tx, not the bare DB. The ledger writes are part of the
// invoice status transition atom and must roll back together.
//
// invoiceID identifies the invoice. status is checked: only 'paid' creates
// operator ledger rows, so unpaid customer invoices cannot surface as payable
// marketplace revenue.
func ComputeAndPersistCredits(ctx context.Context, tx *sql.Tx, invoiceID, status string) error {
	if tx == nil {
		return errors.New("operator.ComputeAndPersistCredits: nil tx")
	}
	if invoiceID == "" {
		return errors.New("operator.ComputeAndPersistCredits: empty invoice_id")
	}
	if status != "paid" {
		// No customer settlement, no operator accrual. Pending, overdue,
		// and manual_review invoices can be re-entered after payment or
		// ops resolution without leaking provisional revenue.
		return nil
	}

	queries := purserdb.New(tx)
	lines, err := queries.ListMarketplaceCreditLines(ctx, invoiceID)
	if err != nil {
		return fmt.Errorf("query marketplace lines: %w", err)
	}
	for _, line := range lines {
		ownerUUID, err := uuid.Parse(line.ClusterOwnerTenantID)
		if err != nil {
			return fmt.Errorf("parse cluster_owner_tenant_id %q: %w", line.ClusterOwnerTenantID, err)
		}
		grossCents := line.OperatorCreditCents + line.PlatformFeeCents
		payable := grossCents - line.PlatformFeeCents
		ledgerStatus, err := initialLedgerStatus(ctx, tx, ownerUUID)
		if err != nil {
			return err
		}
		// ON CONFLICT skip silently: the partial unique index makes the
		// accrual idempotent on (invoice_line_item_id). Re-running the
		// finalization tx against the same line is a no-op.
		err = queries.InsertMarketplaceOperatorCredit(ctx, purserdb.InsertMarketplaceOperatorCreditParams{
			InvoiceLineItemID: line.ID, ClusterOwnerTenantID: line.ClusterOwnerTenantID,
			ClusterID: line.ClusterID, InvoiceID: invoiceID,
			PeriodStart: line.PeriodStart.Time, PeriodEnd: line.PeriodEnd.Time, Currency: line.Currency,
			GrossCents: grossCents, PlatformFeeCents: line.PlatformFeeCents,
			PayableCents: payable, Status: ledgerStatus,
		})
		if err != nil {
			return fmt.Errorf("insert accrual for line %s: %w", line.ID, err)
		}
	}
	if err := persistProviderCredits(ctx, tx, invoiceID); err != nil {
		return err
	}
	return nil
}

func persistProviderCredits(ctx context.Context, tx *sql.Tx, invoiceID string) error {
	queries := purserdb.New(tx)
	allocations, err := queries.ListStorageProviderCreditAllocations(ctx, invoiceID)
	if err != nil {
		return fmt.Errorf("query storage provider usage credits: %w", err)
	}
	for _, allocation := range allocations {
		if allocation.AllocatedGrossCents == 0 {
			continue
		}
		if _, err := uuid.Parse(allocation.SourceID); err != nil {
			return fmt.Errorf("parse storage provider credit source_id %q: %w", allocation.SourceID, err)
		}
		ownerUUID, err := uuid.Parse(allocation.StorageProviderTenantID)
		if err != nil {
			return fmt.Errorf("parse storage_provider_tenant_id %q: %w", allocation.StorageProviderTenantID, err)
		}
		feeBps, err := lookupFeeBps(ctx, tx, ownerUUID, "provider_usage")
		if err != nil {
			return err
		}
		platformFee := platformFeeCents(allocation.AllocatedGrossCents, feeBps)
		payable := allocation.AllocatedGrossCents - platformFee
		ledgerStatus, err := initialLedgerStatus(ctx, tx, ownerUUID)
		if err != nil {
			return err
		}
		switch allocation.SourceType {
		case "usage_adjustment":
			err = queries.InsertUsageAdjustmentOperatorCredit(ctx, purserdb.InsertUsageAdjustmentOperatorCreditParams{
				UsageAdjustmentID: allocation.SourceID, ClusterOwnerTenantID: allocation.StorageProviderTenantID,
				ClusterID: allocation.StorageProviderClusterID, InvoiceID: invoiceID,
				PeriodStart: allocation.PeriodStart.Time, PeriodEnd: allocation.PeriodEnd.Time, Currency: allocation.Currency,
				GrossCents: allocation.AllocatedGrossCents, PlatformFeeCents: platformFee, PayableCents: payable,
				Status: ledgerStatus, StorageBackend: allocation.StorageBackend, UsageType: allocation.UsageType,
			})
			if err != nil {
				return fmt.Errorf("insert storage provider adjustment accrual %s: %w", allocation.SourceID, err)
			}
		case "provider_usage":
			err = queries.InsertProviderUsageOperatorCredit(ctx, purserdb.InsertProviderUsageOperatorCreditParams{
				ProviderUsageRecordID: allocation.SourceID, ClusterOwnerTenantID: allocation.StorageProviderTenantID,
				ClusterID: allocation.StorageProviderClusterID, InvoiceID: invoiceID,
				PeriodStart: allocation.PeriodStart.Time, PeriodEnd: allocation.PeriodEnd.Time, Currency: allocation.Currency,
				GrossCents: allocation.AllocatedGrossCents, PlatformFeeCents: platformFee, PayableCents: payable,
				Status: ledgerStatus, StorageBackend: allocation.StorageBackend, UsageType: allocation.UsageType,
			})
			if err != nil {
				return fmt.Errorf("insert storage provider accrual %s: %w", allocation.SourceID, err)
			}
		default:
			return fmt.Errorf("unsupported storage provider credit source_type %q", allocation.SourceType)
		}
	}
	return nil
}

func platformFeeCents(grossCents int64, feeBps int) int64 {
	absGross := grossCents
	sign := int64(1)
	if absGross < 0 {
		absGross = -absGross
		sign = -1
	}
	return sign * ((absGross*int64(feeBps) + 5000) / 10000)
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
	err = purserdb.New(tx).InsertStripeSubscriptionOperatorCredit(ctx, purserdb.InsertStripeSubscriptionOperatorCreditParams{
		StripeInvoiceID:      sql.NullString{String: stripeInvoiceID, Valid: true},
		ClusterOwnerTenantID: ownerTenantID.String(), ClusterID: clusterID,
		PeriodStart: periodStart, PeriodEnd: periodEnd, Currency: currency,
		GrossCents: grossCents, PlatformFeeCents: platformFee, PayableCents: payable, Status: ledgerStatus,
	})
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
	state, err := purserdb.New(tx).GetClusterOwnerLedgerState(ctx, ownerID.String())
	if errors.Is(err, sql.ErrNoRows) {
		return "held", nil
	}
	if err != nil {
		return "", fmt.Errorf("query cluster_owners: %w", err)
	}
	if state.Status == "approved" && state.PayoutEligible {
		return "accruing", nil
	}
	return "held", nil
}

// lookupFeeBps resolves the platform fee basis points for a cluster owner.
// Lookup order: per-owner row → global default for third_party_marketplace.
// Returns 0 when no policy is configured (no fee is taken — fail-soft so
// invoice finalization doesn't block on missing policy).
func lookupFeeBps(ctx context.Context, tx *sql.Tx, ownerID uuid.UUID, pricingSource string) (int, error) {
	bps, err := purserdb.New(tx).GetOperatorPlatformFeeBps(ctx, purserdb.GetOperatorPlatformFeeBpsParams{
		OwnerID: ownerID.String(), PricingSource: pricingSource,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query platform_fee_policy: %w", err)
	}
	return int(bps), nil
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
