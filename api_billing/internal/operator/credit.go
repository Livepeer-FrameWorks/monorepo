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
		  AND li.amount != 0
		  AND COALESCE(li.meter, '') NOT IN ('storage_gb_seconds_hot', 'storage_gb_seconds_cold')
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
	if err := persistStorageProviderCredits(ctx, tx, invoiceID); err != nil {
		return err
	}
	return nil
}

func persistStorageProviderCredits(ctx context.Context, tx *sql.Tx, invoiceID string) error {
	rows, err := tx.QueryContext(ctx, `
		WITH storage_lines AS (
			SELECT li.id AS line_item_id,
			       li.tenant_id,
			       COALESCE(li.cluster_id, '') AS customer_cluster_id,
			       li.meter,
			       li.currency,
			       inv.period_start,
			       inv.period_end,
			       ROUND(li.amount * 100)::bigint AS gross_cents
			FROM purser.invoice_line_items li
			JOIN purser.billing_invoices inv ON inv.id = li.invoice_id
			WHERE li.invoice_id = $1
			  AND li.meter IN ('storage_gb_seconds_hot', 'storage_gb_seconds_cold')
			  AND li.amount != 0
		),
		base_provider_rows AS (
			SELECT sl.line_item_id,
			       'storage_provider_usage' AS source_type,
			       spu.id::text AS source_id,
			       sl.tenant_id AS usage_tenant_id,
			       spu.storage_provider_tenant_id,
			       spu.storage_provider_cluster_id,
			       spu.storage_backend,
			       spu.usage_type,
			       spu.gb_seconds,
			       sl.currency,
			       sl.period_start,
			       sl.period_end,
			       sl.gross_cents
			FROM storage_lines sl
			JOIN purser.storage_provider_usage_records spu
			  ON spu.usage_tenant_id = sl.tenant_id
			 AND spu.customer_cluster_id = sl.customer_cluster_id
			 AND spu.usage_type = sl.meter
			 AND spu.period_start < sl.period_end
			 AND spu.period_end > sl.period_start
			 AND spu.value_kind = 'delta'
			 AND spu.granularity = 'minute_5'
			WHERE spu.gb_seconds != 0
		),
		adjustment_provider_rows AS (
			SELECT sl.line_item_id,
			       'usage_adjustment' AS source_type,
			       ua.id::text AS source_id,
			       sl.tenant_id AS usage_tenant_id,
			       COALESCE(ua.details #>> '{natural_key,storage_provider_tenant_id}', '') AS storage_provider_tenant_id,
			       COALESCE(ua.details #>> '{natural_key,storage_provider_cluster_id}', '') AS storage_provider_cluster_id,
			       COALESCE(ua.details #>> '{natural_key,storage_backend}', '') AS storage_backend,
			       ua.usage_type,
			       ua.delta_value AS gb_seconds,
			       sl.currency,
			       ua.period_start,
			       ua.period_end,
			       sl.gross_cents
			FROM storage_lines sl
			JOIN purser.usage_adjustments ua
			  ON ua.tenant_id = sl.tenant_id
			 AND ua.cluster_id = sl.customer_cluster_id
			 AND ua.usage_type = sl.meter
			 AND ua.period_start < sl.period_end
			 AND ua.period_end > sl.period_start
			 AND ua.value_kind = 'correction_delta'
			 AND ua.status = 'applied'
			WHERE ua.delta_value != 0
			  AND COALESCE(ua.details #>> '{natural_key,storage_provider_tenant_id}', '') <> ''
		),
		all_provider_rows AS (
			SELECT *,
			       SUM(gb_seconds) OVER (PARTITION BY line_item_id) AS line_gb_seconds
			FROM (
				SELECT * FROM base_provider_rows
				UNION ALL
				SELECT * FROM adjustment_provider_rows
			) rows
		),
		provider_rows AS (
			SELECT *
			FROM all_provider_rows
			WHERE storage_provider_tenant_id <> ''
			  AND storage_provider_tenant_id <> usage_tenant_id::text
		)
		SELECT source_type,
		       source_id,
		       storage_provider_tenant_id,
		       storage_provider_cluster_id,
		       storage_backend,
		       usage_type,
		       currency,
		       period_start,
		       period_end,
		       CASE
		         WHEN line_gb_seconds != 0 THEN ROUND(gross_cents::numeric * gb_seconds / line_gb_seconds)::bigint
		         ELSE 0
		       END AS allocated_gross_cents
		FROM provider_rows
	`, invoiceID)
	if err != nil {
		return fmt.Errorf("query storage provider usage credits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type providerAccrual struct {
		SourceType        string
		UsageRecordID     uuid.UUID
		UsageAdjustmentID uuid.UUID
		OwnerTenantID     uuid.UUID
		ClusterID         string
		Backend           string
		UsageType         string
		Currency          string
		PeriodStart       time.Time
		PeriodEnd         time.Time
		GrossCents        int64
	}
	var pending []providerAccrual
	for rows.Next() {
		var (
			sourceID, ownerID string
			sourceType        string
			a                 providerAccrual
		)
		if err := rows.Scan(&sourceType, &sourceID, &ownerID, &a.ClusterID, &a.Backend, &a.UsageType, &a.Currency, &a.PeriodStart, &a.PeriodEnd, &a.GrossCents); err != nil {
			return fmt.Errorf("scan storage provider usage credit: %w", err)
		}
		if a.GrossCents == 0 {
			continue
		}
		sourceUUID, err := uuid.Parse(sourceID)
		if err != nil {
			return fmt.Errorf("parse storage provider credit source_id %q: %w", sourceID, err)
		}
		ownerUUID, err := uuid.Parse(ownerID)
		if err != nil {
			return fmt.Errorf("parse storage_provider_tenant_id %q: %w", ownerID, err)
		}
		a.SourceType = sourceType
		switch sourceType {
		case "storage_provider_usage":
			a.UsageRecordID = sourceUUID
		case "usage_adjustment":
			a.UsageAdjustmentID = sourceUUID
		default:
			return fmt.Errorf("unsupported storage provider credit source_type %q", sourceType)
		}
		a.OwnerTenantID = ownerUUID
		pending = append(pending, a)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate storage provider usage credits: %w", err)
	}

	for _, a := range pending {
		feeBps, err := lookupFeeBps(ctx, tx, a.OwnerTenantID, "storage_provider_usage")
		if err != nil {
			return err
		}
		platformFee := platformFeeCents(a.GrossCents, feeBps)
		payable := a.GrossCents - platformFee
		ledgerStatus, err := initialLedgerStatus(ctx, tx, a.OwnerTenantID)
		if err != nil {
			return err
		}
		if a.SourceType == "usage_adjustment" {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO purser.operator_credit_ledger (
					source_type, usage_adjustment_id, entry_type,
					cluster_owner_tenant_id, cluster_id,
					invoice_id, period_start, period_end, currency,
					gross_cents, platform_fee_cents, payable_cents, status, notes
				) VALUES (
					'usage_adjustment', $1, 'accrual',
					$2, $3, $4, $5, $6, $7,
					$8, $9, $10, $11,
					jsonb_build_object('storage_backend', $12::text, 'usage_type', $13::text)
				)
				ON CONFLICT (usage_adjustment_id)
				WHERE entry_type = 'accrual' AND source_type = 'usage_adjustment'
				DO NOTHING
			`, a.UsageAdjustmentID, a.OwnerTenantID, a.ClusterID, invoiceID,
				a.PeriodStart, a.PeriodEnd, a.Currency,
				a.GrossCents, platformFee, payable, ledgerStatus,
				a.Backend, a.UsageType)
			if err != nil {
				return fmt.Errorf("insert storage provider adjustment accrual %s: %w", a.UsageAdjustmentID, err)
			}
			continue
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO purser.operator_credit_ledger (
				source_type, storage_provider_usage_record_id, entry_type,
				cluster_owner_tenant_id, cluster_id,
				invoice_id, period_start, period_end, currency,
				gross_cents, platform_fee_cents, payable_cents, status, notes
			) VALUES (
				'storage_provider_usage', $1, 'accrual',
				$2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11,
				jsonb_build_object('storage_backend', $12::text, 'usage_type', $13::text)
			)
			ON CONFLICT (storage_provider_usage_record_id)
			WHERE entry_type = 'accrual' AND source_type = 'storage_provider_usage'
			DO NOTHING
		`, a.UsageRecordID, a.OwnerTenantID, a.ClusterID, invoiceID,
			a.PeriodStart, a.PeriodEnd, a.Currency,
			a.GrossCents, platformFee, payable, ledgerStatus,
			a.Backend, a.UsageType)
		if err != nil {
			return fmt.Errorf("insert storage provider accrual %s: %w", a.UsageRecordID, err)
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
