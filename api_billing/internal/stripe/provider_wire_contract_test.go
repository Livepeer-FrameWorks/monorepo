package stripe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	api "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/checkout/session"
	"github.com/stripe/stripe-go/v85/customer"
	"github.com/stripe/stripe-go/v85/invoice"
	"github.com/stripe/stripe-go/v85/price"
	"github.com/stripe/stripe-go/v85/product"
	"github.com/stripe/stripe-go/v85/subscription"
)

// Decode the same Python wire fixture through the real SDK used by Purser.
// Python-only assertions cannot catch request instructions leaking into typed
// response fields or expandable objects being replaced by scalar parameters.
// The fixture's server rules have separate Python contract tests; this checks
// SDK decoding and Purser's reactions, rather than validating Stripe itself.
func TestProviderWireActualSDKTaxUpgradeAndRenewal(t *testing.T) {
	for _, outcome := range []string{"succeeded", "declined", "requires_action", "processing"} {
		t.Run(outcome, func(t *testing.T) { providerWireTaxUpgradeAndRenewal(t, outcome) })
	}
}

func providerWireTaxUpgradeAndRenewal(t *testing.T, outcome string) {
	baseURL := startPythonBillingProvider(t)
	previous := api.GetBackend(api.APIBackend)
	api.SetBackend(api.APIBackend, api.GetBackendWithConfig(api.APIBackend, &api.BackendConfig{URL: api.String(baseURL), MaxNetworkRetries: api.Int64(0)}))
	t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
	ctx := t.Context()
	c := newStripeClient()
	buyer, err := customer.New(&api.CustomerParams{Name: api.String("Wire Buyer"), Email: api.String("buyer@example.test"), Metadata: map[string]string{"tenant_id": "wire-tenant"}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := product.New(&api.ProductParams{Name: api.String("Support")})
	if err != nil {
		t.Fatal(err)
	}
	prices := []*api.Price{}
	for _, amount := range []int64{2000, 5000} {
		p, createErr := price.New(&api.PriceParams{Product: api.String(base.ID), Currency: api.String("eur"), UnitAmount: api.Int64(amount), Recurring: &api.PriceRecurringParams{Interval: api.String("month")}})
		if createErr != nil {
			t.Fatal(createErr)
		}
		prices = append(prices, p)
	}
	profile := assessedTaxProfile()
	profile.Country, profile.VATNumber, profile.RateBPS, profile.ReverseCharge, profile.Wording = "AT", "ATU12345678", 0, true, "VAT reverse charged / btw verlegd"
	rate, err := c.PrepareCustomerTax(ctx, "wire-tenant", buyer.ID, profile)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := c.CreateCheckoutSession(ctx, CheckoutSessionParams{CustomerID: buyer.ID, TenantID: "wire-tenant", TierID: "supporter", PriceID: prices[0].ID, Currency: "EUR", SuccessURL: "https://app.example.test/success", CancelURL: "https://app.example.test/cancel", TaxProfile: &profile, TaxRateID: rate, IdempotencyKey: "wire-checkout"})
	if err != nil {
		t.Fatal(err)
	}
	providerWireControl(t, baseURL, "/__test/complete", map[string]string{"session_id": checkout.ID}, nil)
	completed, err := session.Get(checkout.ID, nil)
	if err != nil || completed.Subscription == nil {
		t.Fatalf("checkout did not retain subscription: %+v %v", completed, err)
	}
	sub, err := c.GetSubscription(ctx, completed.Subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile = assessedTaxProfile()
	if err = c.SyncSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile); err != nil {
		t.Fatal(err)
	}
	item := sub.Items.Data[0]
	request := billing.TierUpgradeRequest{ID: "wire-upgrade", TenantID: "wire-tenant", CustomerID: buyer.ID, SubscriptionID: sub.ID, TierID: "production", PriceID: prices[1].ID, BaseCents: 5000, OldBaseCents: 2000, RequestedAt: time.Unix(item.CurrentPeriodStart, 0), OldPeriodStart: time.Unix(item.CurrentPeriodStart, 0), OldPeriodEnd: time.Unix(item.CurrentPeriodEnd, 0)}
	receipt, err := c.UpgradeMonthlySubscription(ctx, request)
	if err != nil || !receipt.Applied {
		t.Fatalf("real SDK paid upgrade: %+v %v", receipt, err)
	}
	upgraded, err := invoice.Get(receipt.PaidReceiptID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Status != api.InvoiceStatusPaid || upgraded.Total != 4050 || upgraded.TotalExcludingTax != 3000 {
		t.Fatalf("new EUR50+21%% less old untaxed EUR20: %+v", upgraded)
	}
	request.LocateOnly = true
	if replay, replayErr := c.UpgradeMonthlySubscription(ctx, request); replayErr != nil || replay.PaidReceiptID != receipt.PaidReceiptID {
		t.Fatalf("SDK upgrade replay: %+v %v", replay, replayErr)
	}
	var manual api.Invoice
	providerWireControl(t, baseURL, "/__test/unrelated_invoice", map[string]string{"subscription_id": sub.ID}, &manual)
	manualBefore, err := json.Marshal(manual)
	if err != nil {
		t.Fatal(err)
	}
	providerWireControl(t, baseURL, "/__test/payment_behavior", map[string]string{"customer_id": buyer.ID, "outcome": outcome}, nil)
	var created []struct{ Body string }
	providerWireControl(t, baseURL, "/__test/renew", map[string]string{"subscription_id": sub.ID}, &created)
	if len(created) != 1 {
		t.Fatalf("renewal created %d events", len(created))
	}
	var event api.Event
	if err = json.Unmarshal([]byte(created[0].Body), &event); err != nil {
		t.Fatal(err)
	}
	if event.APIVersion != api.APIVersion {
		t.Fatalf("fixture API version %q differs from SDK %q", event.APIVersion, api.APIVersion)
	}
	var renewal api.Invoice
	if err = json.Unmarshal(event.Data.Raw, &renewal); err != nil {
		t.Fatal(err)
	}
	if renewal.Status != api.InvoiceStatusDraft || !renewal.AutoAdvance || renewal.Total != 6050 {
		t.Fatalf("ordinary renewal lost automatic collection or exclusive VAT: %+v", renewal)
	}
	// Tax synchronization is future-only, even with a past-due invoice.
	providerWireCleanSync(t, baseURL, func() error { return c.SyncSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile) })
	providerWireControl(t, baseURL, "/__test/collect", map[string]string{"invoice_id": renewal.ID}, nil)
	pending, err := invoice.Get(renewal.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == "succeeded" {
		if pending.Status != api.InvoiceStatusPaid || pending.AmountPaid != 6050 {
			t.Fatalf("automatic renewal: %+v", pending)
		}
	} else {
		if pending.Status != api.InvoiceStatusOpen || pending.AmountPaid != 0 || !pending.AutoAdvance {
			t.Fatalf("pending renewal invented funds or lost collection: %+v", pending)
		}
		var objects map[string]json.RawMessage
		providerWireControl(t, baseURL, "/__test/objects", nil, &objects)
		var wire struct {
			Intent      string `json:"payment_intent"`
			NextAttempt int64  `json:"next_payment_attempt"`
		}
		if err = json.Unmarshal(objects[renewal.ID], &wire); err != nil {
			t.Fatal(err)
		}
		var intent struct{ Status string }
		if err = json.Unmarshal(objects[wire.Intent], &intent); err != nil {
			t.Fatal(err)
		}
		expected := outcome
		if outcome == "declined" {
			expected = "requires_payment_method"
		}
		if intent.Status != expected {
			t.Fatalf("payment state=%q want %q", intent.Status, expected)
		}
		providerWireCleanSync(t, baseURL, func() error { return c.SyncSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile) })
		if outcome == "declined" {
			if wire.NextAttempt <= time.Now().Unix() {
				t.Fatal("decline lost scheduled retry")
			}
			providerWireControl(t, baseURL, "/__test/payment_behavior", map[string]string{"invoice_id": renewal.ID, "outcome": "succeeded"}, nil)
			providerWireControl(t, baseURL, "/__test/collect", map[string]any{"invoice_id": renewal.ID, "at": wire.NextAttempt}, nil)
		} else {
			providerWireControl(t, baseURL, "/__test/settle", map[string]string{"invoice_id": renewal.ID}, nil)
		}
	}
	paid, err := invoice.Get(renewal.ID, nil)
	if err != nil || paid.Status != api.InvoiceStatusPaid || paid.AmountPaid != paid.Total {
		t.Fatalf("later settlement: %+v %v", paid, err)
	}
	providerWireControl(t, baseURL, "/__test/collect", map[string]string{"invoice_id": renewal.ID}, nil)
	var objects map[string]json.RawMessage
	providerWireControl(t, baseURL, "/__test/objects", nil, &objects)
	payments := 0
	for _, raw := range objects {
		var payment struct{ Object, Invoice, Status string }
		if err = json.Unmarshal(raw, &payment); err != nil {
			t.Fatal(err)
		}
		if payment.Object == "invoice_payment" && payment.Invoice == renewal.ID && payment.Status == "paid" {
			payments++
		}
	}
	if payments != 1 {
		t.Fatalf("repeated collection created %d payments", payments)
	}
	var manualAfter api.Invoice
	providerWireControl(t, baseURL, "/__test/objects", nil, &objects)
	if err = json.Unmarshal(objects[manual.ID], &manualAfter); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manualAfter)
	if err != nil || !bytes.Equal(encoded, manualBefore) {
		t.Fatal("tax sync changed unrelated overdue debt")
	}
	// A customer/operator pause is not owned by tax reconciliation.
	_, err = subscription.Update(sub.ID, &api.SubscriptionParams{Params: api.Params{Context: ctx}, PauseCollection: &api.SubscriptionPauseCollectionParams{Behavior: api.String("keep_as_draft")}, ProrationBehavior: api.String("none")})
	if err != nil {
		t.Fatal(err)
	}
	providerWireCleanSync(t, baseURL, func() error { return c.SyncSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile) })
	current, err := c.GetSubscription(ctx, sub.ID)
	if err != nil || current.PauseCollection == nil || current.PauseCollection.Behavior != api.SubscriptionPauseCollectionBehaviorKeepAsDraft {
		t.Fatalf("tax sync changed operator pause: %+v %v", current, err)
	}
	var requests []struct{ Method, Path, Body string }
	providerWireControl(t, baseURL, "/__test/requests", nil, &requests)
	for _, request := range requests {
		if strings.Contains(request.Body, "frameworks_tax_hold") || strings.Contains(request.Body, "frameworks_tax_recovery") || strings.Contains(request.Body, "fwtx_") {
			t.Fatalf("tax created obsolete hold work: %+v", request)
		}
		if request.Method == http.MethodPost && request.Path == "/v1/invoices/"+renewal.ID+"/pay" {
			t.Fatal("tax reconciliation forced a synchronous payment")
		}
	}
}

