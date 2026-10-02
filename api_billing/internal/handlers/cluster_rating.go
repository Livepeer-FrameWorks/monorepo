package handlers

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/appconfig"
	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
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
	// GrossUsageAmount is the unwaived metered total across all clusters (what
	// usage would have rated to). Equals UsageAmount when usage is not waived;
	// when the beta waiver is on it is the would-have-cost figure for display.
	GrossUsageAmount decimal.Decimal
	TotalAmount      decimal.Decimal
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

func dimensionedQuantity(usageType, unit string, rawDimensions json.RawMessage, value float64) (rating.DimensionedQuantity, error) {
	dimensions := map[string]string{}
	if len(rawDimensions) > 0 {
		if err := json.Unmarshal(rawDimensions, &dimensions); err != nil {
			return rating.DimensionedQuantity{}, fmt.Errorf("decode dimensions for %s: %w", usageType, err)
		}
	}
	return rating.DimensionedQuantity{
		Meter: rating.Meter(usageType), Unit: unit, Dimensions: dimensions,
		Quantity: decimal.NewFromFloat(value),
	}, nil
}

// usagePhase is one phase of a billing period that switches between prepaid
// and postpaid split: its window, where the split period began and ends, and
// for a phase a switch closes now, the time records must have reached Purser
// by.
type usagePhase struct {
	start, end time.Time
	// chainFrom is the split period's start (splitPeriodStart): records of
	// earlier phases that reached Purser after this phase began belong here.
	chainFrom time.Time
	// periodEnd is where the split period ends; zero means end.
	periodEnd time.Time
	// receivedBefore, when set, is the switch that closes this phase.
	receivedBefore sql.NullTime
}

// splitEnd is where the split period the phase belongs to ends.
func (p usagePhase) splitEnd() time.Time {
	if p.periodEnd.IsZero() {
		return p.end
	}
	return p.periodEnd
}

// splitStart is where the split period the phase belongs to began.
func (p usagePhase) splitStart() time.Time {
	if p.chainFrom.IsZero() || p.chainFrom.After(p.start) {
		return p.start
	}
	return p.chainFrom
}

// baseFeeShare is what the phase pays of a base fee charged for its split
// period: each phase pays for its own time. The share runs from the phase's
// start to its end, in whole seconds of the split period, each end rounded
// half away from zero to the cent, so the phases' shares add up to exactly
// the fee. A phase that is the whole period, or runs past its end, pays at
// most the whole fee.
func (p usagePhase) baseFeeShare(fee decimal.Decimal) decimal.Decimal {
	from, until := p.splitStart(), p.splitEnd()
	whole := int64(until.Sub(from) / time.Second)
	if whole <= 0 || (!p.start.After(from) && !p.end.Before(until)) {
		return fee
	}
	position := func(t time.Time) decimal.Decimal {
		if t.After(until) {
			t = until
		}
		elapsed := int64(t.Sub(from) / time.Second)
		return fee.Shift(2).Mul(decimal.NewFromInt(elapsed)).Div(decimal.NewFromInt(whole)).Round(0)
	}
	return position(p.end).Sub(position(p.start)).Shift(-2)
}

// applyBaseFeeShare charges a rated document's base fee for its phase's
// share of the split period (baseFeeShare).
func applyBaseFeeShare(result *clusterRatingResult, phase usagePhase) {
	if !result.BaseAmount.IsPositive() {
		return
	}
	share := phase.baseFeeShare(result.BaseAmount)
	if share.Equal(result.BaseAmount) {
		return
	}
	until := phase.end
	if until.After(phase.splitEnd()) {
		until = phase.splitEnd()
	}
	result.TotalAmount = result.TotalAmount.Sub(result.BaseAmount).Add(share)
	result.BaseAmount = share
	result.BaseLine.UnitPrice = share
	result.BaseLine.Amount = share
	result.BaseLine.Description = fmt.Sprintf("%s %s to %s", result.BaseLine.Description,
		phase.start.UTC().Format(time.DateOnly), until.UTC().Format(time.DateOnly))
}

