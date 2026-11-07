//go:build stack_verify

package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

type stackRecurringFallback struct {
	tenant, subscription, customer, knownName string
}

type stackFallbackSubscription struct {
	Customer string            `json:"customer"`
	Pause    json.RawMessage   `json:"pause_collection"`
	Metadata map[string]string `json:"metadata"`
	Rates    []struct {
		Percentage float64 `json:"percentage"`
		Inclusive  bool    `json:"inclusive"`
	} `json:"default_tax_rates"`
}

func stackFallbackObjects(t *testing.T, ctx context.Context, fixture stackRecurringFallback) (stackFallbackSubscription, map[string]json.RawMessage) {
	t.Helper()
	var objects map[string]json.RawMessage
	stackProviderControl(t, ctx, "objects", nil, &objects)
	var subscription stackFallbackSubscription
	if err := json.Unmarshal(objects[fixture.subscription], &subscription); err != nil {
		t.Fatal(err)
	}
	return subscription, objects
}

func prepareStackRecurringFallback(t *testing.T, ctx context.Context, conn *grpc.ClientConn, db *sql.DB) stackRecurringFallback {
	t.Helper()
	fixture := stackRecurringFallback{tenant: os.Getenv("STACK_BILLING_FALLBACK_TENANT_ID")}
	if _, err := uuid.Parse(fixture.tenant); err != nil {
		t.Fatal("scenario54 must supply a separate fallback fixture tenant")
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
	if err = db.QueryRowContext(ctx, `SELECT stripe_subscription_id,stripe_customer_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, fixture.tenant).Scan(&fixture.subscription, &fixture.customer); err != nil {
		t.Fatal(err)
	}
	subscription, objects := stackFallbackObjects(t, ctx, fixture)
	var customer struct{ Name string }
	if err = json.Unmarshal(objects[fixture.customer], &customer); err != nil {
		t.Fatal(err)
	}
	fixture.knownName = customer.Name
	var initialProof bool
	if err = db.QueryRowContext(ctx, `SELECT (profile->>'rate_bps')::int=0 AND (profile->>'reverse_charge')::boolean AND NOT COALESCE((profile->>'incomplete_profile')::boolean,false) FROM purser.provider_tax_profiles WHERE tenant_id=$1 AND profile_sha256=$2`, fixture.tenant, subscription.Metadata["frameworks_tax_profile"]).Scan(&initialProof); err != nil {
		t.Fatal(err)
	}
	if !initialProof || fixture.knownName != "Stack Buyer" || len(subscription.Rates) != 1 || subscription.Rates[0].Percentage != 0 || subscription.Rates[0].Inclusive {
		t.Fatalf("fallback fixture did not start from complete VIES-validated reverse charge: %+v knownName=%q initialProof=%v", subscription, fixture.knownName, initialProof)
	}
	// This disposable customer's saved profile becomes incomplete after a real
	// valid checkout. Its invalid VAT claim must not bypass the domestic fallback.
	changed, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_name='   ',tax_id='invalid VAT claim' WHERE tenant_id=$1 AND stripe_subscription_id=$2 AND billing_name='Stack Buyer'`, fixture.tenant, fixture.subscription)
	if err != nil {
		t.Fatal(err)
	}
	if rows, rowsErr := changed.RowsAffected(); rowsErr != nil || rows != 1 {
		t.Fatalf("fallback fixture did not update exactly its own profile: rows=%d err=%v", rows, rowsErr)
	}
	return fixture
}

func verifyStackRecurringFallback(t *testing.T, ctx context.Context, conn *grpc.ClientConn, db *sql.DB, fixture stackRecurringFallback) {
	t.Helper()
	// The main held-renewal fixture has just observed the first real worker
	// cycle. This tenant shares that cycle, allowing only its remaining scan time.
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		subscription, objects := stackFallbackObjects(t, ctx, fixture)
		var customer struct {
			Name      string `json:"name"`
			TaxExempt string `json:"tax_exempt"`
		}
		if err := json.Unmarshal(objects[fixture.customer], &customer); err != nil {
			t.Fatal(err)
		}
		var profile struct {
			Incomplete bool `json:"incomplete_profile"`
			Rate       int  `json:"rate_bps"`
			Reverse    bool `json:"reverse_charge"`
			Evidence   struct {
				Review  bool     `json:"review_required"`
				Reasons []string `json:"review_reasons"`
				Claimed struct {
					VAT string `json:"vat_number"`
				} `json:"claimed_profile"`
			} `json:"evidence"`
		}
		var raw []byte
		if err := db.QueryRowContext(ctx, `SELECT profile FROM purser.provider_tax_profiles WHERE tenant_id=$1 AND profile_sha256=$2`, fixture.tenant, subscription.Metadata["frameworks_tax_profile"]).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &profile); err != nil {
			t.Fatal(err)
		}
		noPause := len(subscription.Pause) == 0 || bytes.Equal(subscription.Pause, []byte("null"))
		if profile.Incomplete && profile.Rate == 2100 && !profile.Reverse && profile.Evidence.Review && slices.Contains(profile.Evidence.Reasons, "billing_profile_incomplete_domestic_vat") && profile.Evidence.Claimed.VAT == "invalid VAT claim" && len(subscription.Rates) == 1 && subscription.Rates[0].Percentage == 21 && !subscription.Rates[0].Inclusive && noPause && subscription.Metadata["frameworks_tax_review"] != "true" && customer.TaxExempt == "none" && customer.Name == fixture.knownName {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("shared worker cycle did not apply explicit 21%% fallback without pausing or losing known provider identity: sub=%+v customer=%+v profile=%s", subscription, customer, raw)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var firstInvoice, firstDocument, firstReview, firstSnapshot, firstEvidence, secondDocument string
	var resolvedAt time.Time
	var firstOccurrences int
	for renewal := range 2 {
		var created []stackProviderEvent
		stackProviderControl(t, ctx, "renew", map[string]string{"subscription_id": fixture.subscription}, &created)
		if len(created) != 1 {
			t.Fatalf("fallback renewal produced %d created events", len(created))
		}
		deliverStackProviderEvents(t, ctx, conn, db, fixture.tenant, created)
		var invoice struct {
			Data struct{ Object struct{ ID string } } `json:"data"`
		}
		if err := json.Unmarshal([]byte(created[0].Body), &invoice); err != nil {
			t.Fatal(err)
		}
		var paidEvents []stackProviderEvent
		stackProviderControl(t, ctx, "collect", map[string]string{"invoice_id": invoice.Data.Object.ID}, &paidEvents)
		deliverStackProviderEvents(t, ctx, conn, db, fixture.tenant, paidEvents)
		_, objects := stackFallbackObjects(t, ctx, fixture)
		var paid struct {
			Status string
			Paid   int64 `json:"amount_paid"`
			Net    int64 `json:"total_excluding_tax"`
			Total  int64
		}
		if err := json.Unmarshal(objects[invoice.Data.Object.ID], &paid); err != nil {
			t.Fatal(err)
		}
		var document, taxStatus, pdfSHA, assessment, snapshot string
		var net, vat, gross int64
		var pdf []byte
		if err := db.QueryRowContext(ctx, `SELECT id::text,tax_status,net_minor,vat_minor,gross_minor,original_pdf,pdf_sha256,tax_assessment::text,jsonb_build_object('net',net_minor,'vat',vat_minor,'gross',gross_minor,'raw',raw_facts,'assessment',tax_assessment,'supplier',supplier_snapshot,'customer',customer_snapshot,'pdf_sha',pdf_sha256)::text FROM purser.provider_billing_documents WHERE tenant_id=$1 AND provider_document_id=$2 AND document_kind='provider_invoice'`, fixture.tenant, invoice.Data.Object.ID).Scan(&document, &taxStatus, &net, &vat, &gross, &pdf, &pdfSHA, &assessment, &snapshot); err != nil {
			t.Fatal(err)
		}
		var proof struct {
			Incomplete bool     `json:"incomplete_profile"`
			Review     bool     `json:"review_required"`
			Reasons    []string `json:"review_reasons"`
		}
		if err := json.Unmarshal([]byte(assessment), &proof); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(pdf)
		if paid.Status != "paid" || paid.Paid != paid.Total || net <= 0 || net != paid.Net || vat != (net*2100+5000)/10000 || gross != net+vat || gross != paid.Total || taxStatus != "legacy_unknown" || !bytes.HasPrefix(pdf, []byte("%PDF-")) || pdfSHA != hex.EncodeToString(hash[:]) || !proof.Incomplete || !proof.Review || !slices.Contains(proof.Reasons, "incomplete_recurring_billing_profile") {
			t.Fatalf("fallback renewal lost VAT, payment, original PDF or review: paid=%+v net=%d vat=%d gross=%d tax=%s proof=%s", paid, net, vat, gross, taxStatus, assessment)
		}
		var providerCount, duplicatePurser int
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM purser.provider_billing_documents WHERE tenant_id=$1 AND provider_document_id=$2 AND document_kind='provider_invoice'),(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND source_reference_id=$2)`, fixture.tenant, invoice.Data.Object.ID).Scan(&providerCount, &duplicatePurser); err != nil || providerCount != 1 || duplicatePurser != 0 {
			t.Fatalf("fallback renewal document count provider=%d duplicatePurser=%d err=%v", providerCount, duplicatePurser, err)
		}
		var reviewID, reviewStatus, reviewEvidence string
		var occurrences int
		if err := db.QueryRowContext(ctx, `SELECT id::text,status,occurrences,evidence_json::text FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND kind='provider_tax_document_review' AND network='stripe' AND reference_type='provider_invoice' AND reference_id=$2`, fixture.tenant, document).Scan(&reviewID, &reviewStatus, &occurrences, &reviewEvidence); err != nil || reviewStatus != "open" || occurrences != 1 {
			t.Fatalf("fallback renewal has no independent open document review: status=%s occurrences=%d err=%v", reviewStatus, occurrences, err)
		}
		if renewal == 0 {
			firstInvoice, firstDocument, firstReview, firstSnapshot, firstEvidence, firstOccurrences = invoice.Data.Object.ID, document, reviewID, snapshot, reviewEvidence, occurrences
			// Fixture-scoped operator acknowledgement; replay must retain it even
			// while another renewal for this same incomplete profile needs review.
			if err := db.QueryRowContext(ctx, `UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolved_at=NOW(),resolution_note='stack operator reviewed first incomplete-profile invoice' WHERE tenant_id=$1 AND id=$2 AND reference_id=$3 AND status='open' RETURNING resolved_at`, fixture.tenant, reviewID, document).Scan(&resolvedAt); err != nil {
				t.Fatal(err)
			}
		} else if document == firstDocument || reviewID == firstReview {
			t.Fatal("separate renewals shared a document or operator review identity")
		} else {
			secondDocument = document
		}
	}
	// A fresh signed event identity re-enters the document importer, rather
	// than exercising only inbox deduplication of an already processed event.
	var replay []stackProviderEvent
	stackProviderControl(t, ctx, "invoice_events", map[string]string{"invoice_id": firstInvoice}, &replay)
	deliverStackProviderEvents(t, ctx, conn, db, fixture.tenant, replay)
	var status, evidence, snapshot string
	var occurrences int
	var retainedResolution time.Time
	if err := db.QueryRowContext(ctx, `SELECT status,occurrences,resolved_at,evidence_json::text FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND id=$2`, fixture.tenant, firstReview).Scan(&status, &occurrences, &retainedResolution, &evidence); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT jsonb_build_object('net',net_minor,'vat',vat_minor,'gross',gross_minor,'raw',raw_facts,'assessment',tax_assessment,'supplier',supplier_snapshot,'customer',customer_snapshot,'pdf_sha',pdf_sha256)::text FROM purser.provider_billing_documents WHERE tenant_id=$1 AND id=$2`, fixture.tenant, firstDocument).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" || occurrences != firstOccurrences || !retainedResolution.Equal(resolvedAt) || evidence != firstEvidence || snapshot != firstSnapshot {
		t.Fatalf("renewal/replay reopened first operator review or rewrote original facts: status=%s occurrences=%d", status, occurrences)
	}
	var openSecond int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND kind='provider_tax_document_review' AND network='stripe' AND reference_type='provider_invoice' AND reference_id=$2 AND status='open'`, fixture.tenant, secondDocument).Scan(&openSecond); err != nil || openSecond != 1 {
		t.Fatalf("first-document replay hid the later document review: open=%d err=%v", openSecond, err)
	}
	t.Log("deployed recurring fallback changed complete EU reverse charge to explicit 21% without losing known Stripe buyer or pausing; two collected renewals each retained one original/PDF and an independent review, first acknowledgement survived later renewal and fresh signed replay")
}
