//go:build schema_verify

package handlers

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingmollie "frameworks/api_billing/internal/mollie"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

func TestNativeMollieFallbackPreservesHistoricalReviewReasons_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name=NULL,billing_email='buyer@example.test',billing_customer_type='business',billing_address='{"country":"NL"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'review-customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	point := time.Now().UTC().Truncate(time.Microsecond)
	accepted, err := AssessProviderTaxProfileAt(t.Context(), db, tenant, "", billingtax.Recurring, point.Add(-2*time.Hour))
	if err != nil || !accepted.IncompleteProfile {
		t.Fatalf("retain original incomplete profile: %+v %v", accepted, err)
	}
	if err = RetainProviderTaxExpectation(t.Context(), db, tenant, accepted); err != nil {
		t.Fatal(err)
	}
	var originalProof TaxAssessment
	if err = json.Unmarshal(accepted.Evidence, &originalProof); err != nil || len(originalProof.ReviewReasons) == 0 {
		t.Fatalf("original fallback lacks review reasons: %+v %v", originalProof, err)
	}
	if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Completed Buyer',billing_address='{"street":"Main1","city":"Toronto","postal_code":"M5V1A1","country":"CA"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	expected, err := AssessProviderTaxProfileAt(t.Context(), db, tenant, "", billingtax.Checkout, point.Add(-time.Hour))
	if err != nil || expected.RateBPS != 0 {
		t.Fatalf("retain later accepted terms: %+v %v", expected, err)
	}
	if err = RetainProviderTaxExpectation(t.Context(), db, tenant, expected); err != nil {
		t.Fatal(err)
	}
	terms, err := billingmollie.NewTaxTerms(accepted.ProfileSHA, 10000, accepted.RateBPS)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"tenant_id": tenant}
	terms.Metadata(metadata)
	payment := &mollieapi.Payment{ID: "fallback-old-provider-rate", CustomerID: "review-customer", SubscriptionID: "subscription", Metadata: metadata, Amount: &mollieapi.Amount{Currency: "EUR", Value: "121.00"}, PaidAt: &point, Status: "paid"}
	service := &Service{db: db, logger: logging.NewLogger()}
	if err = service.issueMollieNativeTaxInvoice(t.Context(), tenant, payment); err != nil {
		t.Fatal(err)
	}
	var frozen, anomaly []byte
	var net, vat, gross int64
	if err = db.QueryRow(`SELECT tax_decision,net_eur_cents,vat_eur_cents,gross_eur_cents FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND source_reference_id=$2`, tenant, payment.ID).Scan(&frozen, &net, &vat, &gross); err != nil {
		t.Fatal(err)
	}
	var retained struct {
		ReviewReasons []string `json:"review_reasons"`
	}
	if err = json.Unmarshal(frozen, &retained); err != nil {
		t.Fatal(err)
	}
	for _, reason := range append(originalProof.ReviewReasons, "provider_tax_sync_stale_at_tax_point") {
		if !slices.Contains(retained.ReviewReasons, reason) {
			t.Fatalf("native fallback discarded historical review reason %q: %s", reason, frozen)
		}
	}
	if net != 10000 || vat != 2100 || gross != 12100 {
		t.Fatalf("historical review repriced collected money: net%d vat%d gross%d", net, vat, gross)
	}
	if err = db.QueryRow(`SELECT evidence_json FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review'`, tenant).Scan(&anomaly); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(anomaly, []byte("provider_tax_sync_stale_at_tax_point")) {
		t.Fatalf("actionable review omitted historical mismatch: %s", anomaly)
	}
	if _, err = db.Exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolution_note='Original facts reviewed' WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review'`, tenant); err != nil {
		t.Fatal(err)
	}
	if err = service.issueMollieNativeTaxInvoice(t.Context(), tenant, payment); err != nil {
		t.Fatal(err)
	}
	var replay []byte
	var open int
	if err = db.QueryRow(`SELECT tax_decision,(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND status='open') FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND source_reference_id=$2`, tenant, payment.ID).Scan(&replay, &open); err != nil || !bytes.Equal(frozen, replay) || open != 0 {
		t.Fatalf("duplicate delivery changed original facts or reopened review: open%d err%v", open, err)
	}
}

func TestNativeMollieInvalidAcceptedProfileHasReviewReason_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Complete Buyer',billing_email='buyer@example.test',billing_customer_type='consumer',billing_address='{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'invalid-profile-customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	point := time.Now().UTC().Truncate(time.Microsecond)
	profile, err := AssessProviderTaxProfileAt(t.Context(), db, tenant, "", billingtax.Checkout, point.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// A retained legacy profile can contain a malformed optional VAT claim even
	// when its positive domestic rate and complete legal parties are provable.
	profile.VATNumber = "DE123456789"
	if _, normalizeErr := normalizeProviderTaxProfile(*profile); normalizeErr == nil {
		t.Fatal("legacy profile fixture must fail billing-country VAT normalization")
	}
	profile, err = retainProviderTaxProfile(t.Context(), db, tenant, profile)
	if err != nil {
		t.Fatal(err)
	}
	terms, err := billingmollie.NewTaxTerms(profile.ProfileSHA, 10000, profile.RateBPS)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"tenant_id": tenant}
	terms.Metadata(metadata)
	payment := &mollieapi.Payment{ID: "invalid-accepted-profile", CustomerID: "invalid-profile-customer", SubscriptionID: "subscription", Metadata: metadata, Amount: &mollieapi.Amount{Currency: "EUR", Value: "121.00"}, PaidAt: &point, Status: "paid"}
	if err = (&Service{db: db, logger: logging.NewLogger()}).issueMollieNativeTaxInvoice(t.Context(), tenant, payment); err != nil {
		t.Fatal(err)
	}
	var frozen, anomaly []byte
	if err = db.QueryRow(`SELECT tax_decision FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND source_reference_id=$2`, tenant, payment.ID).Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	var assessment struct {
		ReviewRequired bool     `json:"review_required"`
		ReviewReasons  []string `json:"review_reasons"`
	}
	if err = json.Unmarshal(frozen, &assessment); err != nil {
		t.Fatal(err)
	}
	if !assessment.ReviewRequired || !slices.Contains(assessment.ReviewReasons, "accepted_profile_invalid") {
		t.Fatalf("normalization failure retained a reasonless review: %s", frozen)
	}
	if err = db.QueryRow(`SELECT evidence_json FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review'`, tenant).Scan(&anomaly); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(anomaly, []byte("accepted_profile_invalid")) {
		t.Fatalf("operator review omitted invalid accepted profile reason: %s", anomaly)
	}
}
