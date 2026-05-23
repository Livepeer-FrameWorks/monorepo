package handlers

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
)

// pricedLine is one rating output line annotated with the cluster attribution
// stamped at rating time. The writer persists this directly into
// invoice_line_items. cluster_id is nil for tenant-scoped lines
// (base_subscription).
type pricedLine struct {
	rating.LineItem
	ClusterID            *string
	ClusterKind          *string
	ClusterOwnerTenantID *uuid.UUID
	PricingSource        pricing.PricingSource
	OperatorCreditCents  int64
	PlatformFeeCents     int64
	PriceVersionID       *uuid.UUID
}

// clusterRatingResult is the aggregated output of cluster-aware rating across
// every cluster a tenant consumed in a period. The base subscription line is
// rated once at the tenant level; usage lines fan out per-cluster.
type clusterRatingResult struct {
	BaseLine    pricedLine
	UsageLines  []pricedLine
	BaseAmount  decimal.Decimal
	UsageAmount decimal.Decimal
	TotalAmount decimal.Decimal
	// ManualReviewReasons is non-empty when at least one cluster's pricing
	// could not be resolved (e.g. custom model with no metered_rates). The
	// caller MUST set the invoice status to 'manual_review' and halt
	// finalization side effects: no payment capture, no Stripe meter push,
	// no operator credit ledger insertion, no period advance.
	ManualReviewReasons []string
	// ClustersByID indexes the per-cluster pricing decisions made during
	// rating so the operator credit ledger and meter-event outbox can
	// look up kind/owner/version without re-resolving.
	ClustersByID map[string]*pricing.ClusterPricing
}

