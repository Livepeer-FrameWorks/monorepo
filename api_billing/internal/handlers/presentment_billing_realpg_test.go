//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_billing/internal/fx"
	billingmollie "frameworks/api_billing/internal/mollie"
	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	stripelib "github.com/stripe/stripe-go/v85"
	"google.golang.org/protobuf/proto"
)

// invoiceCreatedEvents returns tenantID's billing.invoice_created domain
// events by invoice ID, decoded from purser.domain_event_outbox.
func invoiceCreatedEvents(t *testing.T, db *sql.DB, tenantID string) map[string]*publicv1.InvoiceCreated {
	t.Helper()
	rows, err := db.Query(`
		SELECT aggregate_id, payload FROM purser.domain_event_outbox
		WHERE tenant_id = $1::uuid AND event_type = 'billing.invoice_created'`, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]*publicv1.InvoiceCreated{}
	for rows.Next() {
		var aggregateID string
		var payload []byte
		if err := rows.Scan(&aggregateID, &payload); err != nil {
			t.Fatal(err)
		}
		event := &publicv1.InvoiceCreated{}
		if err := proto.Unmarshal(payload, event); err != nil {
			t.Fatalf("decode invoice_created %s: %v", aggregateID, err)
		}
		out[aggregateID] = event
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

type recordedProviderRequest struct {
	method string
	path   string
	query  url.Values
	form   url.Values
}

type providerFixture struct {
	mu       sync.Mutex
	requests []recordedProviderRequest
	routes   map[string]func(recordedProviderRequest) (int, any)
}

func (f *providerFixture) handle(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	request := recordedProviderRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query(), form: r.PostForm}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	route := f.routes[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if route == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"no fixture route"}}`))
		return
	}
	statusCode, body := route(request)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *providerFixture) route(method, path string, respond func(recordedProviderRequest) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = respond
}

func (f *providerFixture) matching(method, path string) []recordedProviderRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedProviderRequest
	for _, request := range f.requests {
		if request.method == method && request.path == path {
			out = append(out, request)
		}
	}
	return out
}

// installStripeFixture points the Stripe SDK at an in-process fixture.
func installStripeFixture(t *testing.T) (*providerFixture, *billingstripe.Client) {
	t.Helper()
	fixture := &providerFixture{routes: map[string]func(recordedProviderRequest) (int, any){}}
	server := httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(server.Close)
	previous := stripelib.GetBackend(stripelib.APIBackend)
	stripelib.SetBackend(stripelib.APIBackend, stripelib.GetBackendWithConfig(stripelib.APIBackend, &stripelib.BackendConfig{
		URL: stripelib.String(server.URL), MaxNetworkRetries: stripelib.Int64(0),
	}))
	t.Cleanup(func() { stripelib.SetBackend(stripelib.APIBackend, previous) })
	return fixture, billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test_presentment", Logger: logging.NewLogger()})
}

type fixtureRoundTripper struct {
	handler http.Handler
}

func (rt fixtureRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	rt.handler.ServeHTTP(recorder, r)
	return recorder.Result(), nil
}

// installMollieFixture serves Mollie API calls made through the default HTTP
// client from an in-process fixture.
func installMollieFixture(t *testing.T) (*providerFixture, *billingmollie.Client) {
	t.Helper()
	fixture := &providerFixture{routes: map[string]func(recordedProviderRequest) (int, any){}}
	previous := http.DefaultClient.Transport
	http.DefaultClient.Transport = fixtureRoundTripper{handler: http.HandlerFunc(fixture.handle)}
	t.Cleanup(func() { http.DefaultClient.Transport = previous })
	client, err := billingmollie.NewClient(billingmollie.Config{APIKey: "test_presentment", Logger: logging.NewLogger()})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, client
}

func storeRates(t *testing.T, db *sql.DB, referenceDate time.Time, unitsPerEUR map[string]string) {
	t.Helper()
	for currency, units := range unitsPerEUR {
		storeRate(t, db, currency, referenceDate, units)
	}
}

func TestUSDCDepositCreditsTheLockedEURAndDocumentsEURVAT_RealPG(t *testing.T) { //nolint:funlen // One deposit is followed from event to credit and tax document.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tenantID := uuid.NewString()
	walletID := uuid.NewString()
	address := "0x00000000000000000000000000000000000000c7"
	// The quote priced 1000 GBP cents at 0.8 GBP per EUR: 1250 EUR cents and
	// 15 USDC at 1.2 USD per EUR.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.crypto_wallets (
			id, tenant_id, purpose, expected_amount_cents, asset, network, wallet_address,
			derivation_index, derivation_xpub, status, expires_at, expected_amount_base_units,
			quoted_price_usd, quoted_at, quote_source, credited_amount_currency, tax_document_kind,
			tax_profile_snapshot,
			original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur, fx_source, fx_reference_date
		) VALUES ($1, $2, 'prepaid', 1000, 'USDC', 'arbitrum', $3, 11, 'xpub-presentment', 'pending',
		          NOW() + INTERVAL '1 hour', 15000000, 1, $4, 'one_to_one', 'EUR', 'simplified', '{}',
		          1000, 'GBP', 1250, 0.8, 'ecb', $5)
	`, walletID, tenantID, address, now.Add(-time.Hour), fx.Date(now.AddDate(0, 0, -1))); err != nil {
		t.Fatalf("insert wallet: %v", err)
	}
	// The receipt pays 18 USDC, more than quoted, after the rates moved.
	storeRates(t, db, now, map[string]string{"USD": "1.0000", "GBP": "0.5000"})
	txHash := "0x" + strings.Repeat("7", 64)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.crypto_deposit_events (
			network, asset, tx_hash, log_index, block_number, block_hash, to_address,
			amount_base_units, status, confirmations, confirmed_at
		) VALUES ('arbitrum', 'USDC', $1, 0, 100, $2, $3, 18000000, 'confirmed', 100, NOW())
	`, txHash, "0x"+strings.Repeat("8", 64), address); err != nil {
		t.Fatalf("insert deposit event: %v", err)
	}

	monitor := &CryptoMonitor{
		db: db, logger: logging.NewLogger(), priceFeed: NewPriceFeed(nil, logging.NewLogger()),
		taxInvoices: &X402Handler{
			db: db, logger: logging.NewLogger(), supplierName: "FrameWorks B.V.", supplierAddress: "Amsterdam, NL",
			supplierVAT: "NL000000000B01", supplierRegistration: "12345678", supplierCountry: "NL",
			countryFromIP: func(string) string { return "" },
		},
	}
	monitor.allocateConfirmedDepositEvents(ctx)

	var balance int64
	if err := db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = 'EUR'`, tenantID).Scan(&balance); err != nil {
		t.Fatalf("balance: %v", err)
	}
	// 18/15 of the locked 1250 EUR cents.
	if balance != 1500 {
		t.Fatalf("credited balance = %d EUR cents, want 1500 at the locked quote", balance)
	}
	var currency, units, referenceDate string
	var gross, eur, netEUR, vatEUR int64
	var vatRateBps int
	if err := db.QueryRowContext(ctx, `
		SELECT currency, gross_amount_cents, amount_eur_cents, net_eur_cents, vat_eur_cents,
		       fx_units_per_eur::text, fx_reference_date::text, vat_rate_bps
		FROM purser.simplified_invoices WHERE tenant_id = $1 AND reference_id = $2
	`, tenantID, txHash).Scan(&currency, &gross, &eur, &netEUR, &vatEUR, &units, &referenceDate, &vatRateBps); err != nil {
		t.Fatalf("tax document: %v", err)
	}
	wantNet, wantVAT := extractVATInclusive(1500, vatRateBps)
	if currency != "GBP" || gross != 1200 || eur != 1500 || units != "0.8000000000" || referenceDate != fx.Date(now.AddDate(0, 0, -1)).Format(time.DateOnly) {
		t.Fatalf("tax document amounts = (%s %d, EUR %d, units %s, date %s)", currency, gross, eur, units, referenceDate)
	}
	if vatRateBps <= 0 || netEUR != wantNet || vatEUR != wantVAT || netEUR+vatEUR != 1500 {
		t.Fatalf("tax document EUR VAT = net %d vat %d at %d bps, want net %d vat %d", netEUR, vatEUR, vatRateBps, wantNet, wantVAT)
	}
}

