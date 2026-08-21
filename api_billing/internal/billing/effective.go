// Package billing holds shared application-layer helpers used by purser
// handlers and the gRPC server. The rating engine itself lives in
// api_billing/internal/rating; this package translates DB state into
// rating.Input.
package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/rating"
)

// EffectiveTier is the result of resolving a tenant's tier and per-tenant
// overrides into a single rated configuration. Currency, base price, and
// rules are everything the rating engine needs.
type EffectiveTier struct {
	TierID          string
	TierName        string
	Currency        string
	BasePrice       decimal.Decimal
	MeteringEnabled bool
	Rules           []rating.Rule
	Entitlements    map[string]string // JSON-encoded values, keyed by entitlement key
}

// LoadEffectiveTier loads a tenant's tier configuration and applies their
// subscription-level overrides. The returned EffectiveTier is read-only.
//
// Override semantics:
//   - subscription_pricing_overrides shadow tier_pricing_rules per (meter):
//     a row in the override table replaces the tier rule wholesale; partial
//     fields fall back to the tier rule's values.
//   - subscription_entitlement_overrides shadow tier_entitlements per (key)
//     the same way.
//
// If the tenant has no active subscription, returns sql.ErrNoRows.
func LoadEffectiveTier(ctx context.Context, db *sql.DB, tenantID string) (*EffectiveTier, error) {
	if db == nil {
		return nil, errors.New("LoadEffectiveTier: nil db")
	}
	if tenantID == "" {
		return nil, errors.New("LoadEffectiveTier: empty tenant_id")
	}

	row, err := purserdb.New(db).LoadActiveEffectiveTier(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	bp, err := decimal.NewFromString(row.BasePrice)
	if err != nil {
		return nil, fmt.Errorf("parse base_price %q: %w", row.BasePrice, err)
	}

	rules, err := loadTierRules(ctx, db, row.TierID.String())
	if err != nil {
		return nil, fmt.Errorf("load tier rules: %w", err)
	}
	rules, err = applyPricingOverrides(ctx, db, row.SubscriptionID.String(), rules)
	if err != nil {
		return nil, fmt.Errorf("apply pricing overrides: %w", err)
	}

	entitlements, err := loadTierEntitlements(ctx, db, row.TierID.String())
	if err != nil {
		return nil, fmt.Errorf("load entitlements: %w", err)
	}
	entitlements, err = applyEntitlementOverrides(ctx, db, row.SubscriptionID.String(), entitlements)
	if err != nil {
		return nil, fmt.Errorf("apply entitlement overrides: %w", err)
	}

	return &EffectiveTier{
		TierID:          row.TierID.String(),
		TierName:        row.TierName,
		Currency:        row.Currency,
		BasePrice:       bp,
		MeteringEnabled: row.MeteringEnabled,
		Rules:           rules,
		Entitlements:    entitlements,
	}, nil
}

func loadTierRules(ctx context.Context, db *sql.DB, tierID string) ([]rating.Rule, error) {
	rows, err := purserdb.New(db).ListTierPricingRules(ctx, tierID)
	if err != nil {
		return nil, err
	}
	rules := make([]rating.Rule, 0, len(rows))
	for _, row := range rows {
		r, err := parseRuleFields(row.Meter, row.Model, row.Currency, row.IncludedQuantity, row.UnitPrice, row.Config)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func applyPricingOverrides(ctx context.Context, db *sql.DB, subscriptionID string, base []rating.Rule) ([]rating.Rule, error) {
	rows, err := purserdb.New(db).ListSubscriptionPricingOverrides(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}

	overrides := map[string]rating.Rule{}
	for _, row := range rows {
		if row.Meter == "" {
			continue
		}
		// Find the base rule for this meter to fill in missing fields.
		var baseRule rating.Rule
		for _, r := range base {
			if string(r.Meter) == row.Meter {
				baseRule = r
				break
			}
		}
		merged := baseRule
		merged.Meter = rating.Meter(row.Meter)
		if row.Model.Valid && row.Model.String != "" {
			merged.Model = rating.Model(row.Model.String)
		}
		if row.Currency.Valid && row.Currency.String != "" {
			merged.Currency = row.Currency.String
		}
		if row.IncludedQuantity.Valid && row.IncludedQuantity.String != "" {
			d, err := decimal.NewFromString(row.IncludedQuantity.String)
			if err != nil {
				return nil, fmt.Errorf("override included_quantity for %q: %w", row.Meter, err)
			}
			merged.IncludedQuantity = d
		}
		if row.UnitPrice.Valid && row.UnitPrice.String != "" {
			d, err := decimal.NewFromString(row.UnitPrice.String)
			if err != nil {
				return nil, fmt.Errorf("override unit_price for %q: %w", row.Meter, err)
			}
			merged.UnitPrice = d
		}
		if config := string(row.Config); config != "" && config != "{}" {
			cfg, err := decodeJSONMap(config)
			if err != nil {
				return nil, fmt.Errorf("override config for %q: %w", row.Meter, err)
			}
			merged.Config = cfg
		}
		if err := validateEffectiveRule(merged); err != nil {
			return nil, fmt.Errorf("pricing override for %q: %w", row.Meter, err)
		}
		overrides[row.Meter] = merged
	}

	out := make([]rating.Rule, 0, len(base)+len(overrides))
	seen := map[string]bool{}
	for _, r := range base {
		if override, ok := overrides[string(r.Meter)]; ok {
			out = append(out, override)
		} else {
			out = append(out, r)
		}
		seen[string(r.Meter)] = true
	}
	// Subscription overrides may add a meter not on the tier. Append those.
	for meter, override := range overrides {
		if seen[meter] {
			continue
		}
		out = append(out, override)
	}
	return out, nil
}

func loadTierEntitlements(ctx context.Context, db *sql.DB, tierID string) (map[string]string, error) {
	rows, err := purserdb.New(db).ListTierEntitlements(ctx, tierID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Key] = row.Value
	}
	return out, nil
}

func applyEntitlementOverrides(ctx context.Context, db *sql.DB, subscriptionID string, base map[string]string) (map[string]string, error) {
	rows, err := purserdb.New(db).ListSubscriptionEntitlementOverrides(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(base))
	maps.Copy(out, base)
	for _, row := range rows {
		out[row.Key] = row.Value
	}
	return out, nil
}

func parseRuleFields(meter, model, currency, included, unitPrice, config string) (rating.Rule, error) {
	includedDec, err := decimal.NewFromString(included)
	if err != nil {
		return rating.Rule{}, fmt.Errorf("included_quantity %q: %w", included, err)
	}
	unitPriceDec, err := decimal.NewFromString(unitPrice)
	if err != nil {
		return rating.Rule{}, fmt.Errorf("unit_price %q: %w", unitPrice, err)
	}
	var cfg map[string]any
	if config != "" && config != "{}" {
		cfg, err = decodeJSONMap(config)
		if err != nil {
			return rating.Rule{}, fmt.Errorf("config %q: %w", config, err)
		}
	}
	rule := rating.Rule{
		Meter:            rating.Meter(meter),
		Model:            rating.Model(model),
		Currency:         currency,
		IncludedQuantity: includedDec,
		UnitPrice:        unitPriceDec,
		Config:           cfg,
	}
	if err := validateEffectiveRule(rule); err != nil {
		return rating.Rule{}, err
	}
	return rule, nil
}

func validateEffectiveRule(rule rating.Rule) error {
	return rating.ValidateRule(rule)
}

func decodeJSONMap(s string) (map[string]any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}
