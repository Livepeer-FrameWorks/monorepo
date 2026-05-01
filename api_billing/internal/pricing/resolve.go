package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
	pb "frameworks/pkg/proto"
)

// QuartermasterClient is the subset of the Quartermaster gRPC client the
// resolver depends on. Tests inject a fake; production wires
// pkg/clients/quartermaster.GRPCClient.
type QuartermasterClient interface {
	GetCluster(ctx context.Context, clusterID string) (*pb.ClusterResponse, error)
}

// ResolveInputs is the read-only input to ResolveClusterPricing. Bundling the
// fields in a struct keeps the call site self-documenting and lets us add new
// optional parameters without breaking signatures.
type ResolveInputs struct {
	DB                *sql.DB
	QM                QuartermasterClient
	ConsumingTenantID string
	ClusterID         string
	AsOf              time.Time

	// TierRules are the tenant's tier-level rating rules (already overridden
	// per subscription if applicable). The resolver returns these unchanged
	// for tier_inherit clusters; for other models it substitutes
	// cluster-derived rules.
	TierRules []rating.Rule

	// TierCurrency is the currency from the tenant's effective tier — used as
	// the currency for cluster-priced rules whose stored row does not carry
	// one (e.g. legacy rows).
	TierCurrency string
}

// ResolveClusterPricing resolves the pricing configuration for one
// (tenant, cluster, period) tuple. See package docs for semantics.
func ResolveClusterPricing(ctx context.Context, in ResolveInputs) (*ClusterPricing, error) {
	if in.DB == nil {
		return nil, errors.New("pricing: nil DB")
	}
	if in.QM == nil {
		return nil, errors.New("pricing: nil Quartermaster client")
	}
	if in.ConsumingTenantID == "" {
		return nil, errors.New("pricing: empty consuming tenant id")
	}
	if in.ClusterID == "" {
		return nil, errors.New("pricing: empty cluster id")
	}
	if in.AsOf.IsZero() {
		return nil, errors.New("pricing: zero AsOf")
	}

	ownership, err := loadOwnership(ctx, in.QM, in.ClusterID)
	if err != nil {
		return nil, fmt.Errorf("load ownership for %s: %w", in.ClusterID, err)
	}
	kind, classifyErr := classify(ownership, in.ConsumingTenantID)
	if classifyErr != nil {
		return nil, fmt.Errorf("classify cluster %s: %w", in.ClusterID, classifyErr)
	}

	row, err := loadHistoryRow(ctx, in.DB, in.ClusterID, in.AsOf)
	if err != nil {
		return nil, fmt.Errorf("load pricing history for %s: %w", in.ClusterID, err)
	}

	out := &ClusterPricing{
		Kind:               kind,
		OwnerTenantID:      ownership.OwnerTenantID,
		IsPlatformOfficial: ownership.IsPlatformOfficial,
	}

	// No history row → cluster has never had an explicit pricing config.
	// Preserve legacy behavior for platform clusters, treat tenant-owned
	// infrastructure as self-hosted/free, and fail closed for marketplace
	// clusters because operator pricing must be explicit.
	if row == nil {
		switch kind {
		case KindPlatformOfficial:
			out.Model = ModelTierInherit
			out.Currency = in.TierCurrency
			out.MeteredRules = in.TierRules
			out.PricingSource = SourceTier
		case KindTenantPrivate:
			out.Model = ModelFreeUnmetered
			out.Currency = in.TierCurrency
			out.MeteredRules = zeroPricedRulesFromTier(in.TierRules, in.TierCurrency)
			out.PricingSource = SourceSelfHosted
		case KindThirdPartyMarketplace:
			return nil, ErrThirdPartyPricingMissing
		default:
			return nil, fmt.Errorf("pricing: unsupported cluster kind %q", kind)
		}
		return out, nil
	}

	out.PriceVersionID = row.VersionID
	out.Model = row.Model
	out.Currency = row.Currency
	if out.Currency == "" {
		out.Currency = in.TierCurrency
	}

	switch row.Model {
	case ModelTierInherit:
		out.MeteredRules = in.TierRules
		out.PricingSource = SourceTier

	case ModelMetered:
		rules, err := buildMeteredRules(row.MeteredRates, out.Currency)
		if err != nil {
			return nil, fmt.Errorf("metered rules for %s: %w", in.ClusterID, err)
		}
		out.MeteredRules = rules
		out.PricingSource = SourceClusterMetered

	case ModelMonthly:
		// Access-only this pass per the plan: no usage line, no operator
		// settlement. Metered usage on a monthly cluster (if anyone
		// configures both) rates as zero-priced informational lines so it
		// still appears on the invoice.
		out.MeteredRules = zeroPricedRulesFromTier(in.TierRules, out.Currency)
		out.PricingSource = SourceIncludedSubscription

	case ModelFreeUnmetered:
		// Zero-priced informational lines per the invariant: usage stays
		// visible to the customer at $0.00 rather than disappearing.
		out.MeteredRules = zeroPricedRulesFromTier(in.TierRules, out.Currency)
		if kind == KindTenantPrivate {
			out.PricingSource = SourceSelfHosted
		} else {
			out.PricingSource = SourceFreeUnmetered
		}

	case ModelCustom:
		if len(row.MeteredRates) == 0 {
			return nil, ErrCustomPricingMissingForCluster
		}
		rules, err := buildMeteredRules(row.MeteredRates, out.Currency)
		if err != nil {
			return nil, fmt.Errorf("custom rules for %s: %w", in.ClusterID, err)
		}
		out.MeteredRules = rules
		out.PricingSource = SourceClusterCustom

	default:
		return nil, fmt.Errorf("pricing: unsupported cluster pricing model %q for cluster %s", row.Model, in.ClusterID)
	}

	return out, nil
}

