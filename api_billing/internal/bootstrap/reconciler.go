package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
)

// Result describes what a reconciler did per row. Returned aggregated so callers
// can log a summary and CI can assert idempotency (a second run returns all noop).
type Result struct {
	Created []string
	Updated []string
	Noop    []string
}

// Total returns the number of rows the reconciler considered.
func (r Result) Total() int { return len(r.Created) + len(r.Updated) + len(r.Noop) }

// ReconcileBillingTierCatalog upserts every CatalogTier into purser.billing_tiers,
// purser.tier_entitlements, and purser.tier_pricing_rules inside a single
// transaction.
//
// Stable key: tier_name. Stripe IDs (stripe_product_id, stripe_price_id_*) are
// owned by the startup Stripe sync; this reconciler never overwrites them and
// never compares against them. is_active is left at its existing value when the
// row already exists; new rows default to true.
func ReconcileBillingTierCatalog(ctx context.Context, exec DBTX, tiers []CatalogTier) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcileBillingTierCatalog: nil executor")
	}
	if len(tiers) == 0 {
		return Result{}, errors.New("ReconcileBillingTierCatalog: empty tier list (refusing to no-op silently — pass EmbeddedTiers() or an explicit slice)")
	}
	if err := validateCatalogPricingRuleUniqueness(tiers); err != nil {
		return Result{}, err
	}

	res := Result{}
	for _, t := range tiers {
		action, err := upsertBillingTier(ctx, exec, t)
		if err != nil {
			return Result{}, fmt.Errorf("upsert tier %q: %w", t.TierName, err)
		}
		switch action {
		case "created":
			res.Created = append(res.Created, t.TierName)
		case "updated":
			res.Updated = append(res.Updated, t.TierName)
		case "noop":
			res.Noop = append(res.Noop, t.TierName)
		}
	}

	return res, nil
}

func validateCatalogPricingRuleUniqueness(tiers []CatalogTier) error {
	for _, tier := range tiers {
		seen := map[string]struct{}{}
		for _, rule := range tier.PricingRules {
			if rule.Meter == "" {
				continue
			}
			if _, ok := seen[rule.Meter]; ok {
				return fmt.Errorf("tier %q has duplicate pricing rule for meter %q", tier.TierName, rule.Meter)
			}
			seen[rule.Meter] = struct{}{}
		}
	}
	return nil
}

