//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func seedPrepaidLedgerTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	tierID := uuid.NewString()
	tenantID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, currency)
		VALUES ($1, $2, 'Ledger currency tier', 'EUR')
	`, tierID, "ledger-"+tierID); err != nil {
		t.Fatalf("insert tier: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model)
		VALUES ($1, $2, 'active', 'prepaid')
	`, tenantID, tierID); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
	return tenantID
}

func admissionBalanceCents(t *testing.T, db *sql.DB, tenantID string) int64 {
	t.Helper()
	row, err := purserdb.New(db).GetTenantAdmissionStatus(context.Background(), purserdb.GetTenantAdmissionStatusParams{
		TenantID: tenantID, Currency: billing.LedgerCurrency,
	})
	if err != nil {
		t.Fatalf("admission status: %v", err)
	}
	if !row.BalanceCents.Valid {
		return 0
	}
	return row.BalanceCents.Int64
}

// storeRate stores one ECB reference rate for tests.
func storeRate(t *testing.T, db *sql.DB, currency string, referenceDate time.Time, unitsPerEUR string) {
	t.Helper()
	if err := fx.Upsert(context.Background(), db, []fx.Rate{{
		Currency: currency, ReferenceDate: referenceDate, UnitsPerEUR: decimal.RequireFromString(unitsPerEUR), Source: fx.SourceECB,
	}}, time.Now()); err != nil {
		t.Fatalf("store %s rate: %v", currency, err)
	}
}

