//go:build schema_verify

package handlers

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func TestStripeLegacyWhitespaceProfileRetainsOriginalIdentity_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	tenant, raw := stripeReassessmentInvoice(t, db, "in_legacy_whitespace")
	var facts struct {
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatal(err)
	}
	q := purserdb.New(db)
	stored, err := q.GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: facts.Metadata["frameworks_tax_profile"]})
	if err != nil {
		t.Fatal(err)
	}
	var legacy billingstripe.TaxProfile
	if err = json.Unmarshal(stored, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Name = " " + legacy.Name + " "
	legacy.Company = " " + legacy.Company + " "
	legacy.Email = " " + legacy.Email + " "
	legacy.Street = " " + legacy.Street + " "
	legacy.City = " " + legacy.City + " "
	legacy.PostalCode = " " + legacy.PostalCode + " "
	legacy.Country = " " + strings.ToLower(legacy.Country) + " "
	legacy.VATNumber = " " + legacy.VATNumber + " "
	legacy.ProfileSHA = legacy.Fingerprint()
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: legacy.ProfileSHA, Profile: encoded}); err != nil {
		t.Fatal(err)
	}
	capturedProfile, err := q.GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: legacy.ProfileSHA})
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]any
	if err = json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	original["metadata"] = map[string]string{"frameworks_tax_profile": legacy.ProfileSHA}
	raw, err = json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
		t.Fatal(err)
	}
	document, err := q.FindProviderBillingDocument(t.Context(), purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "in_legacy_whitespace", DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: document.ID, DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatal(err)
	}
	if current.TaxStatus != "ready" || current.GrossMinor != 12100 || current.NetMinor.Int64 != 10000 || current.VatMinor.Int64 != 2100 {
		t.Fatalf("cosmetic legacy profile caused false permanent Stripe review: status%s net%d VAT%d gross%d", current.TaxStatus, current.NetMinor.Int64, current.VatMinor.Int64, current.GrossMinor)
	}
	after, err := q.GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: legacy.ProfileSHA})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(capturedProfile, after) {
		t.Fatal("canonical comparison rewrote accepted legacy profile")
	}
	var retainedSHA string
	if err = db.QueryRow(`SELECT raw_facts->'metadata'->>'frameworks_tax_profile' FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, document.ID).Scan(&retainedSHA); err != nil {
		t.Fatal(err)
	}
	if retainedSHA != legacy.ProfileSHA {
		t.Fatal("canonical comparison rewrote original provider metadata identity")
	}
}
