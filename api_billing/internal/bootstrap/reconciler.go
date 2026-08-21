package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/database/purserdb"
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

	queries := purserdb.New(exec)
	var tierID uuid.UUID
	var existed bool
	tierID, probeErr := queries.GetBootstrapTierID(ctx, t.TierName)
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
		tierID, err = queries.InsertBootstrapBillingTier(ctx, purserdb.InsertBootstrapBillingTierParams{
			TierName: t.TierName, DisplayName: t.DisplayName, Description: t.Description,
			BasePrice: moneyText(t.BasePrice), Currency: t.Currency, BillingPeriod: defaultPeriod(t.BillingPeriod),
			Features: features, SupportLevel: t.SupportLevel, SlaLevel: t.SLALevel,
			MeteringEnabled: t.MeteringEnabled, TierLevel: int32(t.TierLevel), IsEnterprise: t.IsEnterprise,
			IsDefaultPrepaid: t.IsDefaultPrepaid, IsDefaultPostpaid: t.IsDefaultPostpaid,
			ProcessesLive: processOrEmpty(t.ProcessesLive), ProcessesDvr: processOrEmpty(t.ProcessesDVR),
			ProcessesClip: processOrEmpty(t.ProcessesClip), ProcessesDvrFinalize: processOrEmpty(t.ProcessesDVRFinalize),
			ProcessesVod: processOrEmpty(t.ProcessesVOD),
		})
		if err != nil {
			return "", err
		}
		tierAction = "created"
	} else {
		current, getErr := queries.GetBootstrapBillingTierState(ctx, t.TierName)
		if getErr != nil {
			return "", fmt.Errorf("compare row: %w", getErr)
		}

		if !billingTierMatches(current, t, features) {
			rows, updateErr := queries.UpdateBootstrapBillingTier(ctx, purserdb.UpdateBootstrapBillingTierParams{
				DisplayName: t.DisplayName, Description: t.Description, BasePrice: moneyText(t.BasePrice),
				Currency: t.Currency, BillingPeriod: defaultPeriod(t.BillingPeriod), Features: features,
				SupportLevel: t.SupportLevel, SlaLevel: t.SLALevel, MeteringEnabled: t.MeteringEnabled,
				TierLevel: int32(t.TierLevel), IsEnterprise: t.IsEnterprise,
				IsDefaultPrepaid: t.IsDefaultPrepaid, IsDefaultPostpaid: t.IsDefaultPostpaid,
				ProcessesLive: processOrEmpty(t.ProcessesLive), ProcessesDvr: processOrEmpty(t.ProcessesDVR),
				ProcessesClip: processOrEmpty(t.ProcessesClip), ProcessesDvrFinalize: processOrEmpty(t.ProcessesDVRFinalize),
				ProcessesVod: processOrEmpty(t.ProcessesVOD), TierName: t.TierName,
			})
			if updateErr != nil {
				return "", updateErr
			}
			if rows != 1 {
				return "", fmt.Errorf("update tier affected %d rows", rows)
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

func billingTierMatches(current purserdb.GetBootstrapBillingTierStateRow, desired CatalogTier, features []byte) bool {
	return current.DisplayName == desired.DisplayName &&
		current.Description == desired.Description &&
		moneyEq(current.BasePrice, desired.BasePrice) &&
		current.Currency == desired.Currency &&
		current.BillingPeriod == defaultPeriod(desired.BillingPeriod) &&
		jsonEq(string(current.Features), features) &&
		current.SupportLevel == desired.SupportLevel &&
		current.SlaLevel == desired.SLALevel &&
		current.MeteringEnabled == desired.MeteringEnabled &&
		current.TierLevel == int32(desired.TierLevel) &&
		current.IsEnterprise == desired.IsEnterprise &&
		current.IsDefaultPrepaid == desired.IsDefaultPrepaid &&
		current.IsDefaultPostpaid == desired.IsDefaultPostpaid &&
		jsonEq(string(current.ProcessesLive), []byte(processOrEmpty(desired.ProcessesLive))) &&
		jsonEq(string(current.ProcessesDvr), []byte(processOrEmpty(desired.ProcessesDVR))) &&
		jsonEq(string(current.ProcessesClip), []byte(processOrEmpty(desired.ProcessesClip))) &&
		jsonEq(string(current.ProcessesDvrFinalize), []byte(processOrEmpty(desired.ProcessesDVRFinalize))) &&
		jsonEq(string(current.ProcessesVod), []byte(processOrEmpty(desired.ProcessesVOD)))
}

// reconcileTierEntitlements ensures (tier_id, key) rows match the desired map.
// Returns "updated" if any row was inserted, updated, or removed, else "noop".
func reconcileTierEntitlements(ctx context.Context, exec DBTX, tierID uuid.UUID, desired map[string]any) (string, error) {
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
		if err := purserdb.New(exec).UpsertBootstrapTierEntitlement(ctx, purserdb.UpsertBootstrapTierEntitlementParams{
			TierID: tierID, Key: k, Value: buf,
		}); err != nil {
			return "", err
		}
		changed = true
	}
	for k := range current {
		if _, ok := desiredJSON[k]; ok {
			continue
		}
		if err := purserdb.New(exec).DeleteBootstrapTierEntitlement(ctx, purserdb.DeleteBootstrapTierEntitlementParams{
			TierID: tierID, Key: k,
		}); err != nil {
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
func reconcileTierPricingRules(ctx context.Context, exec DBTX, tierID uuid.UUID, tierCurrency string, desired []CatalogPricingRule) (string, error) {
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
		if err := purserdb.New(exec).UpsertBootstrapTierPricingRule(ctx, purserdb.UpsertBootstrapTierPricingRuleParams{
			TierID: tierID, Meter: meter, Model: rule.Model, Currency: ruleCurrency,
			IncludedQuantity: fmtQuantity(rule.IncludedQuantity), UnitPrice: rule.UnitPrice, Config: configBytes,
		}); err != nil {
			return "", err
		}
		changed = true
	}

	for meter := range current {
		if _, ok := desiredByMeter[meter]; ok {
			continue
		}
		if err := purserdb.New(exec).DeleteBootstrapTierPricingRule(ctx, purserdb.DeleteBootstrapTierPricingRuleParams{
			TierID: tierID, Meter: meter,
		}); err != nil {
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
		return fmt.Errorf("pricing rule meter %q is invalid", rule.Meter)
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
	unitPrice, err := decimal.NewFromString(rule.UnitPrice)
	if err != nil {
		return fmt.Errorf("pricing rule %q unit_price %q: %w", rule.Meter, rule.UnitPrice, err)
	}
	if err := rating.ValidateRule(rating.Rule{
		Meter:            rating.Meter(rule.Meter),
		Model:            rating.Model(rule.Model),
		Currency:         ruleCurrency,
		IncludedQuantity: decimal.NewFromFloat(rule.IncludedQuantity),
		UnitPrice:        unitPrice,
		Config:           rule.Config,
	}); err != nil {
		return fmt.Errorf("pricing rule %q: %w", rule.Meter, err)
	}
	return nil
}

// scanEntitlementRows reads entitlement values as canonical JSON text so catalog
// reconciliation can compare serialized values without reparsing each row.
func scanEntitlementRows(ctx context.Context, exec DBTX, tierID uuid.UUID, out map[string]string) error {
	rows, err := purserdb.New(exec).ListBootstrapTierEntitlements(ctx, tierID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		out[row.Key] = string(row.Value)
	}
	return nil
}

// scanPricingRuleRows reads tier_pricing_rules into out keyed by meter.
func scanPricingRuleRows(ctx context.Context, exec DBTX, tierID uuid.UUID, out map[string]currentRow) error {
	rows, err := purserdb.New(exec).ListBootstrapTierPricingRules(ctx, tierID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		out[row.Meter] = currentRow{
			model: row.Model, currency: row.Currency, includedQuantity: row.IncludedQuantity,
			unitPrice: row.UnitPrice, configJSON: string(row.Config),
		}
	}
	return nil
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
