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

	var (
		tierID, tierName, currency string
		basePrice                  string
		meteringEnabled            bool
		subscriptionID             sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT bt.id, bt.tier_name, bt.base_price::text, bt.currency, bt.metering_enabled,
		       ts.id
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
		WHERE ts.tenant_id = $1 AND ts.status = 'active'
		ORDER BY ts.created_at DESC
		LIMIT 1
	`, tenantID).Scan(&tierID, &tierName, &basePrice, &currency, &meteringEnabled, &subscriptionID)
	if err != nil {
		return nil, err
	}

	bp, err := decimal.NewFromString(basePrice)
	if err != nil {
		return nil, fmt.Errorf("parse base_price %q: %w", basePrice, err)
	}

	rules, err := loadTierRules(ctx, db, tierID)
	if err != nil {
		return nil, fmt.Errorf("load tier rules: %w", err)
	}
	if subscriptionID.Valid && subscriptionID.String != "" {
		rules, err = applyPricingOverrides(ctx, db, subscriptionID.String, rules)
		if err != nil {
			return nil, fmt.Errorf("apply pricing overrides: %w", err)
		}
	}

	entitlements, err := loadTierEntitlements(ctx, db, tierID)
	if err != nil {
		return nil, fmt.Errorf("load entitlements: %w", err)
	}
	if subscriptionID.Valid && subscriptionID.String != "" {
		entitlements, err = applyEntitlementOverrides(ctx, db, subscriptionID.String, entitlements)
		if err != nil {
			return nil, fmt.Errorf("apply entitlement overrides: %w", err)
		}
	}

	return &EffectiveTier{
		TierID:          tierID,
		TierName:        tierName,
		Currency:        currency,
		BasePrice:       bp,
		MeteringEnabled: meteringEnabled,
		Rules:           rules,
		Entitlements:    entitlements,
	}, nil
}

func loadTierRules(ctx context.Context, db *sql.DB, tierID string) ([]rating.Rule, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT meter, model, currency, included_quantity::text, unit_price::text, config::text
		FROM purser.tier_pricing_rules
		WHERE tier_id = $1
	`, tierID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []rating.Rule
	for rows.Next() {
		r, err := scanRule(rows.Scan)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func applyPricingOverrides(ctx context.Context, db *sql.DB, subscriptionID string, base []rating.Rule) ([]rating.Rule, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT meter, model, currency, included_quantity::text, unit_price::text, config::text
		FROM purser.subscription_pricing_overrides
		WHERE subscription_id = $1
	`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	overrides := map[string]rating.Rule{}
	for rows.Next() {
		var meter, model, currency, included, unitPrice, config sql.NullString
		if err := rows.Scan(&meter, &model, &currency, &included, &unitPrice, &config); err != nil {
			return nil, err
		}
		if !meter.Valid || meter.String == "" {
			continue
		}
		// Find the base rule for this meter to fill in missing fields.
		var baseRule rating.Rule
		for _, r := range base {
			if string(r.Meter) == meter.String {
				baseRule = r
				break
			}
		}
		merged := baseRule
		merged.Meter = rating.Meter(meter.String)
		if model.Valid && model.String != "" {
			merged.Model = rating.Model(model.String)
		}
		if currency.Valid && currency.String != "" {
			merged.Currency = currency.String
		}
		if included.Valid && included.String != "" {
			d, err := decimal.NewFromString(included.String)
			if err != nil {
				return nil, fmt.Errorf("override included_quantity for %q: %w", meter.String, err)
			}
			merged.IncludedQuantity = d
		}
		if unitPrice.Valid && unitPrice.String != "" {
			d, err := decimal.NewFromString(unitPrice.String)
			if err != nil {
				return nil, fmt.Errorf("override unit_price for %q: %w", meter.String, err)
			}
			merged.UnitPrice = d
		}
		if config.Valid && config.String != "" && config.String != "{}" {
			cfg, err := decodeJSONMap(config.String)
			if err != nil {
				return nil, fmt.Errorf("override config for %q: %w", meter.String, err)
			}
			merged.Config = cfg
		}
		if err := validateEffectiveRule(merged); err != nil {
			return nil, fmt.Errorf("pricing override for %q: %w", meter.String, err)
		}
		overrides[meter.String] = merged
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	rows, err := db.QueryContext(ctx, `SELECT key, value::text FROM purser.tier_entitlements WHERE tier_id = $1`, tierID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func applyEntitlementOverrides(ctx context.Context, db *sql.DB, subscriptionID string, base map[string]string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value::text FROM purser.subscription_entitlement_overrides WHERE subscription_id = $1`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, len(base))
	maps.Copy(out, base)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// scanRule reads one purser.tier_pricing_rules row and validates the rule exactly
// as stored. Malformed catalog rows must fail the caller instead of being repaired
// at read time.
func scanRule(scan func(...any) error) (rating.Rule, error) {
	var meter, model, currency, included, unitPrice, config string
	if err := scan(&meter, &model, &currency, &included, &unitPrice, &config); err != nil {
		return rating.Rule{}, err
	}
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
	if !rating.ValidMeter(rule.Meter) {
		return fmt.Errorf("unsupported meter %q", rule.Meter)
	}
	if !rating.ValidModel(rule.Model) {
		return fmt.Errorf("unsupported model %q", rule.Model)
	}
	if rule.Currency == "" {
		return fmt.Errorf("currency is required")
	}
	return nil
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