// ownership is the projection of pb.InfrastructureCluster needed for kind
// classification.
type ownership struct {
	OwnerTenantID      *uuid.UUID
	IsPlatformOfficial bool
}

func loadOwnership(ctx context.Context, qm QuartermasterClient, clusterID string) (ownership, error) {
	resp, err := qm.GetCluster(ctx, clusterID)
	if err != nil {
		return ownership{}, err
	}
	c := resp.GetCluster()
	if c == nil {
		return ownership{}, fmt.Errorf("cluster %s not found", clusterID)
	}
	out := ownership{IsPlatformOfficial: c.GetIsPlatformOfficial()}
	if owner := c.GetOwnerTenantId(); owner != "" {
		id, err := uuid.Parse(owner)
		if err != nil {
			return ownership{}, fmt.Errorf("parse owner_tenant_id %q: %w", owner, err)
		}
		out.OwnerTenantID = &id
	}
	return out, nil
}

func classify(o ownership, consumingTenantID string) (ClusterKind, error) {
	if o.IsPlatformOfficial {
		return KindPlatformOfficial, nil
	}
	if o.OwnerTenantID == nil {
		// Non-platform with no owner is a misconfiguration. Failing open
		// (treating as platform_official) would silently waive operator
		// credit, hiding marketplace revenue. Fail closed: callers route
		// to manual_review.
		return "", ErrAmbiguousClusterOwnership
	}
	if o.OwnerTenantID.String() == consumingTenantID {
		return KindTenantPrivate, nil
	}
	return KindThirdPartyMarketplace, nil
}

// historyRow projects the columns the resolver needs from
// purser.cluster_pricing_history.
type historyRow struct {
	VersionID    uuid.UUID
	Model        Model
	Currency     string
	BasePrice    decimal.Decimal
	MeteredRates map[string]any
}