// collectPhaseUsage is the usage the billing document of a phase rates, per
// cluster in both shapes rating takes. A usage record or correction belongs
// to the phase in force when it reached Purser: CollectPhaseUsage holds the
// rule, so the phases of a split period never share one.
func collectPhaseUsage(ctx context.Context, db purserdb.DBTX, tenantID string, phase usagePhase) (map[string]map[string]float64, map[string][]rating.DimensionedQuantity, error) {
	queries := purserdb.New(db)
	rows, err := queries.CollectPhaseUsage(ctx, purserdb.CollectPhaseUsageParams{
		TenantID: tenantID, WindowStart: phase.start, WindowEnd: phase.end, ChainFrom: phase.chainFrom,
		ReceivedBefore: phase.receivedBefore,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("query or iterate usage rows: %w", err)
	}
	perCluster := map[string]map[string]float64{}
	for _, row := range rows {
		if perCluster[row.ClusterID] == nil {
			perCluster[row.ClusterID] = map[string]float64{}
		}
		perCluster[row.ClusterID][row.UsageType] = row.AggregatedValue
	}
	dimensioned, err := collectPhaseDimensionedUsage(ctx, queries, tenantID, phase)
	if err != nil {
		return nil, nil, err
	}
	return perCluster, dimensioned, nil
}

// collectPhaseDimensionedUsage is collectPhaseUsage per meter dimension.
func collectPhaseDimensionedUsage(ctx context.Context, queries *purserdb.Queries, tenantID string, phase usagePhase) (map[string][]rating.DimensionedQuantity, error) {
	rows, err := queries.CollectPhaseDimensionedUsage(ctx, purserdb.CollectPhaseDimensionedUsageParams{
		TenantID: tenantID, WindowStart: phase.start, WindowEnd: phase.end, ChainFrom: phase.chainFrom,
		ReceivedBefore: phase.receivedBefore,
	})
	if err != nil {
		return nil, fmt.Errorf("query dimensioned usage: %w", err)
	}
	dimensioned := map[string][]rating.DimensionedQuantity{}
	for _, row := range rows {
		quantity, err := dimensionedQuantity(row.UsageType, row.Unit, row.Dimensions, row.Quantity)
		if err != nil {
			return nil, err
		}
		dimensioned[row.ClusterID] = append(dimensioned[row.ClusterID], quantity)
	}
	return dimensioned, nil
}

// prepaidUsagePhase is the phase prepaid settlements, reservations and the
// statement of the prepaid period [periodStart, periodEnd) rate.
func prepaidUsagePhase(ctx context.Context, db purserdb.DBTX, tenantID string, periodStart, periodEnd time.Time) (usagePhase, error) {
	chainFrom, err := splitPeriodStart(ctx, db, tenantID, periodStart)
	if err != nil {
		return usagePhase{}, err
	}
	return usagePhase{start: periodStart, end: periodEnd, chainFrom: chainFrom}, nil
}

// splitPeriodStart is where the period began that switches between prepaid
// and postpaid split up to periodStart, following the documents that closed
// each phase at its switch. It is periodStart when no switch ended a phase
// there.
func splitPeriodStart(ctx context.Context, db purserdb.DBTX, tenantID string, periodStart time.Time) (time.Time, error) {
	phaseStart, err := purserdb.New(db).GetSplitPeriodStart(ctx, purserdb.GetSplitPeriodStartParams{
		TenantID: tenantID, PeriodStart: periodStart,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return periodStart, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("look up the phases before %s: %w", periodStart.Format(time.RFC3339), err)
	}
	return phaseStart, nil
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
// baseProviderManaged signals that the tier base fee is collected outside this
// invoice: by an external recurring subscription (Stripe / Mollie) or on the
// period's advance base-fee invoice. When true, rateInvoiceForTenant still
// emits a base line but priced at zero and stamped with pricing_source =
// 'included_subscription' so the line appears in the invoice ledger as an
// informational row without being charged twice. Tenants whose base fee is
// billed in arrears on this invoice pass false.
func (jm *JobManager) rateInvoiceForTenant(
	ctx context.Context,
	tenantID string,
	periodStart, periodEnd time.Time,
	tier *billingpkg.EffectiveTier,
	includeBasePrice bool,
	baseProviderManaged bool,
	perClusterUsage map[string]map[string]float64,
	perClusterDimensioned map[string][]rating.DimensionedQuantity,
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
				return nil, errors.New("rateInvoiceForTenant: pricing resolver not configured (jm.billing.qmClient missing) but per-cluster usage requires it")
			}
		}
	}

	out := &clusterRatingResult{
		ClustersByID: make(map[string]*pricing.ClusterPricing, len(perClusterUsage)),
	}

	// 1. Base subscription line — rated once, tenant-scoped (no cluster_id).
	// When the base fee is collected elsewhere — by a provider subscription
	// (Stripe / Mollie) or on Purser's advance base-fee invoice — the usage
	// invoice records a zero informational line tagged 'included_subscription'
	// instead of billing the base twice.
	basePrice := decimal.Zero
	if includeBasePrice && !baseProviderManaged && (tier.MeteringEnabled || !tier.BasePrice.IsZero()) {
		basePrice = tier.BasePrice
	}
	baseDescription := "Base subscription"
	if tier.BasePriceGranted {
		baseDescription = "Base subscription (operator arrangement)"
	}
	basePricingSource := pricing.SourceTier
	if baseProviderManaged {
		baseDescription = "Base subscription (billed separately)"
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
		input := rating.Input{
			Currency:          resolved.Currency,
			BasePrice:         decimal.Zero,
			Rules:             resolved.MeteredRules,
			Usage:             usageMapFromAggregates(usageData),
			Quantities:        perClusterDimensioned[cid],
			PeriodStart:       periodStart,
			PeriodEnd:         periodEnd,
			WaiveUsageCharges: tier.WaivesUsage(appconfig.Runtime().WaiveUsageCharges),
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
			// A line that rated to a real amount but was waived to €0 by the beta
			// waiver is stamped beta_free. Lines that were already €0 (within the
			// included allowance, or included_subscription) keep their real source.
			if pl.GrossAmount.IsPositive() && pl.Amount.IsZero() {
				pl.PricingSource = pricing.SourceBetaFree
			}
			if cid != "" {
				pl.ClusterID = &clusterIDCopy
				pl.ClusterKind = &kindStr
			}
			out.UsageLines = append(out.UsageLines, pl)
			out.UsageAmount = out.UsageAmount.Add(suffixed.Amount)
		}
		out.GrossUsageAmount = out.GrossUsageAmount.Add(res.GrossUsageAmount)
	}

	monthlyLines, monthlyErr := jm.purserInvoicedClusterMonthlyLines(ctx, tenantID, periodStart, periodEnd, tier, resolver, periodSuffix)
	if monthlyErr != nil {
		return nil, monthlyErr
	}
	for _, line := range monthlyLines {
		out.UsageLines = append(out.UsageLines, line)
		out.UsageAmount = out.UsageAmount.Add(line.Amount)
		out.GrossUsageAmount = out.GrossUsageAmount.Add(line.Amount)
	}

	// Sort usage lines by LineKey for deterministic invoice output.
	sort.Slice(out.UsageLines, func(i, j int) bool {
		return out.UsageLines[i].LineKey < out.UsageLines[j].LineKey
	})

	out.TotalAmount = out.BaseAmount.Add(out.UsageAmount)
	return out, nil
}

