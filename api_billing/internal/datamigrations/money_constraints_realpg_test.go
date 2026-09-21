//go:build schema_verify

package datamigrations

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
)

var sprintf = fmt.Sprintf

func TestMoneyFXAndSettlementConstraints_RealPG(t *testing.T) {
	db := startPreContractPurserRealPG(t)
	tenantID := uuid.NewString()

	insertTopup := func(fx string) error {
		_, err := db.Exec(`INSERT INTO purser.pending_topups (tenant_id, provider, amount_cents, currency, status, expires_at,
    original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur, fx_source, fx_reference_date)
SELECT $1, 'stripe', 1000, 'USD', 'pending', NOW(), v.original, v.currency, v.eur, v.units, v.source, v.ref
FROM (VALUES `+fx+`) AS v(original, currency, eur, units, source, ref)`, tenantID)
		return err
	}
	accepted := map[string]string{
		"no FX yet":         `(NULL::bigint, NULL::char(3), NULL::bigint, NULL::numeric, NULL::varchar, NULL::date)`,
		"identity EUR":      `(1000::bigint, 'EUR'::char(3), 1000::bigint, 1::numeric, 'identity'::varchar, DATE '2026-04-08')`,
		"ECB USD":           `(1000::bigint, 'USD'::char(3), 916::bigint, 1.0921::numeric, 'ecb'::varchar, DATE '2026-04-08')`,
		"ECB GBP":           `(1000::bigint, 'GBP'::char(3), 1170::bigint, 0.85445::numeric, 'ecb'::varchar, DATE '2026-04-08')`,
		"legacy quoted USD": `(1000::bigint, 'USD'::char(3), 916::bigint, 1.0921::numeric, 'legacy_quote'::varchar, DATE '2026-04-08')`,
	}
	for name, fx := range accepted {
		if err := insertTopup(fx); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	rejected := map[string]string{
		"partial FX":              `(1000::bigint, 'USD'::char(3), NULL::bigint, NULL::numeric, NULL::varchar, NULL::date)`,
		"identity amount differs": `(1000::bigint, 'EUR'::char(3), 999::bigint, 1::numeric, 'identity'::varchar, DATE '2026-04-08')`,
		"identity rate not one":   `(1000::bigint, 'EUR'::char(3), 1000::bigint, 1.1::numeric, 'identity'::varchar, DATE '2026-04-08')`,
		"ECB rate for EUR":        `(1000::bigint, 'EUR'::char(3), 1000::bigint, 1::numeric, 'ecb'::varchar, DATE '2026-04-08')`,
		"lowercase currency":      `(1000::bigint, 'usd'::char(3), 916::bigint, 1.0921::numeric, 'ecb'::varchar, DATE '2026-04-08')`,
		"zero rate":               `(1000::bigint, 'USD'::char(3), 916::bigint, 0::numeric, 'ecb'::varchar, DATE '2026-04-08')`,
		"unknown source":          `(1000::bigint, 'USD'::char(3), 916::bigint, 1.0921::numeric, 'chainlink'::varchar, DATE '2026-04-08')`,
		"legacy quote for GBP":    `(1000::bigint, 'GBP'::char(3), 1170::bigint, 0.85445::numeric, 'legacy_quote'::varchar, DATE '2026-04-08')`,
		"no reference date":       `(1000::bigint, 'USD'::char(3), 916::bigint, 1.0921::numeric, 'ecb'::varchar, NULL::date)`,
		"no rate":                 `(1000::bigint, 'USD'::char(3), 916::bigint, NULL::numeric, 'ecb'::varchar, DATE '2026-04-08')`,
		"no source":               `(1000::bigint, 'USD'::char(3), 916::bigint, 1.0921::numeric, NULL::varchar, DATE '2026-04-08')`,
		"no original currency":    `(1000::bigint, NULL::char(3), 916::bigint, 1.0921::numeric, 'ecb'::varchar, DATE '2026-04-08')`,
	}
	for name, fx := range rejected {
		if err := insertTopup(fx); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	invoice := func(presentment string) error {
		_, err := db.Exec(`INSERT INTO purser.billing_invoices (tenant_id, status, currency, amount, due_date,
    presentment_amount_cents, presentment_currency, presentment_units_per_eur, presentment_reference_date)
SELECT $1, 'pending', 'EUR', 10, NOW(), v.amount, v.currency, v.units, v.ref
FROM (VALUES `+presentment+`) AS v(amount, currency, units, ref)`, tenantID)
		return err
	}
	if err := invoice(`(1092::bigint, 'USD'::char(3), 1.0921::numeric, DATE '2026-04-08')`); err != nil {
		t.Errorf("USD presentment refused: %v", err)
	}
	if err := invoice(`(1000::bigint, 'EUR'::char(3), 1.2::numeric, DATE '2026-04-08')`); err == nil {
		t.Error("EUR presentment at a rate other than 1 accepted")
	}
	if err := invoice(`(1000::bigint, 'JPY'::char(3), 160::numeric, DATE '2026-04-08')`); err == nil {
		t.Error("JPY presentment accepted")
	}
	if err := invoice(`(1092::bigint, 'USD'::char(3), NULL::numeric, DATE '2026-04-08')`); err == nil {
		t.Error("USD presentment without a rate accepted")
	}

	settlement := func(values string) error {
		_, err := db.Exec(`INSERT INTO purser.provider_settlements (tenant_id, provider, provider_payment_id, provider_balance_transaction_id,
    charge_amount_cents, charge_currency, settled_amount_cents, fee_cents, net_cents, settlement_currency, exchange_rate, status, settled_at)
SELECT $1, 'stripe', $2, v.btx, v.charge, v.charge_currency, v.settled, v.fee, v.net, v.settlement_currency, v.rate, v.status, v.settled_at
FROM (VALUES `+values+`) AS v(btx, charge, charge_currency, settled, fee, net, settlement_currency, rate, status, settled_at)`,
			tenantID, "pi_"+uuid.NewString())
		return err
	}
	settled := `('txn_%s'::varchar, 1092::bigint, 'USD'::char(3), 1000::bigint, 59::bigint, %s::bigint, 'EUR'::char(3), %s::numeric, 'settled'::varchar, NOW())`
	if err := settlement(sprintf(settled, uuid.NewString(), "941", "0.9157")); err != nil {
		t.Errorf("consistent USD settlement refused: %v", err)
	}
	if err := settlement(sprintf(settled, uuid.NewString(), "940", "0.9157")); err == nil {
		t.Error("settlement with net different from settled minus fee accepted")
	}
	if err := settlement(sprintf(settled, uuid.NewString(), "941", "NULL")); err == nil {
		t.Error("USD settlement without an exchange rate accepted")
	}
	if err := settlement(`('txn_nofee'::varchar, 1092::bigint, 'USD'::char(3), 1000::bigint, NULL::bigint, 941::bigint, 'EUR'::char(3), 0.9157::numeric, 'settled'::varchar, NOW())`); err == nil {
		t.Error("settled settlement without a fee accepted")
	}
	if err := settlement(`(NULL::varchar, 1092::bigint, 'USD'::char(3), NULL::bigint, NULL::bigint, NULL::bigint, NULL::char(3), NULL::numeric, 'pending'::varchar, NULL::timestamptz)`); err != nil {
		t.Errorf("pending settlement refused: %v", err)
	}
	if err := settlement(`('txn_pending'::varchar, 1092::bigint, 'USD'::char(3), 1000::bigint, NULL::bigint, NULL::bigint, NULL::char(3), NULL::numeric, 'pending'::varchar, NULL::timestamptz)`); err == nil {
		t.Error("pending settlement carrying settlement figures accepted")
	}
	if err := settlement(`('txn_eur'::varchar, 1000::bigint, 'EUR'::char(3), 1000::bigint, 25::bigint, 975::bigint, 'EUR'::char(3), NULL::numeric, 'settled'::varchar, NOW())`); err != nil {
		t.Errorf("EUR settlement without conversion refused: %v", err)
	}
}