// loadHistoryRow fetches the pricing config effective at asOf. Returns
// (nil, nil) when no row exists for the cluster (legacy clusters).
func loadHistoryRow(ctx context.Context, db *sql.DB, clusterID string, asOf time.Time) (*historyRow, error) {
	const q = `
		SELECT version_id, pricing_model, currency, base_price::text, metered_rates::text
		FROM purser.cluster_pricing_history
		WHERE cluster_id = $1
		  AND effective_from <= $2
		  AND (effective_to IS NULL OR effective_to > $2)
		ORDER BY effective_from DESC
		LIMIT 1
	`
	var (
		versionStr   string
		modelStr     string
		currency     sql.NullString
		basePriceStr string
		ratesStr     sql.NullString
	)
	err := db.QueryRowContext(ctx, q, clusterID, asOf).Scan(&versionStr, &modelStr, &currency, &basePriceStr, &ratesStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	versionID, err := uuid.Parse(versionStr)
	if err != nil {
		return nil, fmt.Errorf("parse version_id %q: %w", versionStr, err)
	}
	bp, err := decimal.NewFromString(basePriceStr)
	if err != nil {
		return nil, fmt.Errorf("parse base_price %q: %w", basePriceStr, err)
	}
	out := &historyRow{
		VersionID: versionID,
		Model:     Model(modelStr),
		BasePrice: bp,
	}
	if currency.Valid {
		out.Currency = currency.String
	}
	if ratesStr.Valid && ratesStr.String != "" && ratesStr.String != "{}" {
		var rates map[string]any
		if err := json.Unmarshal([]byte(ratesStr.String), &rates); err != nil {
			return nil, fmt.Errorf("parse metered_rates: %w", err)
		}
		out.MeteredRates = rates
	}
	return out, nil
}

// buildMeteredRules converts purser.cluster_pricing.metered_rates JSON into
// rating.Rule values. The expected JSON shape is:
//
//	{
//	  "delivered_minutes":   {"unit_price": "0.00050", "model": "tiered_graduated", "included_quantity": "0"},
//	  "average_storage_gb":  {"unit_price": "0.0",     "model": "tiered_graduated"},
//	  ...
//	}
//
// model defaults to all_usage if absent. unit_price is required.
func buildMeteredRules(rates map[string]any, currency string) ([]rating.Rule, error) {
	if currency == "" {
		return nil, errors.New("pricing: currency is required to build metered rules")
	}
	if len(rates) == 0 {
		return nil, nil
	}
	out := make([]rating.Rule, 0, len(rates))
	for meter, raw := range rates {
		m := rating.Meter(meter)
		if !rating.ValidMeter(m) {
			return nil, fmt.Errorf("pricing: unsupported meter %q in metered_rates", meter)
		}
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("pricing: metered_rates[%q] must be an object", meter)
		}
		modelStr, ok := row["model"].(string)
		if !ok || modelStr == "" {
			modelStr = string(rating.ModelAllUsage)
		}
		unitPrice, err := decimalField(row, "unit_price")
		if err != nil {
			return nil, fmt.Errorf("pricing: metered_rates[%q].unit_price: %w", meter, err)
		}
		included, err := decimalField(row, "included_quantity")
		if err != nil {
			return nil, fmt.Errorf("pricing: metered_rates[%q].included_quantity: %w", meter, err)
		}
		var cfg map[string]any
		if c, ok := row["config"].(map[string]any); ok {
			cfg = c
		}
		out = append(out, rating.Rule{
			Meter:            m,
			Model:            rating.Model(modelStr),
			Currency:         currency,
			IncludedQuantity: included,
			UnitPrice:        unitPrice,
			Config:           cfg,
		})
	}
	return out, nil
}

func decimalField(m map[string]any, key string) (decimal.Decimal, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return decimal.Zero, nil
	}
	switch v := raw.(type) {
	case string:
		return decimal.NewFromString(v)
	case float64:
		return decimal.NewFromFloat(v), nil
	case int:
		return decimal.NewFromInt(int64(v)), nil
	case int64:
		return decimal.NewFromInt(v), nil
	default:
		return decimal.Zero, fmt.Errorf("unsupported numeric type %T", raw)
	}
}

// zeroPricedRulesFromTier returns a rule per tier meter with unit_price
// forced to zero. This is the mechanism behind the
// "free/self-hosted usage produces an informational line, never empty" rule:
// the rating engine still emits a line because included_quantity stays zero
// and quantity > 0 → billable_quantity > 0 → line is rendered with $0.00.
//
// codec_multiplier rules are converted to all_usage at zero price so the
// engine reads from Usage[processing_seconds] (the writer populates this)
// rather than CodecSeconds. Without this conversion the rating engine's
// codec-multiplier path early-exits when unit_price is zero, dropping the
// informational line and silently hiding self-hosted/free transcoding.
// See rateCodecMultiplier in api_billing/internal/rating/rate.go.
func zeroPricedRulesFromTier(tierRules []rating.Rule, currency string) []rating.Rule {
	if len(tierRules) == 0 {
		return nil
	}
	out := make([]rating.Rule, 0, len(tierRules))
	for _, r := range tierRules {
		zero := r
		zero.UnitPrice = decimal.Zero
		zero.IncludedQuantity = decimal.Zero
		zero.Currency = currency
		if zero.Model == rating.ModelCodecMultiplier {
			zero.Model = rating.ModelAllUsage
			zero.Config = nil
		}
		out = append(out, zero)
	}
	return out
}