// upsertBillingTier reconciles one tier across three tables. Returns "created",
// "updated", or "noop". Any drift in the tier row, entitlements, or pricing rules
// counts as "updated".
func upsertBillingTier(ctx context.Context, exec DBTX, t CatalogTier) (string, error) {
	features, err := jsonBytes(t.Features)
	if err != nil {
		return "", fmt.Errorf("features: %w", err)
	}

	var tierID string
	var existed bool
	probeErr := exec.QueryRowContext(ctx,
		`SELECT id FROM purser.billing_tiers WHERE tier_name = $1`,
		t.TierName,
	).Scan(&tierID)
	switch {
	case probeErr == nil:
		existed = true
	case errors.Is(probeErr, sql.ErrNoRows):
		// fall through to INSERT
	default:
		return "", fmt.Errorf("existence probe: %w", probeErr)
	}

	tierAction := "noop"

	if !existed {
		const insertSQL = `
			INSERT INTO purser.billing_tiers (
				tier_name, display_name, description,
				base_price, currency, billing_period,
				features, support_level, sla_level,
				metering_enabled,
				tier_level, is_enterprise,
				is_default_prepaid, is_default_postpaid,
				processes_live, processes_vod
			) VALUES (
				$1, $2, $3,
				$4, $5, $6,
				$7, $8, $9,
				$10,
				$11, $12,
				$13, $14,
				$15, $16
			) RETURNING id`
		if insertErr := exec.QueryRowContext(ctx, insertSQL,
			t.TierName, t.DisplayName, t.Description,
			t.BasePrice, t.Currency, defaultPeriod(t.BillingPeriod),
			features, t.SupportLevel, t.SLALevel,
			t.MeteringEnabled,
			t.TierLevel, t.IsEnterprise,
			t.IsDefaultPrepaid, t.IsDefaultPostpaid,
			processOrEmpty(t.ProcessesLive), processOrEmpty(t.ProcessesVOD),
		).Scan(&tierID); insertErr != nil {
			return "", insertErr
		}
		tierAction = "created"
	} else {
		// Compare current row to desired; UPDATE when drift.
		const compareSQL = `
			SELECT
				display_name, description, base_price::text, currency, billing_period,
				features::text, support_level, sla_level,
				metering_enabled,
				tier_level, is_enterprise,
				is_default_prepaid, is_default_postpaid,
				processes_live::text, processes_vod::text
			FROM purser.billing_tiers
			WHERE tier_name = $1`
		var (
			curDisplay, curDesc, curBase, curCurr, curPeriod                  string
			curFeat, curSupport, curSLA                                       string
			curMetering, curEnterprise, curDefaultPrepaid, curDefaultPostpaid bool
			curTierLvl                                                        int
			curLive, curVOD                                                   string
		)
		if scanErr := exec.QueryRowContext(ctx, compareSQL, t.TierName).Scan(
			&curDisplay, &curDesc, &curBase, &curCurr, &curPeriod,
			&curFeat, &curSupport, &curSLA,
			&curMetering,
			&curTierLvl, &curEnterprise,
			&curDefaultPrepaid, &curDefaultPostpaid,
			&curLive, &curVOD,
		); scanErr != nil {
			return "", fmt.Errorf("compare row: %w", scanErr)
		}

		if !(curDisplay == t.DisplayName &&
			curDesc == t.Description &&
			moneyEq(curBase, t.BasePrice) &&
			curCurr == t.Currency &&
			curPeriod == defaultPeriod(t.BillingPeriod) &&
			jsonEq(curFeat, features) &&
			curSupport == t.SupportLevel &&
			curSLA == t.SLALevel &&
			curMetering == t.MeteringEnabled &&
			curTierLvl == t.TierLevel &&
			curEnterprise == t.IsEnterprise &&
			curDefaultPrepaid == t.IsDefaultPrepaid &&
			curDefaultPostpaid == t.IsDefaultPostpaid &&
			jsonEq(curLive, []byte(processOrEmpty(t.ProcessesLive))) &&
			jsonEq(curVOD, []byte(processOrEmpty(t.ProcessesVOD)))) {
			const updateSQL = `
				UPDATE purser.billing_tiers SET
					display_name = $2,
					description = $3,
					base_price = $4,
					currency = $5,
					billing_period = $6,
					features = $7,
					support_level = $8,
					sla_level = $9,
					metering_enabled = $10,
					tier_level = $11,
					is_enterprise = $12,
					is_default_prepaid = $13,
					is_default_postpaid = $14,
					processes_live = $15,
					processes_vod = $16
				WHERE tier_name = $1`
			if _, updateErr := exec.ExecContext(ctx, updateSQL,
				t.TierName, t.DisplayName, t.Description,
				t.BasePrice, t.Currency, defaultPeriod(t.BillingPeriod),
				features, t.SupportLevel, t.SLALevel,
				t.MeteringEnabled,
				t.TierLevel, t.IsEnterprise,
				t.IsDefaultPrepaid, t.IsDefaultPostpaid,
				processOrEmpty(t.ProcessesLive), processOrEmpty(t.ProcessesVOD),
			); updateErr != nil {
				return "", updateErr
			}
			tierAction = "updated"
		}
	}

	entAction, err := reconcileTierEntitlements(ctx, exec, tierID, t.Entitlements)
	if err != nil {
		return "", fmt.Errorf("entitlements: %w", err)
	}
	rulesAction, err := reconcileTierPricingRules(ctx, exec, tierID, t.Currency, t.PricingRules)
	if err != nil {
		return "", fmt.Errorf("pricing_rules: %w", err)
	}

	if tierAction == "created" {
		return "created", nil
	}
	if tierAction == "updated" || entAction == "updated" || rulesAction == "updated" {
		return "updated", nil
	}
	return "noop", nil
}

