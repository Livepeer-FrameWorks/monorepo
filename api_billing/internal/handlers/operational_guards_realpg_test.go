//go:build schema_verify

package handlers

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestOperationalDatabaseGuards_RealPG(t *testing.T) { //nolint:funlen // One schema fixture covers three small operational repositories.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()

	t.Run("VIES evidence is typed and cached", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
				<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
				  <soap:Body><checkVatResponse><countryCode>NL</countryCode><vatNumber>123456789B01</vatNumber><requestDate>2026-08-21</requestDate><valid>true</valid><name>Example BV</name><address>Leiden</address></checkVatResponse></soap:Body>
				</soap:Envelope>`))
		}))
		defer server.Close()
		t.Setenv("VIES_ENDPOINT", server.URL)
		handler := &X402Handler{db: db}
		tenantID := uuid.NewString()
		for attempt := 0; attempt < 2; attempt++ {
			valid, err := handler.validateVIESVAT(ctx, tenantID, "nl", "NL123456789B01")
			if err != nil || !valid {
				t.Fatalf("validation attempt %d = %v, %v", attempt, valid, err)
			}
		}
		if requests.Load() != 1 {
			t.Fatalf("VIES requests = %d, want one request plus one DB cache hit", requests.Load())
		}
		var raw string
		if err := db.QueryRowContext(ctx, `SELECT raw_response::text FROM purser.vat_validation_evidence WHERE tenant_id = $1`, tenantID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(raw, "Example BV") {
			t.Fatalf("raw VIES JSON = %s", raw)
		}
	})

	t.Run("x402 quote claim is compare-and-swap", func(t *testing.T) {
		queries := purserdb.New(db)
		tenantID := uuid.NewString()
		quoteID := uuid.NewString()
		create := func(id string) {
			t.Helper()
			if err := queries.CreateX402PaymentQuote(ctx, purserdb.CreateX402PaymentQuoteParams{
				ID: id, TenantID: tenantID, Resource: "graphql://contract", ResourceClass: "graphql",
				Network: "eip155:8453", Asset: "0x0000000000000000000000000000000000000001",
				PayTo: "0x0000000000000000000000000000000000000002", AmountAtomic: "5000000",
				CreditAmountCents: 500, EurPerUsdRate: "0.9000000000",
				RequirementsJson: json.RawMessage(`{"scheme":"exact"}`), TaxDocumentKind: "simplified",
				TaxProfileSnapshot: json.RawMessage(`{}`), ExpiresAt: time.Now().UTC().Add(time.Minute),
			}); err != nil {
				t.Fatal(err)
			}
		}
		create(quoteID)
		handler := &X402Handler{db: db}
		claimed, err := handler.claimPaymentQuote(ctx, quoteID)
		if err != nil || !claimed {
			t.Fatalf("first claim = %v, %v", claimed, err)
		}
		claimed, err = handler.claimPaymentQuote(ctx, quoteID)
		if err != nil || claimed {
			t.Fatalf("second claim = %v, %v", claimed, err)
		}

		expiringID := uuid.NewString()
		create(expiringID)
		if err := handler.ExpirePaymentQuote(ctx, expiringID); err != nil {
			t.Fatal(err)
		}
		stored, err := queries.GetX402PaymentQuote(ctx, purserdb.GetX402PaymentQuoteParams{QuoteID: expiringID, TenantID: tenantID})
		if err != nil || stored.Status != "expired" {
			t.Fatalf("expired quote = %q, %v", stored.Status, err)
		}
	})

	t.Run("HD wallet allocation persists quote and custody atomically", func(t *testing.T) {
		wallet := NewHDWallet(db, logging.NewLogger())
		if err := wallet.InitializeHDWallet(ctx, testXpub, "mainnet"); err != nil {
			t.Fatal(err)
		}
		expectedCents := int64(500)
		rate := decimal.RequireFromString("0.92")
		walletID, address, err := wallet.GenerateDepositAddress(DepositAddressParams{
			TenantID: uuid.NewString(), Purpose: "prepaid", Asset: "USDC", Network: "ethereum",
			ExpiresAt: time.Now().UTC().Add(time.Hour), ClientIP: "203.0.113.5",
			ExpectedAmountCents: &expectedCents,
			Quote: &DepositQuote{
				ExpectedAmountBaseUnits: big.NewInt(5_000_000), QuotedPriceUSD: decimal.NewFromInt(1),
				QuotedUSDToEURRate: &rate, QuotedAt: time.Now().UTC(), QuoteSource: "one_to_one",
				CreditedAmountCurrency: "EUR", TaxDocumentKind: "simplified", TaxProfile: CryptoBillingProfile{},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		var derivationIndex int
		var storedAddress, snapshot string
		if err := db.QueryRowContext(ctx, `
			SELECT derivation_index, wallet_address, tax_profile_snapshot::text
			FROM purser.crypto_wallets WHERE id = $1
		`, walletID).Scan(&derivationIndex, &storedAddress, &snapshot); err != nil {
			t.Fatal(err)
		}
		if derivationIndex != 1 || storedAddress != address || !json.Valid([]byte(snapshot)) {
			t.Fatalf("wallet evidence = index %d address %q snapshot %q", derivationIndex, storedAddress, snapshot)
		}
		var custodyCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.crypto_custody_addresses WHERE source_ref = $1`, walletID).Scan(&custodyCount); err != nil {
			t.Fatal(err)
		}
		if custodyCount != 1 {
			t.Fatalf("custody rows = %d, want 1", custodyCount)
		}
	})

	t.Run("checkout top-up replay credits once", func(t *testing.T) {
		tenantID := uuid.NewString()
		topupID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.pending_topups (id, tenant_id, provider, amount_cents, currency, expires_at)
			VALUES ($1, $2, 'stripe', 1500, 'EUR', NOW() + INTERVAL '1 hour')
		`, topupID, tenantID); err != nil {
			t.Fatal(err)
		}
		service := &Service{db: db, logger: logging.NewLogger()}
		for attempt := 0; attempt < 2; attempt++ {
			if err := service.handlePrepaidCheckoutCompleted(ctx, "cs_contract", "pi_contract", tenantID, topupID, 1500, "EUR", ProviderStripe, true); err != nil {
				t.Fatalf("checkout attempt %d: %v", attempt, err)
			}
		}
		var balance int64
		var topupStatus string
		var transactionCount int
		if err := db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = 'EUR'`, tenantID).Scan(&balance); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT status FROM purser.pending_topups WHERE id = $1`, topupID).Scan(&topupStatus); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id = $1 AND reference_id = $2`, tenantID, topupID).Scan(&transactionCount); err != nil {
			t.Fatal(err)
		}
		if balance != 1500 || topupStatus != "completed" || transactionCount != 1 {
			t.Fatalf("top-up state = balance %d status %q transactions %d", balance, topupStatus, transactionCount)
		}
	})

	t.Run("checkout settlement mismatch cannot credit money", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			provider     CheckoutProvider
			amountCents  int64
			currency     string
			wrongSession bool
		}{
			{name: "provider", provider: ProviderMollie, amountCents: 1500, currency: "EUR"},
			{name: "amount", provider: ProviderStripe, amountCents: 1499, currency: "EUR"},
			{name: "currency", provider: ProviderStripe, amountCents: 1500, currency: "USD"},
			{name: "session", provider: ProviderStripe, amountCents: 1500, currency: "EUR", wrongSession: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tenantID := uuid.NewString()
				topupID := uuid.NewString()
				expectedSession := "cs_" + uuid.NewString()
				if _, err := db.ExecContext(ctx, `
					INSERT INTO purser.pending_topups
					    (id, tenant_id, provider, checkout_id, amount_cents, currency, expires_at)
					VALUES ($1, $2, 'stripe', $3, 1500, 'EUR', NOW() + INTERVAL '1 hour')
				`, topupID, tenantID, expectedSession); err != nil {
					t.Fatal(err)
				}

				service := &Service{db: db, logger: logging.NewLogger()}
				receivedSession := expectedSession
				if tc.wrongSession {
					receivedSession = "cs_forged_" + uuid.NewString()
				}
				if err := service.handlePrepaidCheckoutCompleted(ctx, receivedSession, "pi_forged", tenantID, topupID, tc.amountCents, tc.currency, tc.provider, true); err == nil {
					t.Fatal("mismatched provider evidence was accepted")
				}

				var status string
				var providerPaymentID *string
				if err := db.QueryRowContext(ctx, `SELECT status, provider_payment_id FROM purser.pending_topups WHERE id = $1`, topupID).Scan(&status, &providerPaymentID); err != nil {
					t.Fatal(err)
				}
				var balanceRows, transactionRows int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.prepaid_balances WHERE tenant_id = $1`, tenantID).Scan(&balanceRows); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id = $1`, tenantID).Scan(&transactionRows); err != nil {
					t.Fatal(err)
				}
				if status != "pending" || providerPaymentID != nil || balanceRows != 0 || transactionRows != 0 {
					t.Fatalf("mismatch mutated state: status=%q payment=%v balances=%d transactions=%d", status, providerPaymentID, balanceRows, transactionRows)
				}
			})
		}
	})

	t.Run("invoice settlement mismatch cannot confirm value", func(t *testing.T) {
		tenantID := uuid.NewString()
		invoiceID := uuid.NewString()
		paymentID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.billing_invoices (id, tenant_id, status, currency, amount, due_date)
			VALUES ($1, $2, 'pending', 'EUR', 12.50, NOW() + INTERVAL '7 days')
		`, invoiceID, tenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.billing_payments (id, invoice_id, method, amount, currency, tx_id, status)
			VALUES ($2, $1, 'card', 12.50, 'EUR', 'pi_expected', 'pending')
		`, invoiceID, paymentID); err != nil {
			t.Fatal(err)
		}

		service := &Service{db: db, logger: logging.NewLogger()}
		updated, err := service.updateInvoicePaymentStatus("stripe", "pi_expected", invoiceID, "confirmed", providerSettlementEvidence{
			TenantID: tenantID, AmountCents: 1249, Currency: "EUR",
		})
		if err == nil || updated {
			t.Fatalf("mismatched invoice settlement = updated %v, error %v", updated, err)
		}

		var paymentStatus, invoiceStatus string
		if err := db.QueryRowContext(ctx, `SELECT status FROM purser.billing_payments WHERE id = $1`, paymentID).Scan(&paymentStatus); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT status FROM purser.billing_invoices WHERE id = $1`, invoiceID).Scan(&invoiceStatus); err != nil {
			t.Fatal(err)
		}
		if paymentStatus != "pending" || invoiceStatus != "pending" {
			t.Fatalf("mismatch mutated invoice state: payment=%q invoice=%q", paymentStatus, invoiceStatus)
		}
	})

	t.Run("prepaid suspension accepts nullable email", func(t *testing.T) {
		tierID := uuid.NewString()
		tenantID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.billing_tiers (id, tier_name, display_name)
			VALUES ($1, $2, 'Operational guard tier')
		`, tierID, "operational-"+uuid.NewString()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.tenant_subscriptions (id, tenant_id, tier_id, status, billing_model, billing_email)
			VALUES ($1, $2, $3, 'active', 'prepaid', NULL)
		`, uuid.NewString(), tenantID, tierID); err != nil {
			t.Fatal(err)
		}
		enforcer := NewThresholdEnforcer(db, logging.NewLogger(), nil, nil, nil)
		if err := enforcer.EnforcePrepaidThresholds(ctx, tenantID, 10, suspensionThresholdCents-1); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := db.QueryRowContext(ctx, `SELECT status FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "suspended" {
			t.Fatalf("subscription status = %q", status)
		}
	})

	t.Run("x402 minute window increments atomically", func(t *testing.T) {
		handler := &X402Handler{db: db}
		for attempt := 1; attempt <= 2; attempt++ {
			if err := handler.consumeX402RateLimit(ctx, "contract", "same-client", 2); err != nil {
				t.Fatalf("allowed request %d: %v", attempt, err)
			}
		}
		if err := handler.consumeX402RateLimit(ctx, "contract", "same-client", 2); err == nil {
			t.Fatal("third request did not exceed the two-request limit")
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT request_count FROM purser.x402_rate_limit_windows WHERE scope = 'contract'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 3 {
			t.Fatalf("stored request count = %d, want 3", count)
		}
	})
}
