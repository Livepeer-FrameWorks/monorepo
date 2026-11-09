//go:build schema_verify

package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	for _, tc := range []struct {
		name, country, kind, vat string
		incomplete               bool
	}{
		{name: "incomplete_foreign_consumer", country: "US", kind: "consumer", incomplete: true},
		{name: "incomplete_destination_consumer", country: "DE", kind: "consumer", incomplete: true},
		{name: "complete_invalid_vies", country: "AT", kind: "business", vat: "ATU12345678"},
		{name: "complete_missing_vat", country: "AT", kind: "business"},
		{name: "complete_bad_vat_format", country: "AT", kind: "business", vat: "bad-claim"},
		{name: "complete_consumer_vat_claim", country: "NL", kind: "consumer", vat: "NL853319145B01"},
		{name: "complete_domestic_bad_canonical_vat", country: "NL", kind: "business", vat: "bad-claim"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenant := seedPrepaidLedgerTenant(t, db)
			name := "Original Buyer"
			if tc.incomplete {
				name = ""
			}
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name=$2,billing_email='buyer@example.test',billing_customer_type=$3,tax_id=NULLIF($4,''),billing_address=jsonb_build_object('street','Main1','city','City','postal_code','1000','country',$5::text) WHERE tenant_id=$1::uuid`, tenant, name, tc.kind, tc.vat, tc.country); err != nil {
				t.Fatal(err)
			}
			profile, err := AssessRecurringProviderTaxProfile(t.Context(), db, tenant, "")
			if err != nil || profile == nil || !profile.IncompleteProfile || profile.RateBPS != 2100 || profile.ReverseCharge || profile.VATNumber != "" {
				t.Fatalf("recurring claim blocked or granted zero VAT instead of domestic fallback: %+v %v", profile, err)
			}
			var proof CryptoTaxAssessment
			if err = json.Unmarshal(profile.Evidence, &proof); err != nil || !proof.ReviewRequired || !strings.Contains(string(proof.ClaimedProfile), tc.country) {
				t.Fatalf("fallback lost original claim/review: %s %v", profile.Evidence, err)
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
	hash := sha256.Sum256([]byte("ATU12345678"))
	if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,checked_at,expires_at,raw_response) VALUES($1::uuid,'AT',$2,'ATU****5678',true,CURRENT_DATE,NOW(),NOW()+INTERVAL '24 hours','{}')`, tenant, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	accepted, err := AssessRecurringProviderTaxProfile(t.Context(), db, tenant, "")
	if err != nil || !accepted.ReverseCharge {
		t.Fatalf("fixture never established reverse charge: %+v %v", accepted, err)
	}
	if err = retainProviderTaxExpectation(t.Context(), db, tenant, accepted); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE purser.vat_validation_evidence SET checked_at=NOW()-INTERVAL '10 days',expires_at=NOW()-INTERVAL '9 days' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
	profile, err := AssessRecurringProviderTaxProfile(t.Context(), db, tenant, "")
	if profile != nil || !billingtax.IsTransient(err) {
		t.Fatalf("transient VIES failure became a different charge: %+v %v", profile, err)
	}
	if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET tax_id='ATU87654321' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	changed, err := AssessRecurringProviderTaxProfile(t.Context(), db, tenant, "")
	if err != nil || changed == nil || changed.RateBPS != 2100 || changed.ReverseCharge || !changed.IncompleteProfile {
		t.Fatalf("new unproven claim did not receive visible domestic fallback: %+v %v", changed, err)
	}

}
