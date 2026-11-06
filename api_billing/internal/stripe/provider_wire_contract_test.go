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
	"strconv"
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
)

// Decode the same Python wire fixture through the real SDK used by Purser.
// Python-only assertions cannot catch request instructions leaking into typed
// response fields or expandable objects being replaced by scalar parameters.
// The fixture's server rules have separate Python contract tests; this checks
// SDK decoding and Purser's reactions, rather than validating Stripe itself.
func TestProviderWireActualSDKTaxUpgradeAndRecovery(t *testing.T) {
	for _, outcome := range []string{"succeeded", "declined", "requires_action", "processing", "past_invoice_failure", "past_finalize_failure"} {
		t.Run(outcome, func(t *testing.T) { providerWireTaxUpgradeAndRecovery(t, outcome) })
	}
}

func providerWireTaxUpgradeAndRecovery(t *testing.T, outcome string) {
	paymentOutcome := outcome
	pastFailure := outcome == "past_invoice_failure" || outcome == "past_finalize_failure"
	if pastFailure {
		paymentOutcome = "succeeded"
	}
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
	originalInvoice := sub.LatestInvoice.ID
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
	if err = c.HoldSubscriptionTaxReview(ctx, "wire-tenant", buyer.ID, sub.ID); err != nil {
		t.Fatal(err)
	}
	providerWireControl(t, baseURL, "/__test/renew", map[string]string{"subscription_id": sub.ID}, nil)
	heldSub, err := c.GetSubscription(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	heldID := heldSub.LatestInvoice.ID
	firstHold := heldSub.Metadata["frameworks_tax_hold_started"]
	var manual api.Invoice
	providerWireControl(t, baseURL, "/__test/unrelated_invoice", map[string]string{"subscription_id": sub.ID}, &manual)
	if manual.Status != api.InvoiceStatusOpen || manual.CollectionMethod != api.InvoiceCollectionMethodSendInvoice || manual.DueDate >= time.Now().Unix() {
		t.Fatal("manual invoice fixture is not an unrelated overdue invoice")
	}
	manualBefore, err := json.Marshal(manual)
	if err != nil {
		t.Fatal(err)
	}
	providerWireControl(t, baseURL, "/__test/payment_behavior", map[string]string{"customer_id": buyer.ID, "outcome": paymentOutcome}, nil)
	providerWireCleanSync(t, baseURL, func() error { return c.SyncSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile) })
	futureReady, err := c.GetSubscription(ctx, sub.ID)
	if err != nil || futureReady.PauseCollection != nil || futureReady.Metadata["frameworks_tax_hold_started"] != "" || futureReady.Metadata["frameworks_tax_recovery_pending"] == "" {
		t.Fatalf("future-only sync did not release owned hold and retain old tax work: %+v %v", futureReady, err)
	}
	untouched, err := invoice.Get(heldID, nil)
	if err != nil || untouched.Status != api.InvoiceStatusDraft || untouched.AutoAdvance || untouched.AmountPaid != 0 {
		t.Fatalf("future-only sync touched the historical draft: %+v %v", untouched, err)
	}
	if outcome == "past_invoice_failure" {
		providerWireControl(t, baseURL, "/__test/fault", map[string]any{"method": "POST", "path": "/v1/invoices/" + heldID + "/update_lines", "status": 500, "after_execution": true}, nil)
	}
	if outcome == "past_finalize_failure" {
		providerWireControl(t, baseURL, "/__test/fault", map[string]any{"method": "POST", "path": "/v1/invoices/" + heldID + "/finalize", "status": 500, "after_execution": false, "execution_started": true}, nil)
	}
	err = c.ReconcileSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile)
	if pastFailure {
		if err == nil {
			t.Fatal("old invoice recovery failure disappeared")
		}
		future, readErr := c.GetSubscription(ctx, sub.ID)
		if readErr != nil || future.PauseCollection != nil || future.Metadata["frameworks_tax_review"] != "" {
			t.Fatalf("past invoice failure paused correctly taxed future renewals: %+v %v", future, readErr)
		}
		if future.Metadata["frameworks_tax_hold_started"] != "" || future.Metadata["frameworks_tax_recovery_pending"] == "" {
			t.Fatal("unfinished old tax work lost its bounded recovery marker or retained an active hold")
		}
		if outcome == "past_finalize_failure" {
			draft, draftErr := invoice.Get(heldID, nil)
			if draftErr != nil || draft.Status != api.InvoiceStatusDraft {
				t.Fatalf("executed failure unexpectedly finalized invoice: %+v %v", draft, draftErr)
			}
		}
		err = c.ReconcileSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile)
	}
	if err != nil {
		t.Fatal(err)
	}
	held, err := invoice.Get(heldID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if held.Status != api.InvoiceStatusOpen || !held.AutoAdvance || held.AmountPaid != 0 || held.TotalExcludingTax != 5000 || held.Total != 6050 || held.Metadata["frameworks_tax_recovered_hold"] != "true" {
		t.Fatalf("same held renewal was not recovered with exclusive VAT: %+v", held)
	}
	resumed, err := c.GetSubscription(ctx, sub.ID)
	if err != nil || resumed.PauseCollection != nil || resumed.Metadata["frameworks_tax_review"] != "" {
		t.Fatalf("owned pause not released: %+v %v", resumed, err)
	}
	if resumed.Metadata["frameworks_tax_hold_started"] != "" || resumed.Metadata["frameworks_tax_recovery_pending"] != "" {
		t.Fatal("completed tax recovery retained an active or pending marker")
	}
	providerWireCleanSync(t, baseURL, func() error { return c.SyncSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile) })
	providerWireCleanSync(t, baseURL, func() error { return c.ReconcileSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile) })
	providerWireControl(t, baseURL, "/__test/collect", map[string]string{"invoice_id": heldID}, nil)
	pending, err := invoice.Get(heldID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if paymentOutcome != "succeeded" {
		if pending.Status != api.InvoiceStatusOpen || !pending.AutoAdvance || pending.AmountPaid != 0 {
			t.Fatalf("collection failure lost normal dunning: %+v", pending)
		}
		if err = c.SyncSubscriptionTax(ctx, "wire-tenant", buyer.ID, sub.ID, profile); err != nil {
			t.Fatalf("pending payment blocked tax synchronization: %v", err)
		}
		if paymentOutcome == "declined" {
			providerWireControl(t, baseURL, "/__test/payment_behavior", map[string]string{"customer_id": buyer.ID, "outcome": "succeeded"}, nil)
			providerWireControl(t, baseURL, "/__test/collect", map[string]any{"invoice_id": heldID, "at": pending.NextPaymentAttempt}, nil)
		} else {
			providerWireControl(t, baseURL, "/__test/settle", map[string]string{"invoice_id": heldID}, nil)
		}
	}
	settled, err := invoice.Get(heldID, nil)
	if err != nil || settled.Status != api.InvoiceStatusPaid || settled.AmountPaid != 6050 {
		t.Fatalf("later provider collection did not settle accepted invoice: %+v %v", settled, err)
	}
	manualAfter, err := invoice.Get(manual.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	manualBytes, err := json.Marshal(manualAfter)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manualBefore, manualBytes) {
		t.Fatal("unrelated manual outstanding invoice was modified")
	}
	original, err := invoice.Get(originalInvoice, nil)
	if err != nil || original.Total != 2000 || original.Status != api.InvoiceStatusPaid {
		t.Fatalf("original paid supply changed: %+v %v", original, err)
	}
	var requests []struct{ Method, Path string }
	providerWireControl(t, baseURL, "/__test/requests", nil, &requests)
	payments := 0
	for _, recorded := range requests {
		if recorded.Method == http.MethodPost && recorded.Path == "/v1/invoices/"+heldID+"/pay" {
			payments++
		}
	}
	if payments != 0 {
		t.Fatalf("recovery/replay collected %d times", payments)
	}
	if outcome == "succeeded" {
		started, parseErr := strconv.ParseInt(firstHold, 10, 64)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for time.Now().Unix() <= started {
			time.Sleep(10 * time.Millisecond)
		}
		if err = c.HoldSubscriptionTaxReview(ctx, "wire-tenant", buyer.ID, sub.ID); err != nil {
			t.Fatal(err)
		}
		later, readErr := c.GetSubscription(ctx, sub.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		laterStarted, parseErr := strconv.ParseInt(later.Metadata["frameworks_tax_hold_started"], 10, 64)
		if parseErr != nil || laterStarted <= started {
			t.Fatalf("later hold reused its completed cohort: previous=%s current=%+v error=%v", firstHold, later, readErr)
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