func TestX402QuoteLocksTheUSDRateForCreditAndDocument_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	handler := &X402Handler{
		db: db, logger: logging.NewLogger(), topupUSDCents: 1000,
		supplierName: "FrameWorks B.V.", supplierAddress: "Amsterdam, NL", supplierVAT: "NL000000000B01",
		supplierRegistration: "12345678", supplierCountry: "NL", countryFromIP: func(string) string { return "NL" },
	}
	tenantID := uuid.NewString()
	network := Networks["base"]

	if _, err := handler.CreatePaymentQuote(ctx, tenantID, "mcp://tool", "0x0000000000000000000000000000000000000002", network); err == nil {
		t.Fatal("x402 quote created without an ECB USD rate")
	}
	storeRate(t, db, "USD", now.AddDate(0, 0, -6), "1.1000")
	if _, err := handler.CreatePaymentQuote(ctx, tenantID, "mcp://tool", "0x0000000000000000000000000000000000000002", network); err == nil {
		t.Fatal("x402 quote created from a stale ECB USD rate")
	}
	storeRate(t, db, "USD", now, "1.2500")
	quote, err := handler.CreatePaymentQuote(ctx, tenantID, "mcp://tool", "0x0000000000000000000000000000000000000002", network)
	if err != nil {
		t.Fatalf("CreatePaymentQuote: %v", err)
	}
	// The 1000 USD cent minimum at 1.25 USD per EUR credits 800 EUR cents.
	if quote.CreditAmountCents != 800 || quote.AmountAtomic != "10000000" || quote.FX.OriginalMinor != 1000 ||
		quote.FX.OriginalCurrency != fx.USD || !quote.FX.UnitsPerEUR.Equal(decimal.RequireFromString("1.25")) {
		t.Fatalf("quote = credit %d atomic %s FX %+v", quote.CreditAmountCents, quote.AmountAtomic, quote.FX)
	}

	storeRate(t, db, "USD", now, "1.0000")
	loaded, _, err := handler.loadPaymentQuote(ctx, tenantID, quote.ID)
	if err != nil {
		t.Fatalf("loadPaymentQuote: %v", err)
	}
	if loaded.CreditAmountCents != 800 || loaded.FX.EURMinor != 800 || !loaded.FX.UnitsPerEUR.Equal(decimal.RequireFromString("1.25")) {
		t.Fatalf("loaded quote after rate change = credit %d FX %+v", loaded.CreditAmountCents, loaded.FX)
	}

	nonceID := uuid.NewString()
	txHash := "0x" + strings.Repeat("9", 64)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.x402_nonces (id, network, payer_address, nonce, tx_hash, tenant_id, amount_cents, status, quote_id)
		VALUES ($1, 'base', '0x0000000000000000000000000000000000000003', $2, $3, $4, 800, 'pending', $5)
	`, nonceID, "0x"+uuid.NewString(), txHash, tenantID, quote.ID); err != nil {
		t.Fatalf("insert nonce: %v", err)
	}
	balance, err := confirmAndCreditX402Settlement(ctx, db, tenantID, loaded.CreditAmountCents, nonceID, txHash, 10, 21000)
	if err != nil || balance != 800 {
		t.Fatalf("confirmAndCreditX402Settlement = %d, %v; want 800 EUR cents", balance, err)
	}
	if _, err := handler.generateCryptoTopupInvoice(ctx, tenantID, loaded.CreditAmountCents, "x402_payment", txHash, "203.0.113.1", "base"); err != nil {
		t.Fatalf("generateCryptoTopupInvoice: %v", err)
	}
	var currency, units string
	var gross, eur, vatEUR int64
	var vatRateBps int
	if err := db.QueryRowContext(ctx, `
		SELECT currency, gross_amount_cents, amount_eur_cents, vat_eur_cents, fx_units_per_eur::text, vat_rate_bps
		FROM purser.simplified_invoices WHERE tenant_id = $1 AND reference_id = $2
	`, tenantID, txHash).Scan(&currency, &gross, &eur, &vatEUR, &units, &vatRateBps); err != nil {
		t.Fatalf("tax document: %v", err)
	}
	_, wantVATEUR := extractVATInclusive(800, vatRateBps)
	if currency != "USD" || gross != 1000 || eur != 800 || units != "1.2500000000" || vatEUR != wantVATEUR || vatEUR == 0 {
		t.Fatalf("x402 document = (%s %d, EUR %d, VAT EUR %d, units %s)", currency, gross, eur, vatEUR, units)
	}
}

func TestUSDInvoiceFinalizationPresentsAtTheFinalizationRate_RealPG(t *testing.T) { //nolint:funlen // One tenant is followed through a refused and a completed finalization.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	fixture, stripeClient := installStripeFixture(t)
	fixture.route(http.MethodGet, "/v1/customers/cus_presentment", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"id": "cus_presentment", "object": "customer",
			"invoice_settings": map[string]any{"default_payment_method": map[string]any{"id": "pm_presentment", "object": "payment_method"}},
		}
	})
	fixture.route(http.MethodPost, "/v1/payment_intents", func(request recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"id": "pi_" + request.form.Get("metadata[billing_payment_id]"), "object": "payment_intent",
			"status": "processing",
		}
	})

	tierID := uuid.NewString()
	tenantID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, metering_enabled)
		VALUES ($1, $2, 'Presentment postpaid', 20.00, 'EUR', false)
	`, tierID, "presentment-"+tierID); err != nil {
		t.Fatal(err)
	}
	periodStart := now.AddDate(0, 0, -40)
	periodEnd := now.AddDate(0, 0, -10)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (
			tenant_id, tier_id, status, billing_model, billing_email, payment_method, stripe_customer_id,
			billing_period_start, billing_period_end, presentment_currency
		) VALUES ($1, $2, 'active', 'postpaid', 'billing@example.com', 'stripe', 'cus_presentment', $3, $4, 'USD')
	`, tenantID, tierID, periodStart, periodEnd); err != nil {
		t.Fatal(err)
	}
	jobs := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{db: db, logger: logging.NewLogger(), stripeClient: stripeClient}}

	// Only a rate older than the five-day limit exists: finalization rolls back.
	storeRate(t, db, "USD", now.AddDate(0, 0, -8), "1.1000")
	jobs.generateMonthlyInvoices(ctx)
	var invoices int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id = $1`, tenantID).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	var storedPeriodEnd time.Time
	if err := db.QueryRowContext(ctx, `SELECT billing_period_end FROM purser.tenant_subscriptions WHERE tenant_id = $1`, tenantID).Scan(&storedPeriodEnd); err != nil {
		t.Fatal(err)
	}
	if invoices != 0 || storedPeriodEnd.Sub(periodEnd).Abs() > time.Millisecond || len(fixture.matching(http.MethodPost, "/v1/payment_intents")) != 0 {
		t.Fatalf("stale rate left %d invoices, period end %s, %d charges", invoices, storedPeriodEnd, len(fixture.matching(http.MethodPost, "/v1/payment_intents")))
	}
	if created := invoiceCreatedEvents(t, db, tenantID); len(created) != 0 {
		t.Fatalf("a rolled-back finalization left billing.invoice_created events: %v", created)
	}

	storeRate(t, db, "USD", now, "1.2000")
	jobs.generateMonthlyInvoices(ctx)

	var amount, units, currency, referenceDate string
	var presentmentCents int64
	if err := db.QueryRowContext(ctx, `
		SELECT amount::text, presentment_amount_cents, presentment_currency, presentment_units_per_eur::text, presentment_reference_date::text
		FROM purser.billing_invoices
		WHERE tenant_id = $1 AND period_start IS NOT NULL AND base_fee_period_start IS NULL
	`, tenantID).Scan(&amount, &presentmentCents, &currency, &units, &referenceDate); err != nil {
		t.Fatalf("period invoice: %v", err)
	}
	if amount != "20.00" || presentmentCents != 2400 || currency != "USD" || units != "1.2000000000" || referenceDate != fx.Date(now).Format(time.DateOnly) {
		t.Fatalf("period invoice = EUR %s presented %d %s at %s on %s", amount, presentmentCents, currency, units, referenceDate)
	}
	var baseFeeCents int64
	var baseFeeStart time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT presentment_amount_cents, base_fee_period_start FROM purser.billing_invoices
		WHERE tenant_id = $1 AND base_fee_period_start IS NOT NULL
	`, tenantID).Scan(&baseFeeCents, &baseFeeStart); err != nil {
		t.Fatalf("advance base-fee invoice: %v", err)
	}
	if baseFeeCents != 2400 || baseFeeStart.Sub(periodEnd).Abs() > time.Millisecond {
		t.Fatalf("advance base-fee invoice = %d USD cents from %s", baseFeeCents, baseFeeStart)
	}
	// Both issued invoices committed billing.invoice_created with their EUR
	// amount due, keyed by invoice.
	created := invoiceCreatedEvents(t, db, tenantID)
	var issued []string
	if err := func() error {
		rows, err := db.QueryContext(ctx, `SELECT id::text FROM purser.billing_invoices WHERE tenant_id = $1 ORDER BY id`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			issued = append(issued, id)
		}
		return rows.Err()
	}(); err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 || len(issued) != 2 {
		t.Fatalf("invoice_created events = %v for invoices %v, want one per issued invoice", created, issued)
	}
	for _, id := range issued {
		event, ok := created[id]
		if !ok || event.GetInvoiceId() != id || event.GetAmountDue().GetAmountMinor() != 2000 || event.GetAmountDue().GetCurrency() != "EUR" {
			t.Fatalf("invoice %s created event = %v, want EUR 2000 due", id, event)
		}
	}

	charges := fixture.matching(http.MethodPost, "/v1/payment_intents")
	if len(charges) != 2 {
		t.Fatalf("off-session charges = %d, want the period invoice and the advance base fee", len(charges))
	}
	for _, charge := range charges {
		if charge.form.Get("currency") != "usd" || charge.form.Get("amount") != "2400" || charge.form.Get("payment_method") != "pm_presentment" {
			t.Fatalf("off-session charge = %v, want 2400 usd on the default payment method", charge.form)
		}
	}
	rows, err := db.QueryContext(ctx, `
		SELECT p.original_amount_cents, p.original_currency, p.eur_amount_cents, p.fx_units_per_eur::text
		FROM purser.billing_payments p JOIN purser.billing_invoices i ON i.id = p.invoice_id
		WHERE i.tenant_id = $1
	`, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	payments := 0
	for rows.Next() {
		var original, eurCents int64
		var originalCurrency, paymentUnits string
		if err := rows.Scan(&original, &originalCurrency, &eurCents, &paymentUnits); err != nil {
			t.Fatal(err)
		}
		if original != 2400 || originalCurrency != "USD" || eurCents != 2000 || paymentUnits != "1.2000000000" {
			t.Fatalf("payment FX = (%d %s, EUR %d, %s)", original, originalCurrency, eurCents, paymentUnits)
		}
		payments++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if payments != 2 {
		t.Fatalf("pending payments = %d, want 2", payments)
	}
}

func TestStripeSettlementFromChargeUpdatedAndRetry_RealPG(t *testing.T) { //nolint:funlen // Webhook, retry, and not-yet-available paths share one fixture.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	fixture, stripeClient := installStripeFixture(t)
	service := &Service{db: db, logger: logging.NewLogger(), stripeClient: stripeClient}
	queries := newProviderSettlementQueries(db)
	tenantID := uuid.NewString()
	created := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

	fixture.route(http.MethodGet, "/v1/balance_transactions/txn_webhook", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"id": "txn_webhook", "object": "balance_transaction", "amount": 1000, "fee": 59, "net": 941,
			"currency": "eur", "exchange_rate": 0.8333, "created": created.Unix(),
		}
	})
	fixture.route(http.MethodGet, "/v1/payment_intents/pi_retry", func(request recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"id": "pi_retry", "object": "payment_intent",
			"latest_charge": map[string]any{
				"id": "ch_retry", "object": "charge",
				"balance_transaction": map[string]any{
					"id": "txn_retry", "object": "balance_transaction", "amount": 1500, "fee": 45, "net": 1455,
					"currency": "eur", "exchange_rate": 1.25, "created": created.Unix(),
				},
			},
		}
	})
	fixture.route(http.MethodGet, "/v1/payment_intents/pi_unsettled", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{"id": "pi_unsettled", "object": "payment_intent", "latest_charge": map[string]any{"id": "ch_unsettled", "object": "charge"}}
	})

	queries.insertPending(t, tenantID, "stripe", "pi_webhook", 1200, "USD")
	queries.insertPending(t, tenantID, "stripe", "pi_retry", 1200, "GBP")
	queries.insertPending(t, tenantID, "stripe", "pi_unsettled", 800, "USD")
	queries.insertPending(t, tenantID, "stripe", "pi_recent", 800, "USD")
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.provider_settlements SET updated_at = NOW() - INTERVAL '11 minutes'
		WHERE provider_payment_id IN ('pi_retry', 'pi_unsettled')
	`); err != nil {
		t.Fatal(err)
	}

	charge, err := json.Marshal(map[string]any{"id": "ch_webhook", "payment_intent": "pi_webhook", "balance_transaction": "txn_webhook", "currency": "usd", "amount": 1200})
	if err != nil {
		t.Fatal(err)
	}
	payload := StripeWebhookPayload{ID: "evt_charge_updated", Type: "charge.updated"}
	payload.Data.Object = charge
	if err := service.handleStripeChargeUpdated(payload); err != nil {
		t.Fatalf("charge.updated: %v", err)
	}
	queries.assertSettled(t, "stripe", "pi_webhook", "txn_webhook", 1000, 59, 941, "0.8333000000")

	if err := service.retryStripeSettlements(ctx, time.Now()); err != nil {
		t.Fatalf("retryStripeSettlements: %v", err)
	}
	queries.assertSettled(t, "stripe", "pi_retry", "txn_retry", 1500, 45, 1455, "1.2500000000")
	queries.assertPending(t, "stripe", "pi_unsettled")
	queries.assertPending(t, "stripe", "pi_recent")
	if got := len(fixture.matching(http.MethodGet, "/v1/payment_intents/pi_recent")); got != 0 {
		t.Fatalf("a settlement pending for less than the retry interval was read %d times", got)
	}
	retryRead := fixture.matching(http.MethodGet, "/v1/payment_intents/pi_retry")
	if len(retryRead) != 1 || retryRead[0].query.Get("expand[0]") != "latest_charge.balance_transaction" {
		t.Fatalf("retry payment intent reads = %+v", retryRead)
	}
	var touched bool
	if err := db.QueryRowContext(ctx, `SELECT updated_at > NOW() - INTERVAL '1 minute' FROM purser.provider_settlements WHERE provider_payment_id = 'pi_unsettled'`).Scan(&touched); err != nil {
		t.Fatal(err)
	}
	if !touched {
		t.Fatal("an unavailable settlement was not pushed back by the retry interval")
	}

	// A second retry does not read the row it just pushed back.
	if err := service.retryStripeSettlements(ctx, time.Now()); err != nil {
		t.Fatalf("second retryStripeSettlements: %v", err)
	}
	if got := len(fixture.matching(http.MethodGet, "/v1/payment_intents/pi_unsettled")); got != 1 {
		t.Fatalf("unavailable settlement read %d times across two retries, want 1", got)
	}
}