func TestLatePrepaidCryptoReceiptGoesToReviewWithoutCredit_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	walletID := uuid.NewString()
	address := "0x00000000000000000000000000000000000000a1"
	quotedAt := time.Now().Add(-2 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.crypto_wallets (
			id, tenant_id, purpose, expected_amount_cents, asset, network, wallet_address,
			derivation_index, derivation_xpub, status, expires_at, expected_amount_base_units,
			quoted_price_usd, quoted_at, quote_source, credited_amount_currency,
			original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur, fx_source, fx_reference_date
		) VALUES ($1, $2, 'prepaid', 1000, 'USDC', 'base', $3, 7, 'xpub-late-receipt', 'pending',
		          NOW() - INTERVAL '1 hour', 10000000, 1, $4::timestamptz, 'one_to_one', 'EUR',
		          1000, 'USD', 950, 1.0526315789, 'ecb', $4::timestamptz::date)
	`, walletID, tenantID, address, quotedAt); err != nil {
		t.Fatalf("insert wallet: %v", err)
	}
	quote := fx.Record{
		OriginalMinor: 1000, OriginalCurrency: fx.USD, EURMinor: 950,
		UnitsPerEUR: decimal.RequireFromString("1.0526315789"), Source: fx.SourceECB, ReferenceDate: fx.Date(quotedAt),
	}
	expectedCents := int64(1000)
	wallet := PendingWallet{
		ID: walletID, TenantID: tenantID, Purpose: "prepaid", Asset: "USDC", Network: "base",
		WalletAddress: address, ExpiresAt: time.Now().Add(-time.Hour), ExpectedAmountCents: &expectedCents,
		ExpectedAmountBaseUnits: big.NewInt(10_000_000), QuotedPriceUSD: decimal.NewFromInt(1),
		FX: &quote, QuoteSource: "one_to_one", CreditedAmountCurrency: "EUR",
	}
	monitor := &CryptoMonitor{db: db, logger: logging.NewLogger(), priceFeed: NewPriceFeed(nil, logging.NewLogger())}

	monitor.allocateObservedDeposit(ctx, "", "0x"+"ab"+uuid.NewString()[:8], 123, "10000000", wallet)

	var status string
	var credited sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT status, credited_amount_cents FROM purser.crypto_wallets WHERE id = $1`, walletID).Scan(&status, &credited); err != nil {
		t.Fatal(err)
	}
	if status != "review_required" || credited.Valid {
		t.Fatalf("late receipt wallet status=%q credited=%v, want review_required without credit", status, credited)
	}
	var balances int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.prepaid_balances WHERE tenant_id = $1`, tenantID).Scan(&balances); err != nil {
		t.Fatal(err)
	}
	if balances != 0 {
		t.Fatalf("late receipt created %d balance rows", balances)
	}
}

func TestX402QuoteExpiryRefusesClaimAndRecordsLateSettlement_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	handler := &X402Handler{db: db, logger: logging.NewLogger()}

	insertQuote := func(t *testing.T, tenantID, status, expiresIn string) string {
		t.Helper()
		quoteID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.x402_payment_quotes (
				id, tenant_id, resource, resource_class, network, asset, pay_to,
				amount_atomic, credit_amount_cents, requirements_json, status, expires_at,
				original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur, fx_source, fx_reference_date
			) VALUES ($1, $2, 'mcp://test', 'mcp', 'eip155:8453',
			          '0x0000000000000000000000000000000000000001',
			          '0x0000000000000000000000000000000000000002',
			          5000000, 450, '{}', $3, NOW() + $4::interval,
			          500, 'USD', 450, 1.1111111111, 'ecb', CURRENT_DATE)
		`, quoteID, tenantID, status, expiresIn); err != nil {
			t.Fatalf("insert quote: %v", err)
		}
		return quoteID
	}

	t.Run("claim after expiry is refused", func(t *testing.T) {
		quoteID := insertQuote(t, uuid.NewString(), "offered", "-1 minute")
		claimed, err := handler.claimPaymentQuote(ctx, quoteID)
		if err != nil {
			t.Fatalf("claimPaymentQuote: %v", err)
		}
		if claimed {
			t.Fatal("expired quote was claimed")
		}
	})

	t.Run("settlement confirmed after expiry credits the quoted amount and records an anomaly", func(t *testing.T) {
		tenantID := uuid.NewString()
		quoteID := insertQuote(t, tenantID, "settling", "-5 minutes")
		nonceID := uuid.NewString()
		txHash := "0x" + "cd" + uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.x402_nonces (id, network, payer_address, nonce, tx_hash, tenant_id, amount_cents, status, quote_id)
			VALUES ($1, 'base', '0x0000000000000000000000000000000000000003', $2, $3, $4, 450, 'pending', $5)
		`, nonceID, "0x"+uuid.NewString(), txHash, tenantID, quoteID); err != nil {
			t.Fatalf("insert nonce: %v", err)
		}

		balance, err := confirmAndCreditX402Settlement(ctx, db, tenantID, 450, nonceID, txHash, 10, 21000)
		if err != nil {
			t.Fatalf("confirmAndCreditX402Settlement: %v", err)
		}
		if balance != 450 {
			t.Fatalf("balance = %d, want the quoted 450 cents", balance)
		}
		var kind, currency string
		var amount int64
		err = db.QueryRowContext(ctx, `
			SELECT kind, amount_cents, currency FROM purser.crypto_accounting_anomalies
			WHERE reference_type = 'x402_nonce' AND reference_id = $1 AND status = 'open'
		`, nonceID).Scan(&kind, &amount, &currency)
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatal("late x402 settlement recorded no accounting anomaly")
		}
		if err != nil {
			t.Fatal(err)
		}
		if kind != "x402_settlement_confirmed_after_quote_expiry" || amount != 450 || currency != billing.LedgerCurrency {
			t.Fatalf("anomaly = %q %d %s", kind, amount, currency)
		}
	})

	t.Run("settlement confirmed before expiry records no anomaly", func(t *testing.T) {
		tenantID := uuid.NewString()
		quoteID := insertQuote(t, tenantID, "settling", "5 minutes")
		nonceID := uuid.NewString()
		txHash := "0x" + "ef" + uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.x402_nonces (id, network, payer_address, nonce, tx_hash, tenant_id, amount_cents, status, quote_id)
			VALUES ($1, 'base', '0x0000000000000000000000000000000000000004', $2, $3, $4, 450, 'pending', $5)
		`, nonceID, "0x"+uuid.NewString(), txHash, tenantID, quoteID); err != nil {
			t.Fatalf("insert nonce: %v", err)
		}
		if _, err := confirmAndCreditX402Settlement(ctx, db, tenantID, 450, nonceID, txHash, 10, 21000); err != nil {
			t.Fatalf("confirmAndCreditX402Settlement: %v", err)
		}
		var anomalies int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.crypto_accounting_anomalies WHERE reference_id = $1`, nonceID).Scan(&anomalies); err != nil {
			t.Fatal(err)
		}
		if anomalies != 0 {
			t.Fatalf("on-time settlement recorded %d anomalies", anomalies)
		}
	})
}
