package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/pricing"
)

// ReconcileClusterPricing upserts every ClusterPricing row in desired into
// purser.cluster_pricing. The Stripe-owned columns (stripe_product_id,
// stripe_price_id_monthly, stripe_meter_event_name) are owned by Purser's
// startup Stripe sync and are NEVER touched here — the upsert uses COALESCE
// so existing values survive.
//
// Stable key: cluster_id. Result reports created/updated/noop per row so callers
// can assert the idempotency contract on a re-run.
func ReconcileClusterPricing(ctx context.Context, exec DBTX, desired []ClusterPricing) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcileClusterPricing: nil executor")
	}

	res := Result{}
	for _, cp := range desired {
		if cp.ClusterID == "" {
			return Result{}, errors.New("ReconcileClusterPricing: cluster_id required")
		}
		if !validPricingModel(cp.PricingModel) {
			return Result{}, fmt.Errorf("ReconcileClusterPricing: cluster %q: invalid pricing_model %q", cp.ClusterID, cp.PricingModel)
		}
		if err := pricing.ValidateMeteredRates(cp.MeteredRates, pricing.Model(cp.PricingModel)); err != nil {
			return Result{}, fmt.Errorf("ReconcileClusterPricing: cluster %q: %w", cp.ClusterID, err)
		}
		action, err := upsertClusterPricing(ctx, exec, cp)
		if err != nil {
			return Result{}, fmt.Errorf("upsert cluster_pricing %q: %w", cp.ClusterID, err)
		}
		switch action {
		case "created":
			res.Created = append(res.Created, cp.ClusterID)
		case "updated":
			res.Updated = append(res.Updated, cp.ClusterID)
		case "noop":
			res.Noop = append(res.Noop, cp.ClusterID)
		}
	}

	return res, nil
}

func upsertClusterPricing(ctx context.Context, exec DBTX, cp ClusterPricing) (string, error) {
	metered, err := jsonBytes(cp.MeteredRates)
	if err != nil {
		return "", fmt.Errorf("metered_rates: %w", err)
	}
	quotas, err := jsonBytes(cp.DefaultQuotas)
	if err != nil {
		return "", fmt.Errorf("default_quotas: %w", err)
	}

	basePrice, err := parseMoney(cp.BasePrice)
	if err != nil {
		return "", fmt.Errorf("base_price: %w", err)
	}
	currency := cp.Currency
	if currency == "" {
		currency = "EUR"
	}
	requiredTier := int32(0)
	if cp.RequiredTierLevel != nil {
		requiredTier = *cp.RequiredTierLevel
	}
	allowFree := false
	if cp.AllowFreeTier != nil {
		allowFree = *cp.AllowFreeTier
	}

	queries := purserdb.New(exec)
	current, err := queries.GetBootstrapClusterPricing(ctx, cp.ClusterID)
	exists := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", fmt.Errorf("probe cluster_pricing: %w", err)
	default:
		exists = true
	}

	if !exists {
		if err := queries.InsertBootstrapClusterPricing(ctx, purserdb.InsertBootstrapClusterPricingParams{
			ClusterID: cp.ClusterID, PricingModel: cp.PricingModel, BasePrice: moneyText(basePrice),
			Currency:          sql.NullString{String: currency, Valid: true},
			RequiredTierLevel: sql.NullInt32{Int32: requiredTier, Valid: true},
			AllowFreeTier:     sql.NullBool{Bool: allowFree, Valid: true},
			MeteredRates:      json.RawMessage(metered), DefaultQuotas: json.RawMessage(quotas),
		}); err != nil {
			return "", err
		}
		return "created", nil
	}

	if current.PricingModel == cp.PricingModel &&
		moneyEq(current.BasePrice, basePrice) &&
		current.Currency == currency &&
		current.RequiredTierLevel == requiredTier &&
		current.AllowFreeTier == allowFree &&
		jsonEq(string(current.MeteredRates), metered) &&
		jsonEq(string(current.DefaultQuotas), quotas) {
		return "noop", nil
	}

	if err := queries.UpdateBootstrapClusterPricing(ctx, purserdb.UpdateBootstrapClusterPricingParams{
		ClusterID: cp.ClusterID, PricingModel: cp.PricingModel, BasePrice: moneyText(basePrice),
		Currency:          sql.NullString{String: currency, Valid: true},
		RequiredTierLevel: sql.NullInt32{Int32: requiredTier, Valid: true},
		AllowFreeTier:     sql.NullBool{Bool: allowFree, Valid: true},
		MeteredRates:      json.RawMessage(metered), DefaultQuotas: json.RawMessage(quotas),
	}); err != nil {
		return "", err
	}
	return "updated", nil
}

func validPricingModel(m string) bool {
	switch m {
	case "free_unmetered", "metered", "monthly", "tier_inherit", "custom":
		return true
	}
	return false
}

// parseMoney converts a manifest-supplied price string into a NUMERIC value
// driver-suitable. Empty strings are 0.00; everything else must be a single
// non-negative decimal literal with up to four fractional digits and nothing
// else.
//
// The regex+ParseFloat combo rejects every shape `fmt.Sscanf("%f")` silently
// accepted: trailing garbage (`10oops`), exponents (`1e3`), `Inf`/`NaN`,
// multiple tokens, leading whitespace, and negative amounts (no negative
// price makes sense for billing).
var moneyRE = regexp.MustCompile(`^\d+(\.\d{1,4})?$`)

func parseMoney(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	if !moneyRE.MatchString(s) {
		return 0, fmt.Errorf("invalid money %q: expected `-?\\d+(\\.\\d{1,4})?`", s)
	}
	return strconv.ParseFloat(s, 64)
}