func TestMollieSettlementReadsBalanceTransactionsBackToTheCursor_RealPG(t *testing.T) { //nolint:funlen // Two reader runs share the cursor under test.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	fixture, mollieClient := installMollieFixture(t)
	service := &Service{db: db, logger: logging.NewLogger(), mollieClient: mollieClient}
	queries := newProviderSettlementQueries(db)
	tenantID := uuid.NewString()
	now := time.Now().UTC()

	transaction := func(id, kind, paymentID string, createdAt time.Time, initial, deductions, result string) map[string]any {
		tx := map[string]any{
			"resource": "balance_transaction", "id": id, "type": kind, "createdAt": createdAt.Format(time.RFC3339),
			"initialAmount": map[string]string{"currency": "EUR", "value": initial},
			"resultAmount":  map[string]string{"currency": "EUR", "value": result},
		}
		if deductions != "" {
			tx["deductions"] = map[string]string{"currency": "EUR", "value": deductions}
		}
		if paymentID != "" {
			tx["context"] = map[string]any{"paymentId": paymentID}
		}
		return tx
	}
	page := func(transactions []map[string]any, nextFrom string) map[string]any {
		body := map[string]any{"_embedded": map[string]any{"balance_transactions": transactions}, "_links": map[string]any{}}
		if nextFrom != "" {
			body["_links"] = map[string]any{"next": map[string]string{"href": "https://api.mollie.com/v2/balances/bal_primary/transactions?from=" + nextFrom + "&limit=250"}}
		}
		return body
	}
	fixture.route(http.MethodGet, "/v2/balances/primary", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]string{"resource": "balance", "id": "bal_primary"}
	})
	newest := []map[string]any{
		transaction("baltr_3", "payment", "tr_gbp", now.Add(-2*time.Hour), "11.60", "-0.40", "11.20"),
		transaction("baltr_2", "refund", "tr_other", now.Add(-3*time.Hour), "-5.00", "", "-5.00"),
	}
	older := []map[string]any{
		transaction("baltr_1", "payment", "tr_eur", now.Add(-4*time.Hour), "20.00", "-0.29", "19.71"),
	}
	fixture.route(http.MethodGet, "/v2/balances/bal_primary/transactions", func(request recordedProviderRequest) (int, any) {
		if request.query.Get("from") == "baltr_1" {
			return http.StatusOK, page(older, "")
		}
		return http.StatusOK, page(newest, "baltr_1")
	})

	queries.insertPending(t, tenantID, "mollie", "tr_gbp", 1000, "GBP")
	queries.insertPending(t, tenantID, "mollie", "tr_eur", 2000, "EUR")
	if err := service.readMollieSettlements(ctx, time.Now()); err != nil {
		t.Fatalf("readMollieSettlements: %v", err)
	}
	queries.assertSettled(t, "mollie", "tr_gbp", "baltr_3", 1160, 40, 1120, "1.1600000000")
	queries.assertSettled(t, "mollie", "tr_eur", "baltr_1", 2000, 29, 1971, "")
	if pages := len(fixture.matching(http.MethodGet, "/v2/balances/bal_primary/transactions")); pages != 2 {
		t.Fatalf("first read fetched %d pages, want 2", pages)
	}
	var cursorID string
	if err := db.QueryRowContext(ctx, `SELECT last_transaction_id FROM purser.mollie_balance_cursors WHERE balance_id = 'bal_primary'`).Scan(&cursorID); err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursorID != "baltr_3" {
		t.Fatalf("cursor = %s, want the newest transaction older than the lag", cursorID)
	}

	// A new payment settles within the lag; the next read stops at the cursor
	// without paging and leaves the cursor where it was.
	newest = append([]map[string]any{
		transaction("baltr_4", "payment", "tr_usd", now.Add(-10*time.Minute), "9.00", "-0.30", "8.70"),
	}, newest...)
	queries.insertPending(t, tenantID, "mollie", "tr_usd", 1000, "USD")
	if err := service.readMollieSettlements(ctx, time.Now()); err != nil {
		t.Fatalf("second readMollieSettlements: %v", err)
	}
	queries.assertSettled(t, "mollie", "tr_usd", "baltr_4", 900, 30, 870, "0.9000000000")
	if pages := len(fixture.matching(http.MethodGet, "/v2/balances/bal_primary/transactions")); pages != 3 {
		t.Fatalf("reads fetched %d pages in total, want 3", pages)
	}
	if err := db.QueryRowContext(ctx, `SELECT last_transaction_id FROM purser.mollie_balance_cursors WHERE balance_id = 'bal_primary'`).Scan(&cursorID); err != nil {
		t.Fatal(err)
	}
	if cursorID != "baltr_3" {
		t.Fatalf("cursor after second read = %s, want baltr_3", cursorID)
	}
}