// reconcileTierEntitlements ensures (tier_id, key) rows match the desired map.
// Returns "updated" if any row was inserted, updated, or removed, else "noop".
func reconcileTierEntitlements(ctx context.Context, exec DBTX, tierID string, desired map[string]any) (string, error) {
	current := map[string]string{}
	if err := scanEntitlementRows(ctx, exec, tierID, current); err != nil {
		return "", err
	}

	desiredJSON := map[string][]byte{}
	for k, v := range desired {
		// Canonical shape: the bare YAML scalar JSON-encoded (90, "ok", true).
		// Migration backfill, YAML reconcile, and parseRetentionDays all
		// agree on this — no wrapping object, no special accessor.
		buf, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshal entitlement %q: %w", k, err)
		}
		desiredJSON[k] = buf
	}

	changed := false
	for k, buf := range desiredJSON {
		cur, ok := current[k]
		if ok && jsonEq(cur, buf) {
			continue
		}
		if _, err := exec.ExecContext(ctx,
			`INSERT INTO purser.tier_entitlements (tier_id, key, value)
			 VALUES ($1, $2, $3::jsonb)
			 ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value`,
			tierID, k, string(buf),
		); err != nil {
			return "", err
		}
		changed = true
	}
	for k := range current {
		if _, ok := desiredJSON[k]; ok {
			continue
		}
		if _, err := exec.ExecContext(ctx,
			`DELETE FROM purser.tier_entitlements WHERE tier_id = $1 AND key = $2`,
			tierID, k,
		); err != nil {
			return "", err
		}
		changed = true
	}
	if changed {
		return "updated", nil
	}
	return "noop", nil
}

// currentRow mirrors a row in purser.tier_pricing_rules for drift comparison.
type currentRow struct {
	model            string
	currency         string
	includedQuantity string
	unitPrice        string
	configJSON       string
}

// reconcileTierPricingRules ensures the (tier_id, meter) rows match the desired
// rules. The currency on each rule defaults to the tier's currency.
func reconcileTierPricingRules(ctx context.Context, exec DBTX, tierID, tierCurrency string, desired []CatalogPricingRule) (string, error) {
	current := map[string]currentRow{}
	if err := scanPricingRuleRows(ctx, exec, tierID, current); err != nil {
		return "", err
	}

	desiredByMeter := map[string]CatalogPricingRule{}
	for _, rule := range desired {
		if err := validateCatalogPricingRule(rule, tierCurrency); err != nil {
			return "", err
		}
		desiredByMeter[rule.Meter] = rule
	}

	changed := false

	// Sort keys for deterministic ordering (helps tests, doesn't affect SQL semantics).
	meters := make([]string, 0, len(desiredByMeter))
	for m := range desiredByMeter {
		meters = append(meters, m)
	}
	sort.Strings(meters)

	for _, meter := range meters {
		rule := desiredByMeter[meter]
		ruleCurrency := rule.Currency
		if ruleCurrency == "" {
			ruleCurrency = tierCurrency
		}
		configBytes, err := jsonBytes(rule.Config)
		if err != nil {
			return "", fmt.Errorf("rule %q config: %w", meter, err)
		}
		if cur, ok := current[meter]; ok {
			if cur.model == rule.Model &&
				cur.currency == ruleCurrency &&
				numericEq(cur.includedQuantity, rule.IncludedQuantity) &&
				priceEq(cur.unitPrice, rule.UnitPrice) &&
				jsonEq(cur.configJSON, configBytes) {
				continue
			}
		}
		if _, err := exec.ExecContext(ctx,
			`INSERT INTO purser.tier_pricing_rules
			   (tier_id, meter, model, currency, included_quantity, unit_price, config)
			 VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::jsonb)
			 ON CONFLICT (tier_id, meter) DO UPDATE SET
			   model = EXCLUDED.model,
			   currency = EXCLUDED.currency,
			   included_quantity = EXCLUDED.included_quantity,
			   unit_price = EXCLUDED.unit_price,
			   config = EXCLUDED.config`,
			tierID, meter, rule.Model, ruleCurrency,
			fmtQuantity(rule.IncludedQuantity), rule.UnitPrice, string(configBytes),
		); err != nil {
			return "", err
		}
		changed = true
	}

	for meter := range current {
		if _, ok := desiredByMeter[meter]; ok {
			continue
		}
		if _, err := exec.ExecContext(ctx,
			`DELETE FROM purser.tier_pricing_rules WHERE tier_id = $1 AND meter = $2`,
			tierID, meter,
		); err != nil {
			return "", err
		}
		changed = true
	}
	if changed {
		return "updated", nil
	}
	return "noop", nil
}