func providerWireCleanSync(t *testing.T, baseURL string, sync func() error) {
	t.Helper()
	var before, after []struct{ Method, Path string }
	providerWireControl(t, baseURL, "/__test/requests", nil, &before)
	if err := sync(); err != nil {
		t.Fatal(err)
	}
	providerWireControl(t, baseURL, "/__test/requests", nil, &after)
	for _, request := range after[len(before):] {
		if strings.HasPrefix(request.Path, "/v1/invoices") {
			t.Fatal("future-only or completed tax synchronization still touches historical invoices")
		}
	}
}

func providerWireControl(t *testing.T, baseURL, path string, body, target any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fixture control %s: HTTP%d", path, response.StatusCode)
	}
	if target != nil {
		if err = json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
}

func startPythonBillingProvider(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate provider wire fixture")
	}
	path := filepath.Join(filepath.Dir(source), "..", "..", "..", "scripts", "stack", "billing-provider.py")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	program := `import importlib.util,sys
spec=importlib.util.spec_from_file_location("provider_wire",sys.argv[1])
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
server=module.ThreadingHTTPServer(("127.0.0.1",0),module.Handler)
print("http://127.0.0.1:"+str(server.server_port),flush=True)
server.serve_forever()`
	cmd := exec.CommandContext(ctx, "python3", "-u", "-c", program, path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(stderr.String())
		}
	})
	reader := bufio.NewScanner(stdout)
	if !reader.Scan() {
		t.Fatal("Python provider did not expose its listener")
	}
	return reader.Text()
}
