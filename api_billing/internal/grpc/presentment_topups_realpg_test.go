//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/fx"
	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	stripelib "github.com/stripe/stripe-go/v85"
)

type checkoutSessionRequest struct {
	currency   string
	unitAmount string
}

// installStripeCheckoutFixture serves Stripe Checkout Session creation and
// records the currency and amount of every session Purser creates.
func installStripeCheckoutFixture(t *testing.T) func() []checkoutSessionRequest {
	t.Helper()
	var mu sync.Mutex
	var requests []checkoutSessionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" {
			http.Error(w, `{"error":{"message":"unexpected request"}}`, http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, checkoutSessionRequest{
			currency:   r.PostForm.Get("line_items[0][price_data][currency]"),
			unitAmount: r.PostForm.Get("line_items[0][price_data][unit_amount]"),
		})
		id := fmt.Sprintf("cs_test_%d", len(requests))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "object": "checkout.session", "url": "https://checkout.example/" + id,
			"expires_at": time.Now().Add(24 * time.Hour).Unix(),
		})
	}))
	t.Cleanup(server.Close)
	previous := stripelib.GetBackend(stripelib.APIBackend)
	stripelib.SetBackend(stripelib.APIBackend, stripelib.GetBackendWithConfig(stripelib.APIBackend, &stripelib.BackendConfig{
		URL: stripelib.String(server.URL), MaxNetworkRetries: stripelib.Int64(0),
	}))
	t.Cleanup(func() { stripelib.SetBackend(stripelib.APIBackend, previous) })
	appconfigtest.Set(t, "STRIPE_SECRET_KEY", "sk_test_presentment")
	return func() []checkoutSessionRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]checkoutSessionRequest(nil), requests...)
	}
}

func storeECBRates(t *testing.T, db *sql.DB, referenceDate time.Time, unitsPerEUR map[string]string) {
	t.Helper()
	rates := make([]fx.Rate, 0, len(unitsPerEUR))
	for currency, units := range unitsPerEUR {
		rates = append(rates, fx.Rate{
			Currency: currency, ReferenceDate: fx.Date(referenceDate),
			UnitsPerEUR: decimal.RequireFromString(units), Source: fx.SourceECB,
		})
	}
	if err := fx.Upsert(context.Background(), db, rates, time.Now()); err != nil {
		t.Fatalf("store ECB rates: %v", err)
	}
}