func validateCatalogPricingRule(rule CatalogPricingRule, tierCurrency string) error {
	if !rating.ValidMeter(rating.Meter(rule.Meter)) {
		return fmt.Errorf("pricing rule meter %q is not supported", rule.Meter)
	}
	if !rating.ValidModel(rating.Model(rule.Model)) {
		return fmt.Errorf("pricing rule %q model %q is not supported", rule.Meter, rule.Model)
	}
	ruleCurrency := rule.Currency
	if ruleCurrency == "" {
		ruleCurrency = tierCurrency
	}
	if ruleCurrency == "" {
		return fmt.Errorf("pricing rule %q has no currency", rule.Meter)
	}
	if _, err := decimal.NewFromString(rule.UnitPrice); err != nil {
		return fmt.Errorf("pricing rule %q unit_price %q: %w", rule.Meter, rule.UnitPrice, err)
	}
	return nil
}

// scanEntitlementRows reads entitlement values as canonical JSON text so catalog
// reconciliation can compare serialized values without reparsing each row.
func scanEntitlementRows(ctx context.Context, exec DBTX, tierID string, out map[string]string) error {
	rows, err := exec.QueryContext(ctx, `SELECT key, value::text FROM purser.tier_entitlements WHERE tier_id = $1`, tierID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		out[k] = v
	}
	return rows.Err()
}

// scanPricingRuleRows reads tier_pricing_rules into out keyed by meter.
func scanPricingRuleRows(ctx context.Context, exec DBTX, tierID string, out map[string]currentRow) error {
	rows, err := exec.QueryContext(ctx,
		`SELECT meter, model, currency, included_quantity::text, unit_price::text, config::text
		   FROM purser.tier_pricing_rules WHERE tier_id = $1`, tierID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var meter string
		var r currentRow
		if err := rows.Scan(&meter, &r.model, &r.currency, &r.includedQuantity, &r.unitPrice, &r.configJSON); err != nil {
			return err
		}
		out[meter] = r
	}
	return rows.Err()
}

func defaultPeriod(p string) string {
	if p == "" {
		return "monthly"
	}
	return p
}

func processOrEmpty(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

func fmtQuantity(v float64) string { return fmt.Sprintf("%.6f", v) }

// moneyText formats a DECIMAL(10,2) value the way Postgres emits it (e.g. "0.00",
// "999.00"). Used both in the comparison path and in tests.
func moneyText(v float64) string { return fmt.Sprintf("%.2f", v) }

// moneyEq compares a NUMERIC column's text representation against a float.
func moneyEq(current string, desired float64) bool {
	return current == moneyText(desired)
}

// numericEq compares a NUMERIC(20,6) column's text against a float64. Postgres
// emits trailing zeros up to scale, so we format desired the same way.
func numericEq(current string, desired float64) bool {
	return current == fmt.Sprintf("%.6f", desired)
}

// priceEq compares a NUMERIC(20,9) column's text against a string-encoded price.
// Postgres emits 9 decimal places; the catalog stores prices as strings to avoid
// float artifacts.
func priceEq(current, desired string) bool {
	if desired == "" {
		desired = "0"
	}
	cur, err := decimal.NewFromString(current)
	if err != nil {
		return current == desired
	}
	des, err := decimal.NewFromString(desired)
	if err != nil {
		return current == desired
	}
	return cur.Equal(des)
}

// jsonEq compares JSONB column text against the canonical-marshaled bytes the
// reconciler would write. Postgres normalizes JSONB whitespace and key order on
// store, so we compare logical equality by re-parsing both sides.
func jsonEq(current string, desired []byte) bool {
	var a, b any
	if err := json.Unmarshal([]byte(current), &a); err != nil {
		return false
	}
	if err := json.Unmarshal(desired, &b); err != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}