// A pending settlement that never settles does not hold the Mollie cursor back,
// and a settlement row written long after its payment reads the balance again
// from that payment's time.
func TestMollieSettlementRereadsLateRowsWithoutPinningTheCursor_RealPG(t *testing.T) { //nolint:funlen // Three reader runs share the cursor under test.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	fixture, mollieClient := installMollieFixture(t)
	service := &Service{db: db, logger: logging.NewLogger(), mollieClient: mollieClient}
	queries := newProviderSettlementQueries(db)
	now := time.Now().UTC()

	transaction := func(id, paymentID string, createdAt time.Time) map[string]any {
		return map[string]any{
			"resource": "balance_transaction", "id": id, "type": "payment", "createdAt": createdAt.Format(time.RFC3339),
			"initialAmount": map[string]string{"currency": "EUR", "value": "10.00"},
			"resultAmount":  map[string]string{"currency": "EUR", "value": "9.71"},
			"deductions":    map[string]string{"currency": "EUR", "value": "-0.29"},
			"context":       map[string]any{"paymentId": paymentID},
		}
	}
	pages := map[string][]map[string]any{
		"":        {transaction("baltr_a1", "tr_recent", now.Add(-30*time.Minute)), transaction("baltr_a2", "tr_unrelated_a2", now.Add(-2*time.Hour))},
		"baltr_b": {transaction("baltr_b", "tr_late", now.Add(-26*time.Hour))},
		"baltr_c": {transaction("baltr_c", "tr_unrelated_c", now.Add(-74*time.Hour))},
	}
	next := map[string]string{"": "baltr_b", "baltr_b": "baltr_c"}
	fixture.route(http.MethodGet, "/v2/balances/primary", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]string{"resource": "balance", "id": "bal_primary"}
	})
	fixture.route(http.MethodGet, "/v2/balances/bal_primary/transactions", func(request recordedProviderRequest) (int, any) {
		from := request.query.Get("from")
		body := map[string]any{"_embedded": map[string]any{"balance_transactions": pages[from]}, "_links": map[string]any{}}
		if nextFrom := next[from]; nextFrom != "" {
			body["_links"] = map[string]any{"next": map[string]string{"href": "https://api.mollie.com/v2/balances/bal_primary/transactions?from=" + nextFrom + "&limit=250"}}
		}
		return http.StatusOK, body
	})
	pagesRead := func() int { return len(fixture.matching(http.MethodGet, "/v2/balances/bal_primary/transactions")) }

	// A recent charge settles and moves the cursor to the newest transaction
	// older than the lag.
	tenantID := seedPrepaidLedgerTenant(t, db)
	queries.insertPending(t, tenantID, "mollie", "tr_recent", 1000, "EUR")
	if err := service.readMollieSettlements(ctx, time.Now()); err != nil {
		t.Fatalf("first readMollieSettlements: %v", err)
	}
	queries.assertSettled(t, "mollie", "tr_recent", "baltr_a1", 1000, 29, 971, "")

	// The webhook of a payment made 26 hours ago arrives only now.
	topupID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.pending_topups (id, tenant_id, provider, checkout_id, amount_cents, currency, status, expires_at,
		    original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur, fx_source, fx_reference_date)
		VALUES ($1, $2, 'mollie', 'tr_late', 1000, 'EUR', 'pending', NOW() + INTERVAL '1 hour', 1000, 'EUR', 1000, 1, 'identity', CURRENT_DATE)
	`, topupID, tenantID); err != nil {
		t.Fatalf("insert pending top-up: %v", err)
	}
	fixture.route(http.MethodGet, "/v2/payments/tr_late", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"resource": "payment", "id": "tr_late", "status": "paid", "sequenceType": "oneoff",
			"amount":    map[string]string{"currency": "EUR", "value": "10.00"},
			"createdAt": now.Add(-26 * time.Hour).Add(-time.Minute).Format(time.RFC3339),
			"paidAt":    now.Add(-26 * time.Hour).Format(time.RFC3339),
			"metadata":  map[string]string{"tenant_id": tenantID, "purpose": "prepaid", "topup_id": topupID},
		}
	})
	if _, err := service.handleMolliePaymentWebhook(ctx, "tr_late", []byte(`id=tr_late`)); err != nil {
		t.Fatalf("late Mollie webhook: %v", err)
	}
	queries.assertPending(t, "mollie", "tr_late")
	if err := service.readMollieSettlements(ctx, time.Now()); err != nil {
		t.Fatalf("readMollieSettlements after the late row: %v", err)
	}
	queries.assertSettled(t, "mollie", "tr_late", "baltr_b", 1000, 29, 971, "")

	// A charge whose balance transaction never appears stays pending without
	// making every later read page back to it.
	queries.insertPending(t, tenantID, "mollie", "tr_stuck", 1000, "EUR")
	if _, err := db.ExecContext(ctx, `UPDATE purser.provider_settlements SET created_at = NOW() - INTERVAL '3 days' WHERE provider_payment_id = 'tr_stuck'`); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		before := pagesRead()
		if err := service.readMollieSettlements(ctx, time.Now()); err != nil {
			t.Fatalf("readMollieSettlements with a stuck row: %v", err)
		}
		if fetched := pagesRead() - before; run == 2 && fetched != 1 {
			t.Errorf("read %d behind a stuck pending row fetched %d pages, want only the page back to the cursor", run, fetched)
		}
	}
	queries.assertPending(t, "mollie", "tr_stuck")
}

type providerSettlementQueries struct {
	db *sql.DB
}

func newProviderSettlementQueries(db *sql.DB) providerSettlementQueries {
	return providerSettlementQueries{db: db}
}

func (q providerSettlementQueries) insertPending(t *testing.T, tenantID, provider, paymentID string, amountCents int64, currency string) {
	t.Helper()
	if _, err := q.db.ExecContext(context.Background(), `
		INSERT INTO purser.provider_settlements (tenant_id, provider, provider_payment_id, charge_amount_cents, charge_currency)
		VALUES ($1, $2, $3, $4, $5)
	`, tenantID, provider, paymentID, amountCents, currency); err != nil {
		t.Fatalf("insert pending settlement %s: %v", paymentID, err)
	}
}

func (q providerSettlementQueries) assertSettled(t *testing.T, provider, paymentID, balanceTransactionID string, settled, fee, net int64, exchangeRate string) {
	t.Helper()
	var status, currency string
	var gotBalanceTransaction, gotRate sql.NullString
	var gotSettled, gotFee, gotNet sql.NullInt64
	if err := q.db.QueryRowContext(context.Background(), `
		SELECT status, provider_balance_transaction_id, settled_amount_cents, fee_cents, net_cents,
		       COALESCE(settlement_currency, ''), exchange_rate::text
		FROM purser.provider_settlements WHERE provider = $1 AND provider_payment_id = $2
	`, provider, paymentID).Scan(&status, &gotBalanceTransaction, &gotSettled, &gotFee, &gotNet, &currency, &gotRate); err != nil {
		t.Fatalf("load settlement %s: %v", paymentID, err)
	}
	if status != "settled" || gotBalanceTransaction.String != balanceTransactionID || gotSettled.Int64 != settled ||
		gotFee.Int64 != fee || gotNet.Int64 != net || currency != "EUR" || gotRate.String != exchangeRate {
		t.Fatalf("settlement %s = (%s, %s, %d, %d, %d, %s, %s), want (settled, %s, %d, %d, %d, EUR, %s)",
			paymentID, status, gotBalanceTransaction.String, gotSettled.Int64, gotFee.Int64, gotNet.Int64, currency, gotRate.String,
			balanceTransactionID, settled, fee, net, exchangeRate)
	}
}

func (q providerSettlementQueries) assertPending(t *testing.T, provider, paymentID string) {
	t.Helper()
	var status string
	if err := q.db.QueryRowContext(context.Background(), `SELECT status FROM purser.provider_settlements WHERE provider = $1 AND provider_payment_id = $2`,
		provider, paymentID).Scan(&status); err != nil {
		t.Fatalf("load settlement %s: %v", paymentID, err)
	}
	if status != "pending" {
		t.Fatalf("settlement %s status = %s, want pending", paymentID, status)
	}
}
