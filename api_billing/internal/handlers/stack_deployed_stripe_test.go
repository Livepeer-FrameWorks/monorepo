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
	t.Helper()
	tenant := os.Getenv("STACK_BILLING_TENANT_ID")
	if tenant == "" || os.Getenv("STACK_BILLING_DEPLOYED") != "1" {
		t.Skip("run through stack scenario 54")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	deliverStackProviderEvents(t, ctx, conn, db, tenant, events)
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
