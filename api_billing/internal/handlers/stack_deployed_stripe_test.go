//go:build stack_verify

package handlers_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func stackBillingConnection(t *testing.T) (context.Context, *grpc.ClientConn, *sql.DB, string) {
	return stackBillingConnectionTimeout(t, 90*time.Second)
}

func stackBillingConnectionTimeout(t *testing.T, timeout time.Duration) (context.Context, *grpc.ClientConn, *sql.DB, string) {
	t.Helper()
	tenant := os.Getenv("STACK_BILLING_TENANT_ID")
	if tenant == "" || os.Getenv("STACK_BILLING_DEPLOYED") != "1" {
		t.Skip("run through stack scenario 54")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+os.Getenv("SERVICE_TOKEN"))
	conn, err := grpc.NewClient("purser:19003", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	db, err := sql.Open("postgres", os.Getenv("STACK_BILLING_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return ctx, conn, db, tenant
}

func stackProviderControl(t *testing.T, ctx context.Context, path string, input interface{}, out interface{}) {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://billing-provider:18778/__test/"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("provider simulator %s: status=%d body=%s error=%v", path, resp.StatusCode, raw, err)
	}
	if err = json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

type stackProviderEvent struct {
	Body      string `json:"body"`
	Signature string `json:"signature"`
}

func deliverStackProviderEvents(t *testing.T, ctx context.Context, conn *grpc.ClientConn, db *sql.DB, tenant string, events []stackProviderEvent) {
	t.Helper()
	client := purserpb.NewWebhookServiceClient(conn)
	for _, event := range events {
		var identity struct {
			ID   string `json:"id"`
			Data struct {
				Object struct {
					Metadata map[string]string `json:"metadata"`
				} `json:"object"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(event.Body), &identity); err != nil {
			t.Fatal(err)
		}
		if identity.ID == "" || identity.Data.Object.Metadata["tenant_id"] != tenant {
			t.Fatal("provider fixture event lacks tenant ownership")
		}
		// Re-delivery uses the same signed event identity and must stay harmless.
		for range 2 {
			result, err := client.ProcessWebhook(ctx, &sharedpb.WebhookRequest{Provider: "stripe", Body: []byte(event.Body), Headers: map[string]string{"Stripe-Signature": event.Signature}, SourceIp: "192.0.2.1", ReceivedAt: time.Now().Unix()})
			if err != nil || !result.GetSuccess() {
				t.Fatalf("deployed signed webhook: %v %+v body=%s", err, result, event.Body)
			}
		}
		// HTTP acceptance only persists the signed receipt. Wait for the deployed
		// inbox worker to finish before inspecting the resulting money/documents.
		ticker := time.NewTicker(200 * time.Millisecond)
		deadline := time.NewTimer(60 * time.Second)
		for {
			var state, detail string
			err := db.QueryRowContext(ctx, `WITH scoped AS (SELECT event_key,status,last_error,convert_from(raw_payload,'UTF8')::jsonb#>>'{data,object,metadata,tenant_id}' AS tenant_id FROM purser.provider_webhook_inbox WHERE provider='stripe' AND event_key=$1) SELECT status,COALESCE(last_error,'') FROM scoped WHERE tenant_id=$2`, identity.ID, tenant).Scan(&state, &detail)
			if err != nil && err != sql.ErrNoRows {
				t.Fatal(err)
			}
			if state == "processed" {
				break
			}
			if state == "failed" {
				t.Fatalf("deployed inbox failed event %s: %s", identity.ID, detail)
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				t.Fatalf("deployed inbox did not process event %s: %s %s", identity.ID, state, detail)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		ticker.Stop()
		deadline.Stop()
	}
}

func TestStackDeployedCardTopupTax(t *testing.T) {
	ctx, conn, db, tenant := stackBillingConnection(t)
	response, err := purserpb.NewPrepaidServiceClient(conn).CreateCardTopup(ctx, &purserpb.CreateCardTopupRequest{TenantId: tenant, AmountCents: 1000, Provider: "stripe", SuccessUrl: "https://app.stack.test/billing/success", CancelUrl: "https://app.stack.test/billing/cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if response.AmountCents != 1210 || response.Currency != "EUR" {
		t.Fatalf("exclusive NL topup must collect EUR12.10: %+v", response)
	}
	var events []stackProviderEvent
	stackProviderControl(t, ctx, "complete", map[string]string{"session_id": response.CheckoutId}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
	var count int
	var net, vat, gross, lot, credit int64
	err = db.QueryRowContext(ctx, `SELECT count(*),min(net_eur_cents),min(vat_eur_cents),min(gross_eur_cents) FROM purser.billing_invoices WHERE tenant_id=$1 AND source_reference_type='prepaid_topup' AND source_reference_id=$2`, tenant, response.TopupId).Scan(&count, &net, &vat, &gross)
	if err != nil {
		t.Fatal(err)
	}
	err = db.QueryRowContext(ctx, `SELECT net_credit_eur_cents FROM purser.prepaid_funding_lots WHERE tenant_id=$1 AND source_reference_type='prepaid_topup' AND source_reference_id=$2`, tenant, response.TopupId).Scan(&lot)
	if err != nil {
		t.Fatal(err)
	}
	err = db.QueryRowContext(ctx, `SELECT b.amount_cents FROM purser.pending_topups p JOIN purser.balance_transactions b ON b.tenant_id=p.tenant_id AND b.id=p.balance_transaction_id WHERE p.tenant_id=$1 AND p.id=$2`, tenant, response.TopupId).Scan(&credit)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || net != 1000 || vat != 210 || gross != 1210 || lot != 1000 || credit != 1000 {
		t.Fatalf("topup doc count=%d net=%d vat=%d gross=%d lot=%d credit=%d", count, net, vat, gross, lot, credit)
	}
	t.Log("deployed NL card topup collected1210, issued exactly one tax invoice, credited1000 once")
}

func TestStackDeployedStripeRenewalTax(t *testing.T) {
	ctx, conn, db, tenant := stackBillingConnection(t)
	var tier string
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM purser.billing_tiers WHERE tier_name='production'`).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	response, err := purserpb.NewStripeServiceClient(conn).CreateCheckoutSession(ctx, &purserpb.CreateStripeCheckoutRequest{TenantId: tenant, TierId: tier, BillingPeriod: "monthly", SuccessUrl: "https://app.stack.test/billing/success", CancelUrl: "https://app.stack.test/billing/cancel"})
	if err != nil {
		t.Fatal(err)
	}
	var events []stackProviderEvent
	stackProviderControl(t, ctx, "complete", map[string]string{"session_id": response.SessionId}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
	var subscription string
	if err = db.QueryRowContext(ctx, `SELECT stripe_subscription_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&subscription); err != nil {
		t.Fatal(err)
	}
	stackProviderControl(t, ctx, "renew", map[string]string{"subscription_id": subscription}, &events)
	var invoice struct {
		Data struct {
			Object struct {
				ID string `json:"id"`
			} `json:"object"`
		} `json:"data"`
	}
	if err = json.Unmarshal([]byte(events[len(events)-1].Body), &invoice); err != nil {
		t.Fatal(err)
	}
	stackProviderControl(t, ctx, "collect", map[string]string{"invoice_id": invoice.Data.Object.ID}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
	var count, ownedCount int
	var net, vat, gross int64
	var hasPDF, hasProof bool
	err = db.QueryRowContext(ctx, `SELECT count(*),min(net_minor),min(vat_minor),min(gross_minor),bool_and(original_pdf IS NOT NULL AND pdf_sha256 IS NOT NULL),bool_and(tax_assessment <> '{}'::jsonb) FROM purser.provider_billing_documents WHERE tenant_id=$1 AND provider_document_id=$2 AND document_kind='provider_invoice'`, tenant, invoice.Data.Object.ID).Scan(&count, &net, &vat, &gross, &hasPDF, &hasProof)
	if err != nil {
		t.Fatal(err)
	}
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND source_reference_id=$2`, tenant, invoice.Data.Object.ID).Scan(&ownedCount)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || ownedCount != 0 || net <= 0 || vat != (net*2100+5000)/10000 || gross != net+vat || !hasPDF || !hasProof {
		t.Fatalf("renewal documents provider=%d duplicatePurser=%d net=%d vat=%d gross=%d pdf=%v proof=%v", count, ownedCount, net, vat, gross, hasPDF, hasProof)
	}
	t.Logf("deployed Stripe renewal %s retained one original tax document/PDF with21%%exclusiveVAT", invoice.Data.Object.ID)
}

func TestStackDeployedStripePaidUpgradeOriginalTax(t *testing.T) {
	ctx, conn, db, tenant := stackBillingConnection(t)
	var oldTier, newTier string
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM purser.billing_tiers WHERE tier_name='supporter'`).Scan(&oldTier); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM purser.billing_tiers WHERE tier_name='production'`).Scan(&newTier); err != nil {
		t.Fatal(err)
	}
	checkout, err := purserpb.NewStripeServiceClient(conn).CreateCheckoutSession(ctx, &purserpb.CreateStripeCheckoutRequest{TenantId: tenant, TierId: oldTier, BillingPeriod: "monthly", SuccessUrl: "https://app.stack.test/billing/success", CancelUrl: "https://app.stack.test/billing/cancel"})
	if err != nil {
		t.Fatal(err)
	}
	var events []stackProviderEvent
	stackProviderControl(t, ctx, "complete", map[string]string{"session_id": checkout.SessionId}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
	var subscription, originalInvoice string
	if err = db.QueryRowContext(ctx, `SELECT stripe_subscription_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&subscription); err != nil {
		t.Fatal(err)
	}
	var source struct {
		Data struct {
			Object struct {
				ID  string `json:"id"`
				VAT []struct {
					Amount int64 `json:"amount"`
				} `json:"total_taxes"`
			} `json:"object"`
		} `json:"data"`
	}
	if err = json.Unmarshal([]byte(events[len(events)-1].Body), &source); err != nil {
		t.Fatal(err)
	}
	originalInvoice = source.Data.Object.ID
	for _, tax := range source.Data.Object.VAT {
		if tax.Amount != 0 {
			t.Fatal("paid upgrade fixture must start with an actually zero-tax source invoice")
		}
	}
	typeName, vatNumber := "consumer", ""
	_, err = purserpb.NewSubscriptionServiceClient(conn).UpdateBillingDetails(ctx, &purserpb.UpdateBillingDetailsRequest{TenantId: tenant, CustomerType: &typeName, VatNumber: &vatNumber, Address: &purserpb.BillingAddress{Street: "1 Main Street", City: "Amsterdam", PostalCode: "1000AA", Country: "NL"}})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := purserpb.NewPrepaidServiceClient(conn).ChangeBillingTier(ctx, &purserpb.ChangeBillingTierRequest{TenantId: tenant, TierId: newTier})
	if err != nil || !changed.GetSuccess() {
		t.Fatalf("deployed paid upgrade was not applied: %+v %v", changed, err)
	}
	if changed.GetAppliedTierId() != newTier {
		if changed.GetPendingTierId() != newTier {
			t.Fatalf("accepted upgrade has neither the requested applied nor pending tier: %+v", changed)
		}
		deadline := time.NewTimer(time.Minute)
		defer deadline.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			var applied bool
			if err = db.QueryRowContext(ctx, `SELECT tier_id=$2 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, newTier).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied {
				break
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				var state, receipt string
				_ = db.QueryRowContext(ctx, `SELECT state,provider_receipt::text FROM purser.subscription_tier_upgrades WHERE tenant_id=$1 ORDER BY requested_at DESC LIMIT 1`, tenant).Scan(&state, &receipt)
				t.Fatalf("accepted upgrade did not apply within one minute: state=%s receipt=%s response=%+v", state, receipt, changed)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	stackProviderControl(t, ctx, "invoice_events", map[string]string{"subscription_id": subscription}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
	var upgraded struct {
		Data struct {
			Object struct {
				ID    string `json:"id"`
				Net   int64  `json:"total_excluding_tax"`
				Total int64  `json:"total"`
				Lines struct {
					Data []struct {
						Amount   int64             `json:"amount"`
						Metadata map[string]string `json:"metadata"`
						Taxes    []struct {
							Amount int64 `json:"amount"`
						} `json:"taxes"`
					} `json:"data"`
				} `json:"lines"`
			} `json:"object"`
		} `json:"data"`
	}
	if err = json.Unmarshal([]byte(events[len(events)-1].Body), &upgraded); err != nil {
		t.Fatal(err)
	}
	var credit, positive, positiveVAT int64
	for _, line := range upgraded.Data.Object.Lines.Data {
		if line.Amount < 0 {
			if line.Metadata["frameworks_credit_invoice"] != originalInvoice {
				t.Fatal("upgrade credit lost original paid invoice identity")
			}
			credit += line.Amount
			for _, tax := range line.Taxes {
				if tax.Amount != 0 {
					t.Fatal("old zero-tax fee credit was retaxed")
				}
			}
		} else {
			positive += line.Amount
			for _, tax := range line.Taxes {
				positiveVAT += tax.Amount
			}
		}
	}
	if credit >= 0 || positive <= 0 || positiveVAT != (positive*2100+5000)/10000 || upgraded.Data.Object.Net != positive+credit || upgraded.Data.Object.Total != positive+credit+positiveVAT {
		t.Fatalf("paid upgrade original0/new21 credit=%d replacement=%d replacementVAT=%d net=%d total=%d", credit, positive, positiveVAT, upgraded.Data.Object.Net, upgraded.Data.Object.Total)
	}
	var retained int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_billing_documents WHERE tenant_id=$1 AND provider_document_id=$2 AND net_minor=$3 AND vat_minor=$4 AND gross_minor=$5 AND original_pdf IS NOT NULL`, tenant, upgraded.Data.Object.ID, positive+credit, positiveVAT, positive+credit+positiveVAT).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("paid upgrade did not retain exactly one original taxed document: %d %v", retained, err)
	}
	t.Logf("deployed paid upgrade retained original zero-tax credit %d and new 21%% VAT %d", credit, positiveVAT)
}

func TestStackDeployedStripeHeldRenewalRecovery(t *testing.T) {
	// The deployed reconciliation worker runs every five minutes. Exercise its
	// real cadence instead of replacing it with a test-only production trigger.
	ctx, conn, db, tenant := stackBillingConnectionTimeout(t, 15*time.Minute)
	var tier string
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM purser.billing_tiers WHERE tier_name='supporter'`).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	checkout, err := purserpb.NewStripeServiceClient(conn).CreateCheckoutSession(ctx, &purserpb.CreateStripeCheckoutRequest{TenantId: tenant, TierId: tier, BillingPeriod: "monthly", SuccessUrl: "https://app.stack.test/billing/success", CancelUrl: "https://app.stack.test/billing/cancel"})
	if err != nil {
		t.Fatal(err)
	}
	var events []stackProviderEvent
	stackProviderControl(t, ctx, "complete", map[string]string{"session_id": checkout.SessionId}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
	var subscription string
	if err = db.QueryRowContext(ctx, `SELECT stripe_subscription_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&subscription); err != nil {
		t.Fatal(err)
	}
	type subscriptionSnapshot struct {
		Metadata map[string]string `json:"metadata"`
		Customer string            `json:"customer"`
		Status   string            `json:"status"`
		Pause    *struct {
			Behavior string `json:"behavior"`
		} `json:"pause_collection"`
		Latest string `json:"latest_invoice"`
	}
	read := func() (subscriptionSnapshot, map[string]json.RawMessage) {
		var objects map[string]json.RawMessage
		stackProviderControl(t, ctx, "objects", nil, &objects)
		var sub subscriptionSnapshot
		if err := json.Unmarshal(objects[subscription], &sub); err != nil {
			t.Fatal(err)
		}
		return sub, objects
	}
	beforeHold, _ := read()
	var configured map[string]bool
	stackProviderControl(t, ctx, "payment_behavior", map[string]string{"customer_id": beforeHold.Customer, "outcome": "declined"}, &configured)
	stackProviderControl(t, ctx, "renew", map[string]string{"subscription_id": subscription}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
	uncollected, _ := read()
	pastDueID := uncollected.Latest
	stackProviderControl(t, ctx, "collect", map[string]string{"invoice_id": pastDueID}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
	alreadyPastDue, pastDueObjects := read()
	if alreadyPastDue.Status != "past_due" || alreadyPastDue.Pause != nil {
		t.Fatalf("subscription was not already past_due before its tax hold: %+v", alreadyPastDue)
	}
	pastDueMoney, err := stackInvoiceFinancialSnapshot(pastDueObjects[pastDueID])
	if err != nil {
		t.Fatal(err)
	}
	stackProviderControl(t, ctx, "payment_behavior", map[string]string{"customer_id": beforeHold.Customer, "outcome": "succeeded"}, &configured)
	stackProviderControl(t, ctx, "vies", map[string]any{"vat_number": "U99999999", "valid": false}, &configured)
	typeName, vat := "business", "ATU99999999"
	_, err = purserpb.NewSubscriptionServiceClient(conn).UpdateBillingDetails(ctx, &purserpb.UpdateBillingDetailsRequest{TenantId: tenant, CustomerType: &typeName, VatNumber: &vat, Address: &purserpb.BillingAddress{Street: "1 Main Street", City: "Vienna", PostalCode: "1000", Country: "AT"}})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(label string, condition func(subscriptionSnapshot, map[string]json.RawMessage) bool) {
		t.Helper()
		deadline := time.NewTimer(7 * time.Minute)
		defer deadline.Stop()
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			sub, objects := read()
			if condition(sub, objects) {
				return
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				t.Fatalf("deployed worker did not %s", label)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	wait("hold unproven VAT subscription", func(sub subscriptionSnapshot, _ map[string]json.RawMessage) bool {
		return sub.Status == "past_due" && sub.Pause != nil && sub.Pause.Behavior == "keep_as_draft" && sub.Metadata["frameworks_tax_review"] == "true" && sub.Metadata["frameworks_tax_pause_owned"] == "true"
	})
	stackProviderControl(t, ctx, "renew", map[string]string{"subscription_id": subscription}, &events)
	var draftEvent struct {
		Data struct {
			Object struct {
				ID, Status string
				AmountPaid int64 `json:"amount_paid"`
			} `json:"object"`
		} `json:"data"`
	}
	if len(events) != 1 || json.Unmarshal([]byte(events[0].Body), &draftEvent) != nil || draftEvent.Data.Object.Status != "draft" || draftEvent.Data.Object.AmountPaid != 0 {
		t.Fatalf("held renewal unexpectedly finalized/collected: %+v", events)
	}
	draftID := draftEvent.Data.Object.ID
	type paymentCase struct{ ID, Outcome string }
	cases := []paymentCase{{ID: draftID, Outcome: "processing"}}
	for _, outcome := range []string{"declined", "requires_action"} {
		stackProviderControl(t, ctx, "renew", map[string]string{"subscription_id": subscription}, &events)
		if len(events) != 1 || json.Unmarshal([]byte(events[0].Body), &draftEvent) != nil || draftEvent.Data.Object.Status != "draft" {
			t.Fatalf("%s renewal did not remain held", outcome)
		}
		cases = append(cases, paymentCase{ID: draftEvent.Data.Object.ID, Outcome: outcome})
	}
	var manual, adjustment, operatorPaused json.RawMessage
	stackProviderControl(t, ctx, "unrelated_invoice", map[string]string{"subscription_id": subscription}, &manual)
	stackProviderControl(t, ctx, "held_adjustment", map[string]string{"subscription_id": subscription}, &adjustment)
	stackProviderControl(t, ctx, "operator_paused_invoice", map[string]string{"subscription_id": subscription}, &operatorPaused)
	idOf := func(raw json.RawMessage) string {
		var invoice struct{ ID string }
		if err := json.Unmarshal(raw, &invoice); err != nil {
			t.Fatal(err)
		}
		return invoice.ID
	}
	manualID, adjustmentID, operatorID := idOf(manual), idOf(adjustment), idOf(operatorPaused)
	financialSnapshot := func(raw json.RawMessage) []byte {
		snapshot, err := stackInvoiceFinancialSnapshot(raw)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	adjustmentMoney, operatorMoney := financialSnapshot(adjustment), financialSnapshot(operatorPaused)
	heldSubscription, _ := read()
	if heldSubscription.Customer == "" {
		t.Fatal("held subscription lost its customer")
	}
	for _, item := range cases {
		stackProviderControl(t, ctx, "payment_behavior", map[string]string{"invoice_id": item.ID, "outcome": item.Outcome}, &configured)
	}
	var premature int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_billing_documents WHERE tenant_id=$1 AND provider_document_id=$2`, tenant, draftID).Scan(&premature); err != nil || premature != 0 {
		t.Fatalf("uncollected draft became an issued tax document: %d %v", premature, err)
	}
	typeName, vat = "consumer", ""
	_, err = purserpb.NewSubscriptionServiceClient(conn).UpdateBillingDetails(ctx, &purserpb.UpdateBillingDetailsRequest{TenantId: tenant, CustomerType: &typeName, VatNumber: &vat, Address: &purserpb.BillingAddress{Street: "1 Main Street", City: "Amsterdam", PostalCode: "1000AA", Country: "NL"}})
	if err != nil {
		t.Fatal(err)
	}
	wait("restore future collection and finalize the original held renewal with correct VAT", func(sub subscriptionSnapshot, objects map[string]json.RawMessage) bool {
		for _, item := range cases {
			var invoice struct {
				Status      string
				Net         int64 `json:"total_excluding_tax"`
				Total       int64
				Paid        int64 `json:"amount_paid"`
				AutoAdvance bool  `json:"auto_advance"`
			}
			if err := json.Unmarshal(objects[item.ID], &invoice); err != nil {
				t.Fatal(err)
			}
			if invoice.Status != "open" || !invoice.AutoAdvance || invoice.Net <= 0 || invoice.Total != invoice.Net+(invoice.Net*2100+5000)/10000 || invoice.Paid != 0 {
				return false
			}
		}
		var reviews int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND kind='provider_invoice_tax_review' AND network='stripe' AND reference_type='stripe_invoice' AND reference_id=$2 AND status='open' AND COALESCE(evidence_json->>'code','')<>'' AND length(COALESCE(evidence_json->>'input_fingerprint',''))=64`, tenant, adjustmentID).Scan(&reviews); err != nil {
			t.Fatal(err)
		}
		return sub.Pause == nil && sub.Metadata["frameworks_tax_review"] != "true" && sub.Metadata["frameworks_tax_recovery_pending"] == "" && reviews == 1
	})
	var requests []struct{ Method, Path string }
	stackProviderControl(t, ctx, "requests", nil, &requests)
	for _, request := range requests {
		for _, item := range cases {
			if request.Method == http.MethodPost && request.Path == "/v1/invoices/"+item.ID+"/pay" {
				t.Fatal("tax recovery forced synchronous payment instead of restoring automatic collection")
			}
		}
	}
	_, recovered := read()
	if !bytes.Equal(manual, recovered[manualID]) {
		t.Fatal("worker changed the unrelated overdue manual invoice")
	}
	if !bytes.Equal(adjustmentMoney, financialSnapshot(recovered[adjustmentID])) || !bytes.Equal(operatorMoney, financialSnapshot(recovered[operatorID])) {
		t.Fatal("worker changed unsupported draft money or restarted operator-paused collection")
	}
	if !bytes.Equal(pastDueMoney, financialSnapshot(recovered[pastDueID])) {
		t.Fatal("tax recovery rewrote an invoice that was already finalized and past_due before the hold")
	}
	var retainedReview struct{ Metadata map[string]string }
	if err = json.Unmarshal(recovered[adjustmentID], &retainedReview); err != nil {
		t.Fatal(err)
	}
	var reviewCode, reviewInput string
	if err = db.QueryRowContext(ctx, `SELECT evidence_json->>'code',evidence_json->>'input_fingerprint' FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND kind='provider_invoice_tax_review' AND network='stripe' AND reference_type='stripe_invoice' AND reference_id=$2`, tenant, adjustmentID).Scan(&reviewCode, &reviewInput); err != nil {
		t.Fatal(err)
	}
	if retainedReview.Metadata["frameworks_tax_terminal_review"] != reviewCode || retainedReview.Metadata["frameworks_tax_terminal_input"] != reviewInput {
		t.Fatal("provider acknowledgement does not match the durable invoice review")
	}
	var inventedReviews int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND kind='provider_invoice_tax_review' AND network='stripe' AND reference_type='stripe_invoice' AND reference_id IN ($2,$3,$4)`, tenant, manualID, operatorID, pastDueID).Scan(&inventedReviews); err != nil || inventedReviews != 0 {
		t.Fatalf("manual collection choices became fabricated tax exceptions: %d %v", inventedReviews, err)
	}
	t.Log("deployed worker recovered a subscription already past_due before the tax hold, retained non-renewal review and preserved finalized debt and operator collection choices")
	for _, item := range cases {
		stackProviderControl(t, ctx, "invoice_events", map[string]string{"invoice_id": item.ID}, &events)
		deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
		var retained int
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_billing_documents WHERE tenant_id=$1 AND provider_document_id=$2 AND vat_minor=(net_minor*2100+5000)/10000 AND gross_minor=net_minor+vat_minor AND original_pdf IS NOT NULL`, tenant, item.ID).Scan(&retained); err != nil || retained != 1 {
			t.Fatalf("recovered held renewal document: %d %v", retained, err)
		}
		stackProviderControl(t, ctx, "collect", map[string]string{"invoice_id": item.ID}, &events)
		if len(events) > 0 {
			deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
		}
		pendingSubscription, objects := read()
		if pendingSubscription.Pause != nil || pendingSubscription.Metadata["frameworks_tax_review"] == "true" {
			t.Fatal("payment failure or authentication paused future tax-proven collection")
		}
		var pending struct {
			Status      string
			Paid        int64  `json:"amount_paid"`
			Intent      string `json:"payment_intent"`
			AutoAdvance bool   `json:"auto_advance"`
			NextAttempt int64  `json:"next_payment_attempt"`
		}
		if err = json.Unmarshal(objects[item.ID], &pending); err != nil {
			t.Fatal(err)
		}
		var intent struct{ Status string }
		if err = json.Unmarshal(objects[pending.Intent], &intent); err != nil {
			t.Fatal(err)
		}
		expectedIntent := item.Outcome
		if item.Outcome == "declined" {
			expectedIntent = "requires_payment_method"
		}
		if pending.Status != "open" || pending.Paid != 0 || !pending.AutoAdvance || intent.Status != expectedIntent {
			t.Fatalf("pending automatic collection invented paid funds: %+v intent=%+v", pending, intent)
		}
		expectedAttempts := int64(1)
		if item.Outcome == "declined" {
			if pending.NextAttempt <= time.Now().Unix() {
				t.Fatal("declined automatic collection lost its scheduled retry")
			}
			stackProviderControl(t, ctx, "payment_behavior", map[string]string{"invoice_id": item.ID, "outcome": "succeeded"}, &configured)
			stackProviderControl(t, ctx, "collect", map[string]any{"invoice_id": item.ID, "at": pending.NextAttempt}, &events)
			expectedAttempts = 2
		} else {
			stackProviderControl(t, ctx, "settle", map[string]string{"invoice_id": item.ID}, &events)
		}
		deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
		_, settledObjects := read()
		var paid struct {
			Status   string
			Paid     int64 `json:"amount_paid"`
			Total    int64
			Attempts int64 `json:"attempt_count"`
		}
		if err = json.Unmarshal(settledObjects[item.ID], &paid); err != nil {
			t.Fatal(err)
		}
		if paid.Status != "paid" || paid.Paid != paid.Total || paid.Attempts != expectedAttempts {
			t.Fatalf("later provider settlement did not retain one collection: %+v", paid)
		}
		t.Logf("deployed worker restored %s invoice %s with 21%% VAT, no synchronous Pay and correct eventual provider collection", item.Outcome, item.ID)
	}
}
