//go:build stack_verify

package handlers_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

type stackStaleRate struct{ tenant, subscription string }

func prepareStackStaleRate(t *testing.T, ctx context.Context, conn *grpc.ClientConn, db *sql.DB) stackStaleRate {
	t.Helper()
	fixture := stackStaleRate{tenant: os.Getenv("STACK_BILLING_STALE_RATE_TENANT_ID")}
	if _, err := uuid.Parse(fixture.tenant); err != nil {
		t.Fatal("scenario54 must supply an isolated stale-rate tenant")
	}
	var tier string
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM purser.billing_tiers WHERE tier_name='supporter'`).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	checkout, err := purserpb.NewStripeServiceClient(conn).CreateCheckoutSession(ctx, &purserpb.CreateStripeCheckoutRequest{TenantId: fixture.tenant, TierId: tier, BillingPeriod: "monthly", SuccessUrl: "https://app.stack.test/billing/success", CancelUrl: "https://app.stack.test/billing/cancel"})
	if err != nil {
		t.Fatal(err)
	}
	var events []stackProviderEvent
	stackProviderControl(t, ctx, "complete", map[string]string{"session_id": checkout.SessionId}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, fixture.tenant, events)
	if err = db.QueryRowContext(ctx, `SELECT stripe_subscription_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, fixture.tenant).Scan(&fixture.subscription); err != nil {
		t.Fatal(err)
	}
	var configured map[string]bool
	stackProviderControl(t, ctx, "fault", map[string]any{"method": "POST", "path": "/v1/subscriptions/" + fixture.subscription, "status": 500, "after_execution": false, "execution_started": true, "remaining": 100}, &configured)
	kind, vat := "consumer", ""
	_, err = purserpb.NewSubscriptionServiceClient(conn).UpdateBillingDetails(ctx, &purserpb.UpdateBillingDetailsRequest{TenantId: fixture.tenant, CustomerType: &kind, VatNumber: &vat, Address: &purserpb.BillingAddress{Street: "1 Main Street", City: "Amsterdam", PostalCode: "1000AA", Country: "NL"}})
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func verifyStackStaleRate(t *testing.T, ctx context.Context, conn *grpc.ClientConn, db *sql.DB, fixture stackStaleRate) {
	t.Helper()
	deadline := time.NewTimer(7 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var accepted bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM purser.provider_tax_assessments WHERE tenant_id=$1 AND (assessment->>'rate_bps')::int=2100)`, fixture.tenant).Scan(&accepted); err != nil {
			t.Fatal(err)
		}
		if accepted {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("deployed worker did not retain intended21 terms before failed sync")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// Separate the accepted intent and whole-second Stripe finalization time.
	timer := time.NewTimer(1100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var objects map[string]json.RawMessage
	stackProviderControl(t, ctx, "objects", nil, &objects)
	var sub stackFallbackSubscription
	if err := json.Unmarshal(objects[fixture.subscription], &sub); err != nil {
		t.Fatal(err)
	}
	if len(sub.Rates) != 1 || sub.Rates[0].Percentage != 0 || len(sub.Pause) > 0 && !bytes.Equal(sub.Pause, []byte("null")) {
		t.Fatalf("failed synchronization changed old provider state: %+v", sub)
	}
	var events []stackProviderEvent
	stackProviderControl(t, ctx, "renew", map[string]string{"subscription_id": fixture.subscription}, &events)
	var event struct {
		Data struct {
			Object struct {
				ID    string
				Total int64
				Net   int64 `json:"total_excluding_tax"`
			}
		}
	}
	if len(events) != 1 {
		t.Fatalf("renewal events=%d", len(events))
	}
	if err := json.Unmarshal([]byte(events[0].Body), &event); err != nil {
		t.Fatal(err)
	}
	if event.Data.Object.Total <= 0 || event.Data.Object.Total != event.Data.Object.Net {
		t.Fatalf("stale provider renewal did not retain actual zero tax: %+v", event)
	}
	stackProviderControl(t, ctx, "collect", map[string]string{"invoice_id": event.Data.Object.ID}, &events)
	deliverStackProviderEvents(t, ctx, conn, db, fixture.tenant, events)
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_billing_documents WHERE tenant_id=$1 AND provider_document_id=$2 AND gross_minor=$3 AND tax_status='legacy_unknown' AND net_minor IS NULL AND vat_minor IS NULL AND tax_assessment#>'{evidence,expected_tax_assessment}' IS NOT NULL`, fixture.tenant, event.Data.Object.ID, event.Data.Object.Total).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stale0 renewal became verified or lost accepted21 proof/actualcash: count=%d %v", count, err)
	}
	var requests []struct {
		Path string `json:"path"`
	}
	stackProviderControl(t, ctx, "requests", nil, &requests)
	for _, request := range requests {
		if request.Path == "/v1/invoices/"+event.Data.Object.ID+"/pay" {
			t.Fatal("tax review synchronously recharged an already collected invoice")
		}
	}
	var configured map[string]bool
	stackProviderControl(t, ctx, "fault", map[string]any{"method": "POST", "path": "/v1/subscriptions/" + fixture.subscription, "remaining": 0}, &configured)
	retryDeadline := time.NewTimer(7 * time.Minute)
	defer retryDeadline.Stop()
	for {
		stackProviderControl(t, ctx, "objects", nil, &objects)
		if err := json.Unmarshal(objects[fixture.subscription], &sub); err != nil {
			t.Fatal(err)
		}
		if len(sub.Rates) == 1 && sub.Rates[0].Percentage == 21 && (len(sub.Pause) == 0 || bytes.Equal(sub.Pause, []byte("null"))) {
			break
		}
		select {
		case <-ticker.C:
		case <-retryDeadline.C:
			t.Fatal("normal deployed worker never retried the failed tax sync")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_billing_documents WHERE tenant_id=$1 AND provider_document_id=$2 AND gross_minor=$3 AND tax_status='legacy_unknown' AND net_minor IS NULL AND vat_minor IS NULL AND tax_assessment#>'{evidence,expected_tax_assessment}' IS NOT NULL`, fixture.tenant, event.Data.Object.ID, event.Data.Object.Total).Scan(&count); err != nil || count != 1 {
		t.Fatalf("successful future sync rewrote the historical review: count=%d %v", count, err)
	}
	payments := 0
	for _, raw := range objects {
		var p struct{ Object, Invoice, Status string }
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if p.Object == "invoice_payment" && p.Invoice == event.Data.Object.ID && p.Status == "paid" {
			payments++
		}
	}
	if payments != 1 {
		t.Fatalf("stale-rate invoice collected %d times", payments)
	}
	t.Log("deployed failed21sync left provider0 unchanged; actual0renewal kept original cash and expected21 review, normal worker retried21 without recharge")
}