// purserInvoicedClusterMonthlyLines prices the monthly cluster subscriptions
// billed on Purser invoices instead of a Stripe subscription: one line per
// subscription active during the period at the cluster's monthly price in
// effect at the period start, attributed to the cluster for operator credit.
// The price is prorated like the advance base fee: times the seconds the
// subscription was active inside the period over the seconds of the month
// starting at the period start, at most once, rounded half away from zero to
// the cent. A period closed early by a tier change and the period starting at
// the change therefore each carry only their share, and a subscription added,
// cancelled, or reactivated inside a period pays for its active time.
func (jm *JobManager) purserInvoicedClusterMonthlyLines(
	ctx context.Context,
	tenantID string,
	periodStart, periodEnd time.Time,
	tier *billingpkg.EffectiveTier,
	resolver pricing.QuartermasterClient,
	periodSuffix string,
) ([]pricedLine, error) {
	queries := purserdb.New(jm.db)
	subscriptions, err := queries.ListPurserInvoicedClusterSubscriptionsForPeriod(ctx, purserdb.ListPurserInvoicedClusterSubscriptionsForPeriodParams{
		TenantID: tenantID, PeriodStart: periodStart, PeriodEnd: periodEnd,
	})
	if err != nil {
		return nil, fmt.Errorf("list Purser-invoiced cluster subscriptions: %w", err)
	}
	if len(subscriptions) == 0 {
		return nil, nil
	}
	if resolver == nil {
		return nil, errors.New("rateInvoiceForTenant: pricing resolver not configured but monthly cluster subscriptions require it")
	}
	type activeSpan struct {
		from, until           time.Time
		seconds, monthSeconds int64
	}
	spans := make(map[string]activeSpan)
	for _, subscription := range subscriptions {
		clusterID := subscription.ClusterID
		cancelledAt := sql.NullTime{}
		if subscription.Status == "cancelled" {
			cancelledAt = subscription.CancelledAt
		}
		activeFrom, activeUntil, activeSeconds, monthSeconds := monthlyFeeOverlap(periodStart, periodEnd, subscription.ActiveFrom, cancelledAt)
		if activeSeconds <= 0 {
			continue
		}
		span, exists := spans[clusterID]
		if !exists {
			span = activeSpan{from: activeFrom, until: activeUntil, monthSeconds: monthSeconds}
		}
		if activeFrom.Before(span.from) {
			span.from = activeFrom
		}
		if activeUntil.After(span.until) {
			span.until = activeUntil
		}
		span.seconds += activeSeconds
		spans[clusterID] = span
	}
	clusterIDs := make([]string, 0, len(spans))
	for clusterID := range spans {
		clusterIDs = append(clusterIDs, clusterID)
	}
	sort.Strings(clusterIDs)
	lines := make([]pricedLine, 0, len(spans))
	for _, clusterID := range clusterIDs {
		span := spans[clusterID]
		activeFrom, activeUntil := span.from, span.until
		activeSeconds, monthSeconds := min(span.seconds, span.monthSeconds), span.monthSeconds
		history, historyErr := queries.LoadClusterPricingHistory(ctx, purserdb.LoadClusterPricingHistoryParams{
			ClusterID: clusterID, EffectiveFrom: periodStart,
		})
		if errors.Is(historyErr, sql.ErrNoRows) {
			history, historyErr = queries.LoadClusterPricingHistory(ctx, purserdb.LoadClusterPricingHistoryParams{
				ClusterID: clusterID, EffectiveFrom: periodEnd.Add(-time.Second),
			})
		}
		if historyErr != nil {
			return nil, fmt.Errorf("load monthly pricing for cluster %s: %w", clusterID, historyErr)
		}
		if pricing.Model(history.PricingModel) != pricing.ModelMonthly {
			continue
		}
		monthlyPrice, parseErr := decimal.NewFromString(history.BasePrice)
		if parseErr != nil {
			return nil, fmt.Errorf("parse monthly price for cluster %s: %w", clusterID, parseErr)
		}
		price := monthlyPrice
		description := "Cluster monthly fee"
		if activeSeconds < monthSeconds {
			price = monthlyPrice.Shift(2).Mul(decimal.NewFromInt(activeSeconds)).Div(decimal.NewFromInt(monthSeconds)).Round(0).Shift(-2)
			description = fmt.Sprintf("Cluster monthly fee %s to %s", activeFrom.UTC().Format(time.DateOnly), activeUntil.UTC().Format(time.DateOnly))
		}
		resolved, resolveErr := pricing.ResolveClusterPricing(ctx, pricing.ResolveInputs{
			DB: jm.db, QM: resolver, ConsumingTenantID: tenantID, ClusterID: clusterID,
			AsOf: periodStart, TierRules: tier.Rules, TierCurrency: tier.Currency,
		})
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve cluster %s for monthly fee: %w", clusterID, resolveErr)
		}
		if history.Currency != tier.Currency {
			return nil, fmt.Errorf("cluster %s monthly price is in %s, not %s", clusterID, history.Currency, tier.Currency)
		}
		resolved.PricingSource = pricing.SourceClusterMonthly
		operatorCreditCents, platformFeeCents, splitErr := jm.marketplaceLineSplitCents(ctx, price, resolved)
		if splitErr != nil {
			return nil, fmt.Errorf("compute marketplace split for cluster %s: %w", clusterID, splitErr)
		}
		clusterIDCopy := clusterID
		kind := string(resolved.Kind)
		versionID := history.VersionID
		line := pricedLine{
			LineItem: rating.LineItem{
				LineKey:          clusterLineKey("cluster_monthly", clusterID, periodSuffix),
				Description:      description,
				Quantity:         decimal.NewFromInt(1),
				IncludedQuantity: decimal.Zero,
				BillableQuantity: decimal.NewFromInt(1),
				UnitPrice:        price,
				Amount:           price,
				GrossAmount:      price,
				Currency:         history.Currency,
				Unit:             "month",
			},
			ClusterID:           &clusterIDCopy,
			ClusterKind:         &kind,
			PricingSource:       pricing.SourceClusterMonthly,
			OperatorCreditCents: operatorCreditCents,
			PlatformFeeCents:    platformFeeCents,
			PriceVersionID:      &versionID,
		}
		if resolved.OwnerTenantID != nil {
			owner := *resolved.OwnerTenantID
			line.ClusterOwnerTenantID = &owner
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// monthlyFeeOverlap is the part of [periodStart, periodEnd) a subscription
// active from activeFrom until activeUntil (open when not valid) covers, its
// length in whole seconds, and the seconds of the month starting at
// periodStart. The covered seconds never exceed the month.
func monthlyFeeOverlap(periodStart, periodEnd, activeFrom time.Time, activeUntil sql.NullTime) (from, until time.Time, activeSeconds, monthSeconds int64) {
	from, until = periodStart, periodEnd
	if activeFrom.After(from) {
		from = activeFrom
	}
	if activeUntil.Valid && activeUntil.Time.Before(until) {
		until = activeUntil.Time
	}
	monthSeconds = int64(periodStart.AddDate(0, 1, 0).Sub(periodStart) / time.Second)
	if !until.After(from) {
		return from, until, 0, monthSeconds
	}
	return from, until, min(int64(until.Sub(from)/time.Second), monthSeconds), monthSeconds
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
	bps, err := purserdb.New(jm.db).GetMarketplacePlatformFeeBps(ctx, purserdb.GetMarketplacePlatformFeeBpsParams{
		OwnerID:       uuid.NullUUID{UUID: ownerID, Valid: true},
		PricingSource: sql.NullString{String: string(pricingSource), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query platform_fee_policy: %w", err)
	}
	return int(bps), nil
}

// loadEmailLineItems queries persisted invoice_line_items and shapes them as
// EmailInvoiceLineItem DTOs for email rendering. cluster_name is joined from
// Quartermaster when a clusterID is present; lookup failures degrade the
// label to the cluster ID rather than failing the email entirely.
func (jm *JobManager) loadEmailLineItems(ctx context.Context, invoiceID, tenantID string) ([]EmailInvoiceLineItem, error) {
	rows, err := purserdb.New(jm.db).ListInvoiceEmailLineItems(ctx, purserdb.ListInvoiceEmailLineItemsParams{
		InvoiceID: invoiceID,
		TenantID:  tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("query line items: %w", err)
	}

	type row struct {
		Description, Unit, ClusterID, ClusterKind, Quantity, UnitPrice, Total, Currency, PricingSource string
		DimensionsJSON                                                                                 []byte
	}
	var raw []row
	for _, item := range rows {
		raw = append(raw, row{
			Description: item.Description, Unit: item.Unit, DimensionsJSON: item.Dimensions,
			ClusterID: item.ClusterID, ClusterKind: item.ClusterKind, Quantity: item.Quantity,
			UnitPrice: item.UnitPrice, Total: item.Amount, Currency: item.Currency,
			PricingSource: item.PricingSource,
		})
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
		if jm.billing != nil && jm.billing.qmClient != nil {
			if resp, qmErr := jm.billing.qmClient.GetCluster(ctx, r.ClusterID); qmErr == nil {
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
			Description:    r.Description,
			Unit:           r.Unit,
			DimensionLabel: LineItemDimensionLabel(r.DimensionsJSON),
			ClusterID:      r.ClusterID,
			ClusterName:    clusterNames[r.ClusterID],
			ClusterKind:    r.ClusterKind,
			Quantity:       r.Quantity,
			UnitPrice:      r.UnitPrice,
			Total:          r.Total,
			Currency:       r.Currency,
			PricingSource:  r.PricingSource,
			PricingLabel:   emailPricingLabel(r.PricingSource, r.ClusterKind),
			IsZeroPrice:    isZero,
		})
	}
	return out, nil
}

// LineItemDimensionLabel states an invoice line's dimensions JSON as
// "key: value" pairs in key order, for invoice emails and billing documents.
func LineItemDimensionLabel(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	dimensions := map[string]string{}
	if err := json.Unmarshal(raw, &dimensions); err != nil || len(dimensions) == 0 {
		return ""
	}
	keys := make([]string, 0, len(dimensions))
	for key := range dimensions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strings.ReplaceAll(key, "_", " ")+": "+dimensions[key])
	}
	return strings.Join(parts, " · ")
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
	case "beta_free":
		return "Usage is on us during beta"
	default:
		return ""
	}
}

// pricingResolver returns the package-level Quartermaster client typed as the
// resolver's interface. Returns nil when handlers.Init has not been called
// with a quartermaster client (test paths and tools that don't need rating).
func (jm *JobManager) pricingResolver() pricing.QuartermasterClient {
	if jm.billing.qmClient == nil {
		return nil
	}
	return jm.billing.qmClient
}

// persistManualReviewDraft writes a held draft invoice for ops visibility
// without firing any downstream side effects. The held invoice records no
// prepaid credit, so credit an earlier draft of the period took returns to the
// balance; no period advance, no Stripe meter push. Lines persist so ops can
// see what would have been billed. Resolution flow: ops fixes the cluster
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
	grossMeteredAmt := ratingResult.GrossUsageAmount.Round(2).String()
	creditAmt := decimal.Zero.String()

	var invoiceID string
	var creditChange invoiceCreditChange
	err := withTx(ctx, jm.db, func(tx *sql.Tx) error {
		var txErr error
		creditChange, txErr = reconcileInvoicePrepaidCreditTx(ctx, tx, tenantID, periodStart, 0)
		if txErr != nil {
			return txErr
		}
		heldID, txErr := purserdb.New(tx).UpsertManualReviewInvoice(ctx, purserdb.UpsertManualReviewInvoiceParams{
			TenantID: tenantID, Amount: totalAmt, Currency: currency, DueDate: dueDate,
			BaseAmount: baseAmt, MeteredAmount: meteredAmt, PrepaidCreditApplied: creditAmt,
			PeriodStart:        sql.NullTime{Time: periodStart, Valid: true},
			PeriodEnd:          sql.NullTime{Time: periodEnd, Valid: true},
			GrossMeteredAmount: grossMeteredAmt,
		})
		if txErr != nil {
			return fmt.Errorf("upsert manual_review draft: %w", txErr)
		}
		invoiceID = heldID.String()
		return persistInvoiceLineItems(ctx, tx, invoiceID, tenantID, ratingResult)
	})
	if err != nil {
		return err
	}
	logInvoiceCreditChange(jm.logger, tenantID, invoiceID, periodStart, creditChange)
	return nil
}
