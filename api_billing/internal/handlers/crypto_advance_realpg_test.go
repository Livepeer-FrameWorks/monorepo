//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/fx"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestCryptoAdvanceCreditDocumentAndReorgAreAtomic_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenant := seedPrepaidLedgerTenant(t, db)
	walletID := uuid.NewString()
	now := time.Now().UTC()
	money, cash, err := quoteAdvanceMoney(1000, 2100, fx.Rate{Currency: "EUR", UnitsPerEUR: decimal.NewFromInt(1), Source: "identity", ReferenceDate: fx.Date(now)})
	if err != nil {
		t.Fatal(err)
	}
	profile := CryptoBillingProfile{CustomerType: "consumer", Name: "Original Buyer", Email: "buyer@example.test", Address: json.RawMessage(`{"street":"Main1","city":"Leiden","postal_code":"1234","country":"NL"}`), TaxAssessment: &CryptoTaxAssessment{Advance: &money, PolicyVersion: billing.CurrentCryptoTopupTaxPolicy, AssessedAt: now, SupplierCountry: "NL", Supplier: DocumentSupplier{Name: "DDVTech B.V.", Address: "Leiden, NL", VATNumber: "NL123456789B01", RegistrationNumber: "12345678"}, Decision: vatDecision{Country: "NL", RateBPS: 2100, Treatment: "domestic_vat", Source: "Dutch VAT", CheckedOn: now.Format(time.DateOnly), EffectiveFrom: "2026-01-01"}}}
	raw, _ := json.Marshal(profile)
	if _, err = db.ExecContext(ctx, `INSERT INTO purser.crypto_wallets(id,tenant_id,purpose,expected_amount_cents,asset,network,wallet_address,derivation_index,derivation_xpub,status,expires_at,expected_amount_base_units,quoted_price_usd,quoted_at,quote_source,credited_amount_currency,tax_document_kind,tax_profile_snapshot,original_amount_cents,original_currency,eur_amount_cents,fx_units_per_eur,fx_source,fx_reference_date) VALUES($1,$2,'prepaid',1210,'USDC','base','0x00000000000000000000000000000000000000dd',9,'xpub-test','pending',NOW()+INTERVAL '1 hour',15125000,1,NOW(),'one_to_one','EUR','full',$3,1210,'EUR',1210,1,'identity',CURRENT_DATE)`, walletID, tenant, raw); err != nil {
		t.Fatal(err)
	}
	expected := int64(1210)
	wallet := PendingWallet{ID: walletID, TenantID: tenant, Purpose: "prepaid", ExpectedAmountCents: &expected, Asset: "USDC", Network: "base", ExpectedAmountBaseUnits: big.NewInt(15125000), FX: &cash, TaxProfile: profile, TaxDocumentKind: "full"}
	receipt := CryptoTransaction{Hash: "0x" + strings.Repeat("d", 64)}
	monitor := &CryptoMonitor{db: db}
	if _, err = db.ExecContext(ctx, `CREATE FUNCTION purser.test_reject_crypto_advance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rejects financial document after balance update'; END $$; CREATE TRIGGER test_reject_crypto_advance BEFORE INSERT ON purser.billing_invoices FOR EACH ROW EXECUTE FUNCTION purser.test_reject_crypto_advance()`); err != nil {
		t.Fatal(err)
	}
	err = database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		_, _, err := monitor.confirmPrepaidTopup(ctx, tx, wallet, receipt, wallet.ExpectedAmountBaseUnits, now)
		return err
	})
	if err == nil {
		t.Fatal("bad document was accepted after balance credit")
	}
	if _, err = db.ExecContext(ctx, `DROP TRIGGER test_reject_crypto_advance ON purser.billing_invoices; DROP FUNCTION purser.test_reject_crypto_advance()`); err != nil {
		t.Fatal(err)
	}
	var balance int64
	if err = db.QueryRowContext(ctx, `SELECT COALESCE((SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id=$1 AND currency='EUR'),0)`, tenant).Scan(&balance); err != nil || balance != 0 {
		t.Fatalf("failed tax document exposed spendable credit: %d %v", balance, err)
	}
	err = database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		credited, currency, err := monitor.confirmPrepaidTopup(ctx, tx, wallet, receipt, wallet.ExpectedAmountBaseUnits, now)
		if err == nil && (credited != 1000 || currency != "EUR") {
			t.Fatalf("gross became spendable credit: %d %s", credited, currency)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var net, vat, gross, remaining, remainingVAT int64
	if err = db.QueryRowContext(ctx, `SELECT invoice.net_eur_cents,invoice.vat_eur_cents,invoice.gross_eur_cents,lot.remaining_net_eur_cents,lot.remaining_vat_eur_cents FROM purser.billing_invoices invoice JOIN purser.prepaid_funding_lots lot ON lot.tenant_id=invoice.tenant_id AND lot.source_document_id=invoice.id WHERE invoice.tenant_id=$1 AND invoice.source_reference_type='crypto_payment' AND invoice.source_reference_id=$2`, tenant, receipt.Hash).Scan(&net, &vat, &gross, &remaining, &remainingVAT); err != nil || net != 1000 || vat != 210 || gross != 1210 || remaining != 1000 || remainingVAT != 210 {
		t.Fatalf("advance facts net=%d VAT=%d gross=%d remaining=%d/%d: %v", net, vat, gross, remaining, remainingVAT, err)
	}
	event := uuid.NewString()
	reversal := allocatedDepositReversal{tenantID: tenant, walletID: walletID, txHash: sql.NullString{String: receipt.Hash, Valid: true}, creditedCents: sql.NullInt64{Int64: 1000, Valid: true}, creditCurrency: sql.NullString{String: "EUR", Valid: true}}
	err = database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		return reverseCryptoPrepaidDepositTx(ctx, tx, event, "0xold", "0xnew", reversal)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT balance.balance_cents,lot.remaining_net_eur_cents,lot.remaining_vat_eur_cents FROM purser.prepaid_balances balance JOIN purser.prepaid_funding_lots lot ON lot.tenant_id=balance.tenant_id WHERE balance.tenant_id=$1 AND balance.currency='EUR' AND lot.source_reference_type='crypto_payment'`, tenant).Scan(&balance, &remaining, &remainingVAT); err != nil || balance != 0 || remaining != 0 || remainingVAT != 0 {
		t.Fatalf("reorg left credit or funding usable: %d %d %d %v", balance, remaining, remainingVAT, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT net_eur_cents,vat_eur_cents,gross_eur_cents FROM purser.credit_notes WHERE tenant_id=$1 AND reversal_reference_id=$2`, tenant, event).Scan(&net, &vat, &gross); err != nil || net != 1000 || vat != 210 || gross != 1210 {
		t.Fatalf("reorg source tax credit net=%d VAT=%d gross=%d: %v", net, vat, gross, err)
	}
}