// collectInvoiceUsage aggregates usage_records grouped by (cluster_id,
// usage_type) for one (tenant, period) tuple. MAX for peak meters, SUM
// for the rest; uniques skipped (states can't be summed scalar). Only
// canonical-ledger 'delta' rows are counted; non-delta rows stay out of
// invoice aggregation.
//
// usage_records rows with empty cluster_id bucket under "" and are mapped by
// the resolver to platform_official.
//
// Returns (cluster_id → meter → aggregated_value). Errors abort the caller —
// rating an invoice on partial usage underbills.
func (jm *JobManager) collectInvoiceUsage(ctx context.Context, tenantID string, periodStart, periodEnd time.Time) (map[string]map[string]float64, error) {
	rows, err := jm.db.QueryContext(ctx, `
		WITH usage_rows AS (
			SELECT COALESCE(cluster_id, '') AS cluster_id,
			       usage_type,
			       usage_value
			FROM purser.usage_records
			WHERE tenant_id = $1
			  AND period_start < $3
			  AND period_end > $2
			  AND usage_type NOT IN ('unique_users', 'total_streams', 'total_viewers', 'unique_users_period')
			  AND value_kind = 'delta'
			  AND granularity = 'minute_5'

			UNION ALL

			SELECT COALESCE(cluster_id, '') AS cluster_id,
			       usage_type,
			       delta_value AS usage_value
			FROM purser.usage_adjustments
			WHERE tenant_id = $1
			  AND period_start < $3
			  AND period_end > $2
			  AND status = 'applied'
			  AND value_kind = 'correction_delta'
			  AND usage_type NOT IN ('unique_users', 'total_streams', 'total_viewers', 'unique_users_period')
		)
		SELECT cluster_id,
		       usage_type,
		       CASE
		           WHEN usage_type IN ('peak_bandwidth_mbps', 'max_viewers') THEN MAX(usage_value)
		           ELSE SUM(usage_value)
		       END AS aggregated_value
		FROM usage_rows
		GROUP BY cluster_id, usage_type
	`, tenantID, periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("query usage_records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]map[string]float64{}
	for rows.Next() {
		var clusterID, usageType string
		var val float64
		if err := rows.Scan(&clusterID, &usageType, &val); err != nil {
			return nil, fmt.Errorf("scan usage row: %w", err)
		}
		if out[clusterID] == nil {
			out[clusterID] = map[string]float64{}
		}
		out[clusterID][usageType] = val
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage rows: %w", err)
	}
	return out, nil
}

// collectInvoiceCodecBreakdowns extracts per-cluster, per-meter processing
// seconds from usage_details.codec_seconds. Keys may be plain codec names
// ("h264") or joint process/codec keys ("Livepeer:h264"), which lets cluster
// pricing distinguish future processing modes without another usage_type.
// Returns (cluster_id -> meter -> breakdown key -> seconds).
func (jm *JobManager) collectInvoiceCodecBreakdowns(ctx context.Context, tenantID string, periodStart, periodEnd time.Time) (map[string]map[string]map[string]float64, error) {
	rows, err := jm.db.QueryContext(ctx, `
		WITH usage_codec_rows AS (
			SELECT COALESCE(ur.cluster_id, '') AS cluster_id,
			       ur.usage_type,
			       kv.key,
			       kv.value::float8 AS seconds
			FROM purser.usage_records ur
			CROSS JOIN LATERAL jsonb_each_text(COALESCE(ur.usage_details->'codec_seconds', '{}'::jsonb)) AS kv(key, value)
			WHERE ur.tenant_id = $1
			  AND ur.period_start < $3
			  AND ur.period_end > $2
			  AND ur.value_kind = 'delta'
			  AND ur.granularity = 'minute_5'
			  AND ur.usage_details ? 'codec_seconds'
		),
		adjustment_base AS (
			SELECT COALESCE(ua.cluster_id, '') AS cluster_id,
			       ua.usage_type,
			       ua.delta_value::float8 AS seconds,
			       NULLIF(COALESCE(ua.details #>> '{output_codec}', ua.details #>> '{natural_key,output_codec}'), '') AS output_codec,
			       NULLIF(COALESCE(ua.details #>> '{process_type}', ua.details #>> '{natural_key,process_type}'), '') AS process_type
			FROM purser.usage_adjustments ua
			WHERE ua.tenant_id = $1
			  AND ua.period_start < $3
			  AND ua.period_end > $2
			  AND ua.status = 'applied'
			  AND ua.value_kind = 'correction_delta'
			  AND ua.usage_type = 'media_seconds'
		),
		adjustment_codec_rows AS (
			SELECT cluster_id, usage_type, output_codec AS key, seconds
			FROM adjustment_base
			WHERE output_codec IS NOT NULL

			UNION ALL

			SELECT cluster_id, usage_type, process_type || ':' || output_codec AS key, seconds
			FROM adjustment_base
			WHERE output_codec IS NOT NULL AND process_type IS NOT NULL
		)
		SELECT cluster_id,
		       usage_type,
		       key,
		       COALESCE(SUM(seconds), 0) AS seconds
		FROM (
			SELECT * FROM usage_codec_rows
			UNION ALL
			SELECT * FROM adjustment_codec_rows
		) codec_rows
		GROUP BY cluster_id, usage_type, key
	`, tenantID, periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("query codec_seconds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]map[string]map[string]float64{}
	for rows.Next() {
		var clusterID, usageType, key string
		var seconds float64
		if err := rows.Scan(&clusterID, &usageType, &key, &seconds); err != nil {
			return nil, fmt.Errorf("scan codec row: %w", err)
		}
		if out[clusterID] == nil {
			out[clusterID] = map[string]map[string]float64{}
		}
		if out[clusterID][usageType] == nil {
			out[clusterID][usageType] = map[string]float64{}
		}
		out[clusterID][usageType][key] += seconds
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate codec rows: %w", err)
	}
	return out, nil
}

// flattenUsageAcrossClusters returns the union of all per-cluster meter values
// summed across clusters. Used when the caller needs a tenant-level
// usage_details JSON blob; presentation surfaces should read invoice_line_items
// instead.
func flattenUsageAcrossClusters(perCluster map[string]map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for _, perMeter := range perCluster {
		for meter, v := range perMeter {
			out[meter] += v
		}
	}
	return out
}

// rateInvoiceForTenant runs cluster-aware rating for one tenant invoice
// period. For each cluster the tenant consumed, it resolves cluster pricing,
// rates that cluster's usage, and tags every line with cluster attribution.
//
// resolveAsOf controls the cluster_pricing_history lookup. Pass
// periodStart so a mid-period repricing remains visible per-version on the
// invoice but does not split the rate transition (Decision 3: pricing is
// period-bound).
//
// Returns ManualReviewReasons set when any cluster's pricing fails
// resolvably. The caller halts finalization in that case.
// baseProviderManaged signals that the tier base fee is collected by an
// external recurring subscription (Stripe / Mollie) rather than by Purser.
// When true, rateInvoiceForTenant still emits a base line but priced at zero
// and stamped with pricing_source = 'included_subscription' so the line
// appears in the invoice ledger as an informational row without being charged
// twice. Self-managed (free / prepaid-only / wire-transfer) subs pass false.
func (jm *JobManager) rateInvoiceForTenant(
	ctx context.Context,
	tenantID string,
	periodStart, periodEnd time.Time,
	tier *billingpkg.EffectiveTier,
	includeBasePrice bool,
	baseProviderManaged bool,
	perClusterUsage map[string]map[string]float64,
	perClusterCodecBreakdowns map[string]map[string]map[string]float64,
) (*clusterRatingResult, error) {
	if tier == nil {
		return nil, errors.New("rateInvoiceForTenant: nil tier")
	}

	// The resolver is only required when at least one cluster has a real
	// (non-empty) cluster_id. The empty-id unattributed bucket never consults
	// Quartermaster.
	resolver := jm.pricingResolver()
	if resolver == nil {
		for cid := range perClusterUsage {
			if cid != "" {
				return nil, errors.New("rateInvoiceForTenant: pricing resolver not configured (qmClient missing) but per-cluster usage requires it")
			}
		}
	}

	out := &clusterRatingResult{
		ClustersByID: make(map[string]*pricing.ClusterPricing, len(perClusterUsage)),
	}

	// 1. Base subscription line — rated once, tenant-scoped (no cluster_id).
	// For provider-managed subs the external provider (Stripe / Mollie) owns
	// the recurring base charge, so the Purser invoice records a $0
	// informational line tagged 'included_subscription' instead of double-
	// billing the tenant. Description stays neutral because provider-managed
	// subscriptions can be card, mandate, or bank-backed.
	basePrice := decimal.Zero
	if includeBasePrice && !baseProviderManaged && (tier.MeteringEnabled || !tier.BasePrice.IsZero()) {
		basePrice = tier.BasePrice
	}
	baseDescription := "Base subscription"
	basePricingSource := pricing.SourceTier
	if baseProviderManaged {
		baseDescription = "Base subscription (paid through your subscription)"
		basePricingSource = pricing.SourceIncludedSubscription
	}
	out.BaseLine = pricedLine{
		LineItem: rating.LineItem{
			LineKey:          rating.LineKeyBaseSubscription,
			Description:      baseDescription,
			Quantity:         decimal.NewFromInt(1),
			IncludedQuantity: decimal.Zero,
			BillableQuantity: decimal.NewFromInt(1),
			UnitPrice:        basePrice,
			Amount:           basePrice,
			Currency:         tier.Currency,
		},
		PricingSource: basePricingSource,
	}
	out.BaseAmount = basePrice

	// 2. Per-cluster usage fan-out. Iterate cluster IDs in deterministic
	// order so rating output is stable across runs.
	clusterIDs := make([]string, 0, len(perClusterUsage))
	for cid := range perClusterUsage {
		clusterIDs = append(clusterIDs, cid)
	}
	sort.Strings(clusterIDs)

	periodSuffix := periodStart.Format("200601")

	for _, cid := range clusterIDs {
		usageData := perClusterUsage[cid]
		if len(usageData) == 0 {
			continue
		}

		// An empty cluster_id row is unattributed. Skip the resolver and treat
		// it as the tenant's tier rules directly (platform_official). Avoids
		// spurious Quartermaster lookups for empty IDs.
		var resolved *pricing.ClusterPricing
		var resolveErr error
		if cid == "" {
			resolved = &pricing.ClusterPricing{
				Model:              pricing.ModelTierInherit,
				Kind:               pricing.KindPlatformOfficial,
				Currency:           tier.Currency,
				MeteredRules:       tier.Rules,
				PricingSource:      pricing.SourceTier,
				IsPlatformOfficial: true,
			}
		} else {
			resolved, resolveErr = pricing.ResolveClusterPricing(ctx, pricing.ResolveInputs{
				DB: jm.db, QM: resolver,
				ConsumingTenantID: tenantID,
				ClusterID:         cid,
				AsOf:              periodStart,
				TierRules:         tier.Rules,
				TierCurrency:      tier.Currency,
			})
			if errors.Is(resolveErr, pricing.ErrCustomPricingMissingForCluster) {
				out.ManualReviewReasons = append(out.ManualReviewReasons,
					fmt.Sprintf("cluster %s: custom pricing model has no metered_rates configured", cid))
				continue
			}
			if errors.Is(resolveErr, pricing.ErrAmbiguousClusterOwnership) {
				out.ManualReviewReasons = append(out.ManualReviewReasons,
					fmt.Sprintf("cluster %s: ambiguous ownership (neither platform-official nor owner_tenant_id set)", cid))
				continue
			}
			if errors.Is(resolveErr, pricing.ErrThirdPartyPricingMissing) {
				out.ManualReviewReasons = append(out.ManualReviewReasons,
					fmt.Sprintf("cluster %s: third-party marketplace cluster has no explicit pricing configured", cid))
				continue
			}
			if resolveErr != nil {
				return nil, fmt.Errorf("resolve cluster pricing for %s: %w", cid, resolveErr)
			}
		}
		out.ClustersByID[cid] = resolved
		if resolved.Currency != tier.Currency {
			out.ManualReviewReasons = append(out.ManualReviewReasons,
				fmt.Sprintf("cluster %s: prices in %s but tenant invoice currency is %s", cid, resolved.Currency, tier.Currency))
			continue
		}

		// Build a rating Input scoped to this cluster's usage and rules.
		// BasePrice is zero — the base subscription is rated once above.
		breakdowns := codecBreakdownsFromCluster(perClusterCodecBreakdowns[cid])
		input := rating.Input{
			Currency:     resolved.Currency,
			BasePrice:    decimal.Zero,
			Rules:        resolved.MeteredRules,
			Usage:        usageMapFromAggregates(usageData),
			Breakdowns:   breakdowns,
			CodecSeconds: breakdowns[rating.MeterMediaSeconds],
			PeriodStart:  periodStart,
			PeriodEnd:    periodEnd,
		}
		res, err := rating.Rate(input)
		if err != nil {
			return nil, fmt.Errorf("rate cluster %s: %w", cid, err)
		}

		// Tag each line with cluster attribution and append the
		// :<cluster_id>:<yyyymm> suffix to keep line_keys unique across
		// clusters when the same meter appears for both. base_subscription
		// never reaches here (rating engine emits it but we ignore via
		// UsageLines only).
		var ownerCopy *uuid.UUID
		if resolved.OwnerTenantID != nil {
			id := *resolved.OwnerTenantID
			ownerCopy = &id
		}
		var versionCopy *uuid.UUID
		if resolved.PriceVersionID != uuid.Nil {
			id := resolved.PriceVersionID
			versionCopy = &id
		}
		clusterIDCopy := cid
		kindStr := string(resolved.Kind)
		for _, line := range res.UsageLines {
			suffixed := line
			if cid != "" {
				suffixed.LineKey = clusterLineKey(line.LineKey, cid, periodSuffix)
			}
			operatorCreditCents, platformFeeCents, splitErr := jm.marketplaceLineSplitCents(ctx, suffixed.Amount, resolved)
			if splitErr != nil {
				return nil, fmt.Errorf("compute marketplace split for cluster %s: %w", cid, splitErr)
			}
			pl := pricedLine{
				LineItem:             suffixed,
				PricingSource:        resolved.PricingSource,
				ClusterOwnerTenantID: ownerCopy,
				OperatorCreditCents:  operatorCreditCents,
				PlatformFeeCents:     platformFeeCents,
				PriceVersionID:       versionCopy,
			}
			if cid != "" {
				pl.ClusterID = &clusterIDCopy
				pl.ClusterKind = &kindStr
			}
			out.UsageLines = append(out.UsageLines, pl)
			out.UsageAmount = out.UsageAmount.Add(suffixed.Amount)
		}
	}

	// Sort usage lines by LineKey for deterministic invoice output.
	sort.Slice(out.UsageLines, func(i, j int) bool {
		return out.UsageLines[i].LineKey < out.UsageLines[j].LineKey
	})

	out.TotalAmount = out.BaseAmount.Add(out.UsageAmount)
	return out, nil
}

// usageMapFromAggregates derives the rating engine's per-meter usage map from
// one cluster's usage_records aggregate. Every value is canonical and the map
// is intentionally data-driven: a new priced usage_type can be rated as soon as
// usage_records contains it and a pricing rule references it.
func usageMapFromAggregates(usageData map[string]float64) map[rating.Meter]decimal.Decimal {
	out := make(map[rating.Meter]decimal.Decimal, len(usageData))
	for meter, value := range usageData {
		m := rating.Meter(meter)
		if !rating.ValidMeter(m) {
			continue
		}
		out[m] = decimal.NewFromFloat(value)
	}
	return out
}

func clusterLineKey(baseKey, clusterID, periodSuffix string) string {
	const maxLineKeyLen = 128
	candidate := fmt.Sprintf("%s:%s:%s", baseKey, clusterID, periodSuffix)
	if len(candidate) <= maxLineKeyLen {
		return candidate
	}
	sum := sha1.Sum([]byte(clusterID))
	shortID := hex.EncodeToString(sum[:])[:12]
	suffix := fmt.Sprintf(":cluster-%s:%s", shortID, periodSuffix)
	if len(baseKey)+len(suffix) > maxLineKeyLen {
		baseKey = baseKey[:maxLineKeyLen-len(suffix)]
	}
	return baseKey + suffix
}

func (jm *JobManager) marketplaceLineSplitCents(ctx context.Context, amount decimal.Decimal, resolved *pricing.ClusterPricing) (operatorCreditCents, platformFeeCents int64, err error) {
	if resolved == nil || resolved.Kind != pricing.KindThirdPartyMarketplace || resolved.OwnerTenantID == nil || amount.IsZero() {
		return 0, 0, nil
	}
	grossCents := amount.Mul(decimal.NewFromInt(100)).Round(0).IntPart()
	feeBps, err := jm.lookupPlatformFeeBps(ctx, *resolved.OwnerTenantID, resolved.PricingSource)
	if err != nil {
		return 0, 0, err
	}
	platformFeeCents = signedBasisPointsCents(grossCents, feeBps)
	return grossCents - platformFeeCents, platformFeeCents, nil
}

func signedBasisPointsCents(grossCents int64, feeBps int) int64 {
	absGross := grossCents
	sign := int64(1)
	if absGross < 0 {
		absGross = -absGross
		sign = -1
	}
	return sign * ((absGross*int64(feeBps) + 5000) / 10000)
}

func (jm *JobManager) lookupPlatformFeeBps(ctx context.Context, ownerID uuid.UUID, pricingSource pricing.PricingSource) (int, error) {
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
	err := jm.db.QueryRowContext(ctx, q, ownerID, string(pricingSource)).Scan(&bps)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query platform_fee_policy: %w", err)
	}
	return bps, nil
}

// loadEmailLineItems queries persisted invoice_line_items and shapes them as
// EmailInvoiceLineItem DTOs for email rendering. cluster_name is joined from
// Quartermaster when a clusterID is present; lookup failures degrade the
// label to the cluster ID rather than failing the email entirely.
func (jm *JobManager) loadEmailLineItems(ctx context.Context, invoiceID, tenantID string) ([]EmailInvoiceLineItem, error) {
	rows, err := jm.db.QueryContext(ctx, `
		SELECT description,
		       COALESCE(cluster_id, ''),
		       COALESCE(cluster_kind, ''),
		       quantity::text,
		       unit_price::text,
		       amount::text,
		       currency,
		       pricing_source
		FROM purser.invoice_line_items
		WHERE invoice_id = $1 AND tenant_id = $2
		ORDER BY (line_key = 'base_subscription') DESC, line_key ASC
	`, invoiceID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query line items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type row struct {
		Description, ClusterID, ClusterKind, Quantity, UnitPrice, Total, Currency, PricingSource string
	}
	var raw []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Description, &r.ClusterID, &r.ClusterKind,
			&r.Quantity, &r.UnitPrice, &r.Total, &r.Currency, &r.PricingSource); err != nil {
			return nil, fmt.Errorf("scan line item: %w", err)
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate line items: %w", err)
	}

	// Resolve cluster names once per cluster_id. A best-effort lookup; we
	// fall back to the cluster_id string when Quartermaster is unavailable.
	clusterNames := map[string]string{}
	for _, r := range raw {
		if r.ClusterID == "" {
			continue
		}
		if _, seen := clusterNames[r.ClusterID]; seen {
			continue
		}
		name := r.ClusterID
		if qmClient != nil {
			if resp, qmErr := qmClient.GetCluster(ctx, r.ClusterID); qmErr == nil {
				if c := resp.GetCluster(); c != nil && c.GetClusterName() != "" {
					name = c.GetClusterName()
				}
			}
		}
		clusterNames[r.ClusterID] = name
	}

	out := make([]EmailInvoiceLineItem, 0, len(raw))
	for _, r := range raw {
		isZero := r.Total == "0" || r.Total == "0.00" || r.Total == "0.0"
		out = append(out, EmailInvoiceLineItem{
			Description:   r.Description,
			ClusterID:     r.ClusterID,
			ClusterName:   clusterNames[r.ClusterID],
			ClusterKind:   r.ClusterKind,
			Quantity:      r.Quantity,
			UnitPrice:     r.UnitPrice,
			Total:         r.Total,
			Currency:      r.Currency,
			PricingSource: r.PricingSource,
			PricingLabel:  emailPricingLabel(r.PricingSource, r.ClusterKind),
			IsZeroPrice:   isZero,
		})
	}
	return out, nil
}

// emailPricingLabel mirrors gRPC's pricingLabelFor for the email path. Kept
// here so the handlers package doesn't import api_billing/internal/grpc.
func emailPricingLabel(pricingSource, clusterKind string) string {
	switch pricingSource {
	case "tier":
		return "Subscription tier"
	case "cluster_metered":
		if clusterKind == "third_party_marketplace" {
			return "Marketplace metered"
		}
		return "Cluster metered"
	case "cluster_monthly":
		return "Cluster monthly"
	case "cluster_custom":
		return "Custom contract"
	case "free_unmetered":
		return "Free (no charge)"
	case "self_hosted":
		return "Self-hosted (no charge)"
	case "included_subscription":
		return "Included in subscription"
	default:
		return ""
	}
}

// pricingResolver returns the package-level Quartermaster client typed as the
// resolver's interface. Returns nil when handlers.Init has not been called
// with a quartermaster client (test paths and tools that don't need rating).
func (jm *JobManager) pricingResolver() pricing.QuartermasterClient {
	if qmClient == nil {
		return nil
	}
	return qmClient
}

// persistManualReviewDraft writes a held draft invoice for ops visibility
// without firing any downstream side effects. No prepaid credit is deducted,
// no period advance, no Stripe meter push. Lines persist so ops can see
// what would have been billed. Resolution flow: ops fixes the cluster
// pricing → updateInvoiceDraft re-runs → side effects fire once on the
// corrected total.
func (jm *JobManager) persistManualReviewDraft(
	ctx context.Context,
	tenantID string,
	periodStart, periodEnd time.Time,
	currency string,
	ratingResult *clusterRatingResult,
) error {
	dueDate := periodEnd.AddDate(0, 0, 14)
	totalAmt := ratingResult.TotalAmount.Round(2).String()
	baseAmt := ratingResult.BaseAmount.Round(2).String()
	meteredAmt := ratingResult.UsageAmount.Round(2).String()
	creditAmt := decimal.Zero.String()

	return withTx(ctx, jm.db, func(tx *sql.Tx) error {
		var invoiceID string
		txErr := tx.QueryRowContext(ctx, `
			INSERT INTO purser.billing_invoices (
				id, tenant_id, amount, currency, status, due_date,
				base_amount, metered_amount, prepaid_credit_applied, usage_details,
				period_start, period_end,
				created_at, updated_at
			) VALUES (
				gen_random_uuid(), $1, $2::numeric, $3, 'manual_review', $4,
				$5::numeric, $6::numeric, $7::numeric, '{}'::jsonb, $8, $9,
				NOW(), NOW()
			)
			ON CONFLICT (tenant_id, period_start) WHERE period_start IS NOT NULL
			DO UPDATE SET
				amount = EXCLUDED.amount,
				status = 'manual_review',
				due_date = EXCLUDED.due_date,
				base_amount = EXCLUDED.base_amount,
				metered_amount = EXCLUDED.metered_amount,
				period_end = EXCLUDED.period_end,
				updated_at = NOW()
			WHERE purser.billing_invoices.status IN ('draft', 'manual_review')
			RETURNING id
		`, tenantID, totalAmt, currency, dueDate, baseAmt, meteredAmt, creditAmt, periodStart, periodEnd).Scan(&invoiceID)
		if txErr != nil {
			return fmt.Errorf("upsert manual_review draft: %w", txErr)
		}
		return persistInvoiceLineItems(ctx, tx, invoiceID, tenantID, ratingResult)
	})
}
