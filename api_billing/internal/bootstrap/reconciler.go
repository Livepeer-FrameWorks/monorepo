package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// ReconcileBillingTierCatalog upserts every CatalogTier into purser.billing_tiers
// inside a single transaction. It is the binary's source of truth for the canonical
// catalog; production calls it through the embedded YAML, tests inject arbitrary
// slices.
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

// upsertBillingTier reconciles one tier row. Returns "created", "updated", or
// "noop" so the caller can build an aggregate Result.
//
// The explicit existence + drift check (vs INSERT … ON CONFLICT DO UPDATE)
// exists because the noop case is what idempotency tests assert and what
// operators want to see. One extra SELECT per tier per run on a six-row
// catalog is negligible.
func upsertBillingTier(ctx context.Context, exec DBTX, t CatalogTier) (string, error) {
	bandwidth, err := jsonBytes(t.BandwidthAllocation)
	if err != nil {
		return "", fmt.Errorf("bandwidth_allocation: %w", err)
	}
	storage, err := jsonBytes(t.StorageAllocation)
	if err != nil {
		return "", fmt.Errorf("storage_allocation: %w", err)
	}
	compute, err := jsonBytes(t.ComputeAllocation)
	if err != nil {
		return "", fmt.Errorf("compute_allocation: %w", err)
	}
	features, err := jsonBytes(t.Features)
	if err != nil {
		return "", fmt.Errorf("features: %w", err)
	}
	overage, err := jsonBytes(t.OverageRates)
	if err != nil {
		return "", fmt.Errorf("overage_rates: %w", err)
	}

	var exists bool
	if err := exec.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM purser.billing_tiers WHERE tier_name = $1)`,
		t.TierName,
	).Scan(&exists); err != nil {
		return "", fmt.Errorf("existence probe: %w", err)
	}

	if !exists {
		const insertSQL = `
			INSERT INTO purser.billing_tiers (
				tier_name, display_name, description,
				base_price, currency, billing_period,
				bandwidth_allocation, storage_allocation, compute_allocation,
				features, support_level, sla_level,
				metering_enabled, overage_rates,
				tier_level, is_enterprise,
				is_default_prepaid, is_default_postpaid,
				processes_live, processes_vod
			) VALUES (
				$1, $2, $3,
				$4, $5, $6,
				$7, $8, $9,
				$10, $11, $12,
				$13, $14,
				$15, $16,
				$17, $18,
				$19, $20
			)`
		if _, err := exec.ExecContext(ctx, insertSQL,
			t.TierName, t.DisplayName, t.Description,
			t.BasePrice, t.Currency, defaultPeriod(t.BillingPeriod),
			bandwidth, storage, compute,
			features, t.SupportLevel, t.SLALevel,
			t.MeteringEnabled, overage,
			t.TierLevel, t.IsEnterprise,
			t.IsDefaultPrepaid, t.IsDefaultPostpaid,
			processOrEmpty(t.ProcessesLive), processOrEmpty(t.ProcessesVOD),
		); err != nil {
			return "", err
		}
		return "created", nil
	}

	// Compare current row to desired; if every mutable column already matches,
	// skip the UPDATE so the result is a clean noop. Stripe columns are
	// deliberately not in either side of the comparison — the Stripe sync owns them.
	const compareSQL = `
		SELECT
			display_name, description, base_price::text, currency, billing_period,
			bandwidth_allocation::text, storage_allocation::text, compute_allocation::text,
			features::text, support_level, sla_level,
			metering_enabled, overage_rates::text,
			tier_level, is_enterprise,
			is_default_prepaid, is_default_postpaid,
			processes_live::text, processes_vod::text
		FROM purser.billing_tiers
		WHERE tier_name = $1`
	var (
		curDisplay, curDesc, curBase, curCurr, curPeriod                  string
		curBW, curStor, curCmp, curFeat, curOverage                       string
		curSupport, curSLA                                                string
		curMetering, curEnterprise, curDefaultPrepaid, curDefaultPostpaid bool
		curTierLvl                                                        int
		curLive, curVOD                                                   string
	)
	if err := exec.QueryRowContext(ctx, compareSQL, t.TierName).Scan(
		&curDisplay, &curDesc, &curBase, &curCurr, &curPeriod,
		&curBW, &curStor, &curCmp, &curFeat, &curSupport, &curSLA,
		&curMetering, &curOverage,
		&curTierLvl, &curEnterprise,
		&curDefaultPrepaid, &curDefaultPostpaid,
		&curLive, &curVOD,
	); err != nil {
		return "", fmt.Errorf("compare row: %w", err)
	}

	if curDisplay == t.DisplayName &&
		curDesc == t.Description &&
		moneyEq(curBase, t.BasePrice) &&
		curCurr == t.Currency &&
		curPeriod == defaultPeriod(t.BillingPeriod) &&
		jsonEq(curBW, bandwidth) &&
		jsonEq(curStor, storage) &&
		jsonEq(curCmp, compute) &&
		jsonEq(curFeat, features) &&
		curSupport == t.SupportLevel &&
		curSLA == t.SLALevel &&
		curMetering == t.MeteringEnabled &&
		jsonEq(curOverage, overage) &&
		curTierLvl == t.TierLevel &&
		curEnterprise == t.IsEnterprise &&
		curDefaultPrepaid == t.IsDefaultPrepaid &&
		curDefaultPostpaid == t.IsDefaultPostpaid &&
		jsonEq(curLive, []byte(processOrEmpty(t.ProcessesLive))) &&
		jsonEq(curVOD, []byte(processOrEmpty(t.ProcessesVOD))) {
		return "noop", nil
	}

	const updateSQL = `
		UPDATE purser.billing_tiers SET
			display_name = $2,
			description = $3,
			base_price = $4,
			currency = $5,
			billing_period = $6,
			bandwidth_allocation = $7,
			storage_allocation = $8,
			compute_allocation = $9,
			features = $10,
			support_level = $11,
			sla_level = $12,
			metering_enabled = $13,
			overage_rates = $14,
			tier_level = $15,
			is_enterprise = $16,
			is_default_prepaid = $17,
			is_default_postpaid = $18,
			processes_live = $19,
			processes_vod = $20
		WHERE tier_name = $1`
	if _, err := exec.ExecContext(ctx, updateSQL,
		t.TierName, t.DisplayName, t.Description,
		t.BasePrice, t.Currency, defaultPeriod(t.BillingPeriod),
		bandwidth, storage, compute,
		features, t.SupportLevel, t.SLALevel,
		t.MeteringEnabled, overage,
		t.TierLevel, t.IsEnterprise,
		t.IsDefaultPrepaid, t.IsDefaultPostpaid,
		processOrEmpty(t.ProcessesLive), processOrEmpty(t.ProcessesVOD),
	); err != nil {
		return "", err
	}
	return "updated", nil
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

// moneyText formats a DECIMAL(10,2) value the way Postgres emits it (e.g. "0.00",
// "999.00"). Used both in the comparison path and in tests.
func moneyText(v float64) string { return fmt.Sprintf("%.2f", v) }

// moneyEq compares a NUMERIC column's text representation against a float.
func moneyEq(current string, desired float64) bool {
	return current == moneyText(desired)
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