func TestCardTopupsCreditTheEURLockedAtCheckoutCreation_RealPG(t *testing.T) { //nolint:funlen // One engine run proves creation, rate change, and completion for three currencies.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	sessions := installStripeCheckoutFixture(t)
	logger := logging.NewLogger()
	server := &PurserServer{db: db, logger: logger}
	webhooks := handlers.NewService(db, logger, nil, nil, nil, nil, nil)

	tierID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers (id, tier_name, display_name) VALUES ($1, $2, 'Card top-up contract')`,
		tierID, "card-topup-"+tierID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	storeECBRates(t, db, now.AddDate(0, 0, -1), map[string]string{"USD": "1.2000", "GBP": "0.8000"})

	cases := []struct {
		country, currency string
		amountCents       int64
		wantEURCents      int64
		wantUnits         string
	}{
		{country: "NL", currency: "EUR", amountCents: 1200, wantEURCents: 1200, wantUnits: "1.0000000000"},
		{country: "US", currency: "USD", amountCents: 1200, wantEURCents: 1000, wantUnits: "1.2000000000"},
		{country: "GB", currency: "GBP", amountCents: 1200, wantEURCents: 1500, wantUnits: "0.8000000000"},
	}
	type created struct {
		tenantID, topupID, sessionID string
	}
	createdTopups := make([]created, len(cases))
	for index, tc := range cases {
		tenantID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email, billing_name)
			VALUES ($1, $2, 'active', 'prepaid', 'billing@example.com', 'Card Customer')
		`, tenantID, tierID); err != nil {
			t.Fatal(err)
		}
		if _, err := server.UpdateBillingDetails(ctx, &purserpb.UpdateBillingDetailsRequest{
			TenantId: tenantID,
			Address:  &purserpb.BillingAddress{Street: "1 Street", City: "City", PostalCode: "1000", Country: tc.country},
		}); err != nil {
			t.Fatalf("%s billing details: %v", tc.currency, err)
		}
		response, err := server.CreateCardTopup(ctx, &purserpb.CreateCardTopupRequest{
			TenantId: tenantID, AmountCents: tc.amountCents, Provider: "stripe",
			SuccessUrl: "https://app.example/ok", CancelUrl: "https://app.example/cancel",
		})
		if err != nil {
			t.Fatalf("%s CreateCardTopup: %v", tc.currency, err)
		}
		if response.GetCurrency() != tc.currency {
			t.Fatalf("%s top-up presented in %s", tc.currency, response.GetCurrency())
		}
		createdTopups[index] = created{tenantID: tenantID, topupID: response.GetTopupId(), sessionID: response.GetCheckoutId()}

		var original, eur int64
		var originalCurrency, units, source string
		if err := db.QueryRowContext(ctx, `
			SELECT original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur::text, fx_source
			FROM purser.pending_topups WHERE id = $1
		`, response.GetTopupId()).Scan(&original, &originalCurrency, &eur, &units, &source); err != nil {
			t.Fatal(err)
		}
		if original != tc.amountCents || originalCurrency != tc.currency || eur != tc.wantEURCents || units != tc.wantUnits {
			t.Fatalf("%s top-up FX = (%d %s, %d EUR, %s), want (%d %s, %d EUR, %s)",
				tc.currency, original, originalCurrency, eur, units, tc.amountCents, tc.currency, tc.wantEURCents, tc.wantUnits)
		}
		responseFX := response.GetFx()
		if responseFX.GetOriginalAmountCents() != tc.amountCents || responseFX.GetOriginalCurrency() != tc.currency ||
			responseFX.GetEurAmountCents() != tc.wantEURCents || responseFX.GetUnitsPerEur() != decimalText(tc.wantUnits) {
			t.Fatalf("%s response FX = %+v, want (%d %s, %d EUR, %s)",
				tc.currency, responseFX, tc.amountCents, tc.currency, tc.wantEURCents, tc.wantUnits)
		}
		pending, err := server.GetPendingTopup(ctx, &purserpb.GetPendingTopupRequest{Lookup: &purserpb.GetPendingTopupRequest_TopupId{TopupId: response.GetTopupId()}})
		if err != nil {
			t.Fatalf("%s GetPendingTopup: %v", tc.currency, err)
		}
		if pending.GetFx().GetEurAmountCents() != tc.wantEURCents || pending.GetFx().GetReferenceDate() != responseFX.GetReferenceDate() {
			t.Fatalf("%s pending top-up FX = %+v, want %+v", tc.currency, pending.GetFx(), responseFX)
		}
	}
	requests := sessions()
	if len(requests) != len(cases) {
		t.Fatalf("checkout sessions = %+v, want %d", requests, len(cases))
	}
	for index, tc := range cases {
		if !strings.EqualFold(requests[index].currency, tc.currency) || requests[index].unitAmount != fmt.Sprint(tc.amountCents) {
			t.Fatalf("%s checkout session = %+v, want %d %s", tc.currency, requests[index], tc.amountCents, strings.ToLower(tc.currency))
		}
	}

	// The rates move before the customers pay; completion credits the EUR
	// fixed when each checkout was created.
	storeECBRates(t, db, now, map[string]string{"USD": "1.5000", "GBP": "0.6000"})
	for index, tc := range cases {
		topup := createdTopups[index]
		session, err := json.Marshal(map[string]any{
			"id": topup.sessionID, "payment_intent": "pi_" + topup.topupID, "payment_status": "paid", "mode": "payment",
			"amount_total": tc.amountCents, "currency": strings.ToLower(tc.currency),
			"metadata": map[string]string{"purpose": string(handlers.PurposePrepaid), "tenant_id": topup.tenantID, "reference_id": topup.topupID},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := webhooks.DispatchStripeCheckoutCompleted(ctx, session); err != nil {
			t.Fatalf("%s checkout completion: %v", tc.currency, err)
		}
		if err := webhooks.DispatchStripeCheckoutCompleted(ctx, session); err != nil {
			t.Fatalf("%s replayed checkout completion: %v", tc.currency, err)
		}
		var balance int64
		if err := db.QueryRowContext(ctx, `SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = 'EUR'`,
			topup.tenantID).Scan(&balance); err != nil {
			t.Fatalf("%s balance: %v", tc.currency, err)
		}
		if balance != tc.wantEURCents {
			t.Fatalf("%s top-up credited %d EUR cents, want %d", tc.currency, balance, tc.wantEURCents)
		}
		var chargeAmount int64
		var chargeCurrency, settlementStatus string
		if err := db.QueryRowContext(ctx, `
			SELECT charge_amount_cents, charge_currency, status FROM purser.provider_settlements
			WHERE provider = 'stripe' AND provider_payment_id = $1
		`, "pi_"+topup.topupID).Scan(&chargeAmount, &chargeCurrency, &settlementStatus); err != nil {
			t.Fatalf("%s settlement row: %v", tc.currency, err)
		}
		if chargeAmount != tc.amountCents || chargeCurrency != tc.currency || settlementStatus != "pending" {
			t.Fatalf("%s settlement row = (%d %s %s)", tc.currency, chargeAmount, chargeCurrency, settlementStatus)
		}
	}
}

func TestUSDCDepositQuoteLocksEURAndAssetUnitsOnOneReferenceDate_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	logger := logging.NewLogger()
	server := &PurserServer{db: db, logger: logger, priceFeed: handlers.NewPriceFeed(nil, logger)}
	network := handlers.Networks[defaultNetworkForAsset("USDC")]
	now := time.Now().UTC()

	storeECBRates(t, db, now.AddDate(0, 0, -1), map[string]string{"USD": "1.2000", "GBP": "0.8000"})
	// A newer USD rate without a GBP rate on the same date cannot price GBP.
	storeECBRates(t, db, now, map[string]string{"USD": "1.1000"})
	if _, _, err := server.buildDepositQuote(ctx, network, "USDC", fx.GBP, 1000, 6); err == nil {
		t.Fatal("GBP deposit quote priced across two reference dates")
	}
	storeECBRates(t, db, now, map[string]string{"GBP": "0.5500"})

	cases := []struct {
		currency       string
		wantEUR        int64
		wantBaseUnits  string
		wantUnitsPrice string
	}{
		// 1000 GBP cents at 0.55 GBP/EUR is 1818 EUR cents and 20 USD at 1.10.
		{currency: fx.GBP, wantEUR: 1818, wantBaseUnits: "20000000", wantUnitsPrice: "0.55"},
		// 1000 USD cents at 1.10 USD/EUR is 909 EUR cents.
		{currency: fx.USD, wantEUR: 909, wantBaseUnits: "10000000", wantUnitsPrice: "1.1"},
		{currency: fx.EUR, wantEUR: 1000, wantBaseUnits: "11000000", wantUnitsPrice: "1"},
	}
	for _, tc := range cases {
		quote, baseUnits, err := server.buildDepositQuote(ctx, network, "USDC", tc.currency, 1000, 6)
		if err != nil {
			t.Fatalf("%s deposit quote: %v", tc.currency, err)
		}
		if quote.FX.EURMinor != tc.wantEUR || quote.FX.OriginalMinor != 1000 || quote.FX.OriginalCurrency != tc.currency ||
			!quote.FX.UnitsPerEUR.Equal(decimal.RequireFromString(tc.wantUnitsPrice)) || !quote.FX.ReferenceDate.Equal(fx.Date(now)) {
			t.Fatalf("%s quote FX = %+v", tc.currency, quote.FX)
		}
		if baseUnits.String() != tc.wantBaseUnits || quote.CreditedAmountCurrency != "EUR" {
			t.Fatalf("%s quote base units = %s credited in %s, want %s EUR", tc.currency, baseUnits, quote.CreditedAmountCurrency, tc.wantBaseUnits)
		}
	}
}
