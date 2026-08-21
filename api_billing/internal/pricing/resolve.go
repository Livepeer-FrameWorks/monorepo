package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
)

// QuartermasterClient is the subset of the Quartermaster gRPC client the
// resolver depends on. Tests inject a fake; production wires
// pkg/clients/quartermaster.GRPCClient.
type QuartermasterClient interface {
	GetCluster(ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error)
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
	// one.
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

	// A tenant consuming its own cluster is self-hosted/private usage,
	// regardless of whether that cluster also has marketplace pricing history
	// for other tenants. Keep the line items visible, but price them at zero
	// and never create operator credits for the tenant paying itself.
	if kind == KindTenantPrivate {
		out.Model = ModelFreeUnmetered
		out.Currency = in.TierCurrency
		if row != nil {
			out.PriceVersionID = row.VersionID
			if row.Currency != "" {
				out.Currency = row.Currency
			}
		}
		out.MeteredRules = zeroPricedRulesFromTier(in.TierRules, out.Currency)
		out.PricingSource = SourceSelfHosted
		return out, nil
	}

	// No history row → cluster has never had an explicit pricing config.
	// Platform clusters inherit the tenant tier; marketplace clusters fail
	// closed because operator pricing must be explicit.
	if row == nil {
		switch kind {
		case KindPlatformOfficial:
			out.Model = ModelTierInherit
			out.Currency = in.TierCurrency
			out.MeteredRules = in.TierRules
			out.PricingSource = SourceTier
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
		if err := ValidateMeteredRates(row.MeteredRates, row.Model); err != nil {
			return nil, fmt.Errorf("metered rates for %s: %w", in.ClusterID, err)
		}
		rules, err := buildMeteredRules(row.MeteredRates, out.Currency)
		if err != nil {
			return nil, fmt.Errorf("metered rules for %s: %w", in.ClusterID, err)
		}
		out.MeteredRules = rules
		out.PricingSource = SourceClusterMetered

	case ModelMonthly:
		// Monthly cluster access is charged outside metered usage. Metered
		// usage on a monthly cluster rates as zero-priced informational lines
		// so it still appears on the invoice.
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
		if err := ValidateMeteredRates(row.MeteredRates, row.Model); err != nil {
			if errors.Is(err, ErrCustomPricingMissingForCluster) {
				return nil, err
			}
			return nil, fmt.Errorf("custom rates for %s: %w", in.ClusterID, err)
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
	if o.OwnerTenantID != nil && o.OwnerTenantID.String() == consumingTenantID {
		return KindTenantPrivate, nil
	}
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
// (nil, nil) when no row exists for the cluster.
func loadHistoryRow(ctx context.Context, db *sql.DB, clusterID string, asOf time.Time) (*historyRow, error) {
	row, err := purserdb.New(db).LoadClusterPricingHistory(ctx, purserdb.LoadClusterPricingHistoryParams{
		ClusterID:     clusterID,
		EffectiveFrom: asOf,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	bp, err := decimal.NewFromString(row.BasePrice)
	if err != nil {
		return nil, fmt.Errorf("parse base_price %q: %w", row.BasePrice, err)
	}
	out := &historyRow{
		VersionID: row.VersionID,
		Model:     Model(row.PricingModel),
		BasePrice: bp,
	}
	out.Currency = row.Currency
	if row.MeteredRates != "" && row.MeteredRates != "{}" {
		var rates map[string]any
		if err := json.Unmarshal([]byte(row.MeteredRates), &rates); err != nil {
			return nil, fmt.Errorf("parse metered_rates: %w", err)
		}
		out.MeteredRates = rates
	}
	return out, nil
}

func ValidateMeteredRates(rates map[string]any, clusterModel Model) error {
	switch clusterModel {
	case ModelMetered, ModelCustom:
		if len(rates) == 0 {
			if clusterModel == ModelCustom {
				return ErrCustomPricingMissingForCluster
			}
			return errors.New("metered pricing requires at least one rate")
		}
	}
	for meter, raw := range rates {
		m := rating.Meter(meter)
		if !rating.ValidMeter(m) {
			return fmt.Errorf("invalid meter %q in metered_rates", meter)
		}
		row, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("metered_rates[%q] must be an object", meter)
		}
		modelStr, ok := row["model"].(string)
		if !ok || modelStr == "" {
			return fmt.Errorf("metered_rates[%q].model: required", meter)
		}
		if _, ok := row["unit_price"]; !ok || row["unit_price"] == nil {
			return fmt.Errorf("metered_rates[%q].unit_price: required", meter)
		}
		unitPrice, err := decimalField(row, "unit_price")
		if err != nil {
			return fmt.Errorf("metered_rates[%q].unit_price: %w", meter, err)
		}
		included, err := decimalField(row, "included_quantity")
		if err != nil {
			return fmt.Errorf("metered_rates[%q].included_quantity: %w", meter, err)
		}
		cfg, err := configMapField(row, "config")
		if err != nil {
			return fmt.Errorf("metered_rates[%q].config: %w", meter, err)
		}
		if err := rating.ValidateRuleShape(rating.Rule{
			Meter:            m,
			Model:            rating.Model(modelStr),
			IncludedQuantity: included,
			UnitPrice:        unitPrice,
			Config:           cfg,
		}); err != nil {
			return fmt.Errorf("metered_rates[%q]: %w", meter, err)
		}
	}
	return nil
}

// buildMeteredRules converts purser.cluster_pricing.metered_rates JSON into
// rating.Rule values. The expected JSON shape is:
//
//	{
//	  "delivered_minutes":   {"unit_price": "0.00050", "model": "tiered_graduated", "included_quantity": "0"},
//	  "storage_gb_seconds_hot":  {"unit_price": "0.0",     "model": "tiered_graduated"},
//	  ...
//	}
//
// model and unit_price are required.
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
			return nil, fmt.Errorf("pricing: invalid meter %q in metered_rates", meter)
		}
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("pricing: metered_rates[%q] must be an object", meter)
		}
		modelStr, ok := row["model"].(string)
		if !ok || modelStr == "" {
			return nil, fmt.Errorf("pricing: metered_rates[%q].model: required", meter)
		}
		if _, ok := row["unit_price"]; !ok || row["unit_price"] == nil {
			return nil, fmt.Errorf("pricing: metered_rates[%q].unit_price: required", meter)
		}
		unitPrice, err := decimalField(row, "unit_price")
		if err != nil {
			return nil, fmt.Errorf("pricing: metered_rates[%q].unit_price: %w", meter, err)
		}
		included, err := decimalField(row, "included_quantity")
		if err != nil {
			return nil, fmt.Errorf("pricing: metered_rates[%q].included_quantity: %w", meter, err)
		}
		cfg, err := configMapField(row, "config")
		if err != nil {
			return nil, fmt.Errorf("pricing: metered_rates[%q].config: %w", meter, err)
		}
		rule := rating.Rule{
			Meter:            m,
			Model:            rating.Model(modelStr),
			Currency:         currency,
			IncludedQuantity: included,
			UnitPrice:        unitPrice,
			Config:           cfg,
		}
		if err := rating.ValidateRule(rule); err != nil {
			return nil, fmt.Errorf("pricing: metered_rates[%q]: %w", meter, err)
		}
		out = append(out, rule)
	}
	return out, nil
}

func configMapField(m map[string]any, key string) (map[string]any, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil, nil
	}
	cfg, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be an object")
	}
	return cfg, nil
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
		if zero.Model == rating.ModelDimensioned {
			zero.Config = zeroDimensionRates(r.Config)
		}
		out = append(out, zero)
	}
	return out
}

func zeroDimensionRates(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	out := make(map[string]any, len(config))
	for key, value := range config {
		out[key] = value
	}
	rawRates, ok := config["rates"].([]any)
	if !ok {
		return out
	}
	rates := make([]any, 0, len(rawRates))
	for _, rawRate := range rawRates {
		rate, ok := rawRate.(map[string]any)
		if !ok {
			rates = append(rates, rawRate)
			continue
		}
		copyRate := make(map[string]any, len(rate))
		for key, value := range rate {
			copyRate[key] = value
		}
		copyRate["unit_price"] = "0"
		rates = append(rates, copyRate)
	}
	out["rates"] = rates
	return out
}
