//go:build schema_verify

package grpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"
	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	stripeapi "github.com/stripe/stripe-go/v85"
)

// paidTermStripe serves one subscription whose paid invoice line carries the
// price it was bought on, however the item's price changed afterwards.
type paidTermStripe struct {
	tenant, tierID, itemPrice string
	canceled                  bool
	termStart, termEnd        time.Time
	updates, cancels          int
}

func (s *paidTermStripe) subscription() map[string]any {
	status := "active"
	if s.canceled {
		status = "canceled"
	}
	return map[string]any{"id": "subscription", "object": "subscription", "customer": "customer", "status": status,
		"metadata":       map[string]string{"tenant_id": s.tenant, "tier_id": s.tierID},
		"latest_invoice": map[string]any{"id": "paid-invoice", "status": "paid"},
		"items": map[string]any{"data": []any{map[string]any{"id": "item", "quantity": 1,
			"price":                map[string]any{"id": s.itemPrice, "currency": "eur", "recurring": map[string]any{"interval": "month", "interval_count": 1}},
			"current_period_start": s.termStart.Unix(), "current_period_end": s.termEnd.Unix()}}}}
}

func (s *paidTermStripe) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	var body any
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/subscriptions/subscription":
		body = s.subscription()
	case r.Method == http.MethodPost && r.URL.Path == "/v1/subscriptions/subscription":
		s.updates++
		s.itemPrice, s.tierID = r.PostForm.Get("items[0][price]"), r.PostForm.Get("metadata[tier_id]")
		body = s.subscription()
	case r.Method == http.MethodDelete && r.URL.Path == "/v1/subscriptions/subscription":
		s.cancels++
		s.canceled = true
		body = s.subscription()
	case strings.HasPrefix(r.URL.Path, "/v1/prices/"):
		body = map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/v1/prices/"), "active": true, "currency": "eur", "unit_amount": 3000, "recurring": map[string]any{"interval": "month", "interval_count": 1}}
	case r.URL.Path == "/v1/invoices/paid-invoice":
		line := map[string]any{"id": "paid-base", "amount": 5000, "currency": "eur", "quantity": 1,
			"parent":  map[string]any{"type": "subscription_item_details", "subscription_item_details": map[string]any{"subscription": "subscription", "subscription_item": "item", "proration": false}},
			"pricing": map[string]any{"price_details": map[string]any{"price": "paid-price"}},
			"period":  map[string]any{"start": s.termStart.Unix(), "end": s.termEnd.Unix()}}
		unrelated := map[string]any{"id": "other", "amount": 1000, "currency": "eur", "quantity": 1,
			"pricing": map[string]any{"price_details": map[string]any{"price": "unrelated"}},
			"period":  map[string]any{"start": s.termStart.Unix(), "end": s.termEnd.AddDate(1, 0, 0).Unix()}}
		body = map[string]any{"id": "paid-invoice", "object": "invoice", "status": "paid", "amount_paid": 5000, "lines": map[string]any{"object": "list", "data": []any{line, unrelated}}}
	case r.URL.Path == "/v1/invoice_payments":
		body = map[string]any{"object": "list", "data": []any{map[string]any{"id": "payment", "payment": map[string]any{"type": "charge", "charge": "paid-charge"}}}}
	case r.URL.Path == "/v1/charges/paid-charge":
		body = map[string]any{"id": "paid-charge", "amount": 5000, "refunded": false, "disputed": false}
	default:
		t.Errorf("unexpected Stripe call %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Error(err)
	}
}

// The real Stripe stop finds the paid term by its subscription item after a
// confirmed mid-term price change, and a term paid past the staged effective time
// keeps the tier until it ends.
func TestFreeStopAfterAConfirmedPriceChange_RealPG(t *testing.T) {
	for _, tc := range []struct {
		name       string
		paidBeyond time.Duration
	}{{"term ends at the effective time", 0}, {"term paid past the effective time", 5 * 24 * time.Hour}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFreeDowngrade(t, "stripe")
			effective := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
			stripe := &paidTermStripe{tenant: f.tenant, tierID: f.paid, itemPrice: "paid-price", termStart: effective.AddDate(0, -1, 0), termEnd: effective.Add(tc.paidBeyond)}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { stripe.serve(t, w, r) }))
			t.Cleanup(server.Close)
			previous := stripeapi.GetBackend(stripeapi.APIBackend)
			stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{URL: stripeapi.String(server.URL), HTTPClient: server.Client(), MaxNetworkRetries: stripeapi.Int64(0)}))
			t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
			logger := logging.NewLogger()
			client := billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test_fixture", Logger: logger})
			f.service = handlers.NewService(f.db, logger, nil, nil, nil, client, nil)
			f.server.billing, f.server.stripeClient = f.service, client
			if _, err := f.change(t, f.low); err != nil {
				t.Fatal(err)
			}
			if err := f.service.ReconcileProviderTier(t.Context(), f.tenant); err != nil {
				t.Fatal(err)
			}
			if stripe.updates != 1 || stripe.itemPrice != "low-price" {
				t.Fatalf("confirmed mid-term price change: updates=%d price=%s", stripe.updates, stripe.itemPrice)
			}
			if _, err := f.change(t, f.free); err != nil {
				t.Fatal(err)
			}
			f.approachRenewalAt(t, effective)
			f.step(t)
			f.server.sweepFreeFallbackMovesOnce(t.Context())
			var failure string
			if err := f.db.QueryRowContext(t.Context(), `SELECT COALESCE(free_fallback_error,'') FROM purser.tenant_subscriptions WHERE tenant_id=$1`, f.tenant).Scan(&failure); err != nil {
				t.Fatal(err)
			}
			s := f.state(t)
			if failure != "" || stripe.cancels != 1 || !s.ended || s.tier != f.paid || !s.fallbackAt.Valid || !s.fallbackAt.Time.Equal(stripe.termEnd) {
				t.Fatalf("stop: error=%q cancels=%d state=%+v, want the paid term retained until %s", failure, stripe.cancels, s, stripe.termEnd)
			}
		})
	}
}