func TestFundingReturnCapsSharedLotAgainstRevokedCredit_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.prepaid_balances(tenant_id,currency,balance_cents) VALUES($1,'EUR',1000);`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.prepaid_funding_lots(tenant_id,source_reference_type,source_reference_id,classification,net_credit_eur_cents,remaining_net_eur_cents) VALUES($1,'test','shared','legacy_unknown',1000,1000)`, tenant); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.ExecContext(ctx, `UPDATE purser.prepaid_balances SET balance_cents=$2 WHERE tenant_id=$1 AND currency='EUR'`, tenant, 500-i*500); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.balance_transactions(tenant_id,amount_cents,balance_after_cents,transaction_type,reference_type,reference_id,description) VALUES($1,-500,$2,'deduction','invoice_credit',$3,'Invoice credit: TEST-INV')`, tenant, 500-i*500, uuid.NewString()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE purser.prepaid_funding_lots SET revoked_net_eur_cents=750 WHERE tenant_id=$1 AND source_reference_type='test'`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE purser.prepaid_balances SET balance_cents=250 WHERE tenant_id=$1 AND currency='EUR'`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.balance_transactions(tenant_id,amount_cents,balance_after_cents,transaction_type,reference_type,reference_id,description) VALUES($1,1000,250,'credit','invoice_credit',$2,'Invoice credit returned: TEST-INV')`, tenant, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := db.QueryRowContext(ctx, `SELECT remaining_net_eur_cents FROM purser.prepaid_funding_lots WHERE tenant_id=$1 AND source_reference_type='test'`, tenant).Scan(&remaining); err != nil || remaining != 250 {
		t.Fatalf("multiple allocation rows restored revoked credit: remaining=%d want250: %v", remaining, err)
	}
}
