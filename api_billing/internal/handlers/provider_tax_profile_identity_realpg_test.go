//go:build schema_verify

package handlers

import (
	"encoding/json"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/google/uuid"
)

func TestProviderTaxProfileRetainsOneIdentityAcrossEvidenceRefresh_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	tenant := uuid.NewString()
	q := purserdb.New(db)
	profile := billingstripe.TaxProfile{
		Name: "Buyer", Email: "buyer@example.test", Street: "Main 1", City: "Leiden", PostalCode: "2332ED", Country: "NL",
		RateBPS: 2100, SupplierCountry: "NL", SupplierIdentity: "DDVTech B.V.; VAT NL853319145B01",
	}
	store := func() {
		t.Helper()
		profile.ProfileSHA = profile.Fingerprint()
		raw, err := json.Marshal(profile)
		if err != nil {
			t.Fatal(err)
		}
		if err = q.StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: profile.ProfileSHA, Profile: raw}); err != nil {
			t.Fatal(err)
		}
	}
	profile.Evidence = json.RawMessage(`{"decision":{"checked_on":"2026-11-01","rate_bps":2100},"ip_country":"NL"}`)
	store()
	firstSHA := profile.ProfileSHA
	profile.Evidence = json.RawMessage(`{"decision":{"checked_on":"2026-11-02","rate_bps":2100},"ip_country":"DE","vat_validation":{"checked_at":"2026-11-02T12:00:00Z"}}`)
	store()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.provider_tax_profiles WHERE tenant_id=$1::uuid`, tenant).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || profile.ProfileSHA != firstSHA {
		t.Fatalf("evidence refresh created daily recurring profiles: rows=%d initial=%s refreshed=%s", count, firstSHA, profile.ProfileSHA)
	}
	retained, err := q.GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: firstSHA})
	if err != nil {
		t.Fatal(err)
	}
	var initial struct {
		Evidence struct {
			Decision struct {
				CheckedOn string `json:"checked_on"`
			} `json:"decision"`
		} `json:"evidence"`
	}
	if err = json.Unmarshal(retained, &initial); err != nil || initial.Evidence.Decision.CheckedOn != "2026-11-01" {
		t.Fatalf("refresh replaced the immutable original proof: %s (%v)", retained, err)
	}
	profile.Country, profile.VATNumber, profile.RateBPS, profile.ReverseCharge = "DE", "DE123456789", 0, true
	profile.Wording = "VAT reverse charged / btw verlegd"
	profile.Evidence = json.RawMessage(`{"decision":{"checked_on":"2026-11-02","rate_bps":0,"reverse_charge":true}}`)
	store()
	if profile.ProfileSHA == firstSHA {
		t.Fatal("changed charged terms reused the previous recurring profile")
	}
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.provider_tax_profiles WHERE tenant_id=$1::uuid`, tenant).Scan(&count); err != nil || count != 2 {
		t.Fatalf("changed charged terms did not retain a new profile: rows=%d err=%v", count, err)
	}
}
