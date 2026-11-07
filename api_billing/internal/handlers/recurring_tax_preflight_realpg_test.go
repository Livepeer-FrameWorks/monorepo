//go:build schema_verify

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingtax "frameworks/api_billing/internal/tax"
)

func TestRecurringTaxPreflightsIncompleteProfilesAndInvalidClaims_RealPG(t *testing.T) {
	db, _, _ := newStripeReassessmentService(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><checkVatResponse xmlns="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><countryCode>AT</countryCode><vatNumber>U12345678</vatNumber><requestDate>` + time.Now().UTC().Format(time.DateOnly) + `+01:00</requestDate><valid>false</valid></checkVatResponse></soap:Body></soap:Envelope>`))
	}))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
	usBusinessPolicy := func(from, until time.Time) string {
		return fmt.Sprintf(`{"reference":"reviewed-US-business-policy","supplier_country":"NL","valid_from":%q,"valid_until":%q,"non_eu_businesses":{"US":{"rate_bps":0,"source":"Reviewed US business policy","tax_label":"VAT","wording":"Outside scope of Dutch VAT","evidence_reference":"US-business-review"}}}`, from.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
	}
	activeUSPolicy := usBusinessPolicy(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name, country, kind, vat, policy, mode string
		wantReason, wantReview                 string
		incomplete                             bool
		businessStoreUnavailable               bool
	}{
		{name: "incomplete_foreign_consumer", country: "US", kind: "consumer", policy: fmt.Sprintf(`{"reference":"reviewed-empty-policy","supplier_country":"NL","valid_from":%q,"valid_until":%q,"non_eu_consumers":{"CA":{"rate_bps":0,"source":"Reviewed CA policy","tax_label":"VAT","wording":"Outside scope of Dutch VAT","evidence_reference":"CA-review"}}}`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)), incomplete: true},
		{name: "incomplete_destination_consumer", country: "DE", kind: "consumer", mode: "destination", incomplete: true},
		{name: "complete_invalid_vies", country: "AT", kind: "business", vat: "ATU12345678"},
		{name: "complete_missing_vat", country: "AT", kind: "business"},
		{name: "complete_bad_vat_format", country: "AT", kind: "business", vat: "bad-claim"},
		{name: "complete_consumer_vat_claim", country: "NL", kind: "consumer", vat: "NL853319145B01"},
		{name: "complete_domestic_bad_canonical_vat", country: "NL", kind: "business", vat: "bad-claim"},
		{name: "complete_US_business_without_establishment_proof", country: "US", kind: "business", vat: "unverified-US-claim", policy: activeUSPolicy, wantReason: "business_evidence_required"},
		{name: "business_evidence_store_unavailable_retries", country: "US", kind: "business", policy: activeUSPolicy, businessStoreUnavailable: true, wantReview: "business_evidence_unavailable"},
		{name: "expired_foreign_policy_still_requires_review", country: "US", kind: "business", policy: usBusinessPolicy(time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)), wantReview: "foreign_tax_policy_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			appconfigtest.Set(t, "CRYPTO_TAX_POLICY_JSON", tc.policy)
			appconfigtest.Set(t, "EU_CONSUMER_VAT_MODE", tc.mode)
			tenant := seedPrepaidLedgerTenant(t, db)
			name := "Original Buyer"
			if tc.incomplete {
				name = ""
			}
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name=$2,billing_email='buyer@example.test',billing_customer_type=$3,tax_id=NULLIF($4,''),billing_address=jsonb_build_object('street','Main1','city','City','postal_code','1000','country',$5::text) WHERE tenant_id=$1::uuid`, tenant, name, tc.kind, tc.vat, tc.country); err != nil {
				t.Fatal(err)
			}
			if tc.businessStoreUnavailable {
				if _, err := db.Exec(`ALTER TABLE purser.business_establishment_reviews RENAME TO unavailable_business_establishment_reviews`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := db.Exec(`ALTER TABLE purser.unavailable_business_establishment_reviews RENAME TO business_establishment_reviews`); err != nil {
						t.Error(err)
					}
				})
			}
			profile, err := AssessRecurringProviderTaxProfile(t.Context(), db, tenant, "")
			if tc.wantReview != "" {
				var review *billingtax.ReviewError
				if profile != nil || !errors.As(err, &review) || review.Code != tc.wantReview || billingtax.IsTransient(err) != tc.businessStoreUnavailable {
					t.Fatalf("non-claim failure became a domestic fallback: profile %+v error %v", profile, err)
				}
				return
			}
			if err != nil || profile == nil || !profile.IncompleteProfile || profile.RateBPS != 2100 || profile.ReverseCharge || profile.VATNumber != "" {
				t.Fatalf("recurring claim blocked or granted zero VAT instead of domestic fallback: %+v %v", profile, err)
			}
			var proof CryptoTaxAssessment
			if err = json.Unmarshal(profile.Evidence, &proof); err != nil || !proof.ReviewRequired || !strings.Contains(string(proof.ClaimedProfile), tc.country) {
				t.Fatalf("fallback lost original claim/review: %s %v", profile.Evidence, err)
			}
			if tc.wantReason != "" && (!slices.Contains(proof.ReviewReasons, tc.wantReason) || !strings.Contains(string(proof.ClaimedProfile), tc.vat) || proof.Decision.Source == "" || proof.Decision.CheckedOn == "" || proof.Decision.EffectiveFrom == "") {
				t.Fatalf("fallback lost the exact claim, reason or effective-rate evidence: %s", profile.Evidence)
			}
		})
	}
}

func TestRecurringTaxFallbackFreezesEffectiveDomesticRate_RealPG(t *testing.T) {
	db, _, _ := newStripeReassessmentService(t)
	if _, err := db.Exec(`UPDATE purser.vat_rate_periods SET rate_bps=2300,source='Reviewed NL test rate',source_checked_on='2026-11-01' WHERE country_code='NL'`); err != nil {
		t.Fatal(err)
	}
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='',billing_email='buyer@example.test',billing_customer_type='consumer',billing_address='{"country":"US"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	profile, err := AssessRecurringProviderTaxProfile(t.Context(), db, tenant, "")
	if err != nil {
		t.Fatal(err)
	}
	var proof CryptoTaxAssessment
	if err = json.Unmarshal(profile.Evidence, &proof); err != nil {
		t.Fatal(err)
	}
	if profile.RateBPS != 2300 || proof.Decision.RateBPS != 2300 || proof.Decision.Source != "Reviewed NL test rate" || proof.Decision.CheckedOn != "2026-11-01" || proof.Decision.EffectiveFrom == "" {
		t.Fatalf("fallback hardcoded tax or lost effective-rate evidence: rate %d decision %+v", profile.RateBPS, proof.Decision)
	}
}

func TestRecurringTaxVIESOutageDoesNotBecomeDomesticFallback_RealPG(t *testing.T) {
	db, _, _ := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Buyer',billing_email='buyer@example.test',billing_customer_type='business',tax_id='ATU12345678',billing_address='{"street":"Main1","city":"Vienna","postal_code":"1000","country":"AT"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
	profile, err := AssessRecurringProviderTaxProfile(t.Context(), db, tenant, "")
	if profile != nil || !billingtax.IsTransient(err) {
		t.Fatalf("transient VIES failure became a different charge: %+v %v", profile, err)
	}
}
