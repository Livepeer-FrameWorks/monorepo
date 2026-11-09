//go:build schema_verify

package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
	"testing"
	"time"
)

func TestProviderRenewalDoesNotVerifyStaleTaxAfterSyncFailure_RealPG(t *testing.T) {
	for _, customerUpdated := range []bool{false, true} {
		t.Run(fmt.Sprintf("customer_updated_%t", customerUpdated), func(t *testing.T) { providerRenewalStaleSyncFailure(t, customerUpdated) })
	}
}

func providerRenewalStaleSyncFailure(t *testing.T, customerUpdated bool) {
	db, service, fixture := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	point := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	hash := sha256.Sum256([]byte("DE123456789"))
	validation, _ := json.Marshal(map[string]any{"tenant_id": tenant, "country_code": "DE", "vat_number_hash": hex.EncodeToString(hash[:]), "valid": true, "checked_at": point.Add(-time.Hour), "expires_at": point.Add(time.Hour)})
	raw, _ := historicalStripeReverseCharge(t, db, tenant, "in_stale_zero_after_sync_failure", point, validation)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id='sub_expected_tax' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	fixture.route("GET", "/v1/subscriptions/sub_expected_tax", func(recordedProviderRequest) (int, any) {
		return 500, map[string]any{"error": map[string]string{"type": "api_error", "message": "Provider unavailable"}}
	})
	jm := &JobManager{db: db, billing: service}
	if err := jm.reconcileStripeTaxTenant(t.Context(), tenant); err == nil {
		t.Fatal("expected genuine provider synchronization failure")
	}
	if customerUpdated {
		var actual map[string]any
		if err := json.Unmarshal(raw, &actual); err != nil {
			t.Fatal(err)
		}
		actual["customer_name"], actual["customer_email"] = "Different current buyer", "current@example.test"
		actual["customer_address"] = map[string]string{"line1": "Current1", "city": "Leiden", "postal_code": "2332ED", "country": "NL"}
		var err error
		raw, err = json.Marshal(actual)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
		t.Fatal(err)
	}
	var status string
	var proof []byte
	var net, vat, gross int64
	if err := db.QueryRow(`SELECT tax_status,tax_assessment,net_minor,vat_minor,gross_minor FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid`, tenant).Scan(&status, &proof, &net, &vat, &gross); err != nil {
		t.Fatal(err)
	}
	if status != "legacy_unknown" || net != 10000 || vat != 0 || gross != 10000 {
		t.Fatalf("stale renewal changed reported money or verified tax: status=%s net=%d vat=%d gross=%d proof=%s", status, net, vat, gross, proof)
	}
	var review map[string]json.RawMessage
	if err := json.Unmarshal(proof, &review); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(proof, []byte("expected_tax_assessment")) {
		t.Fatalf("review discarded expected terms at tax point: %s", proof)
	}
}

func TestProviderTaxExpectationHistoryAtPoint_RealPG(t *testing.T) {
	db, _, _ := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	point := time.Now().UTC().Truncate(time.Second)
	profiles := make([]billingstripe.TaxProfile, 3)
	for index, country := range []string{"US", "NL", "US"} {
		if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Buyer',billing_email='buyer@example.test',billing_customer_type='consumer',billing_address=jsonb_build_object('street','Main1','city','City','postal_code','1000','country',$2::text) WHERE tenant_id=$1::uuid`, tenant, country); err != nil {
			t.Fatal(err)
		}
		p, err := AssessProviderTaxProfileAt(t.Context(), db, tenant, "", billingtax.Recurring, point.Add(time.Duration(index-3)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err = retainProviderTaxExpectation(t.Context(), db, tenant, p); err != nil {
			t.Fatal(err)
		}
		profiles[index] = *p
	}
	if profiles[0].Fingerprint() != profiles[2].Fingerprint() {
		t.Fatal("return to earlier legal terms created a different identity")
	}
	for index, p := range profiles {
		at := point.Add(time.Duration(index-3)*time.Hour + time.Minute)
		frozen, matching, err := providerTaxExpectationAtPoint(t.Context(), db, tenant, p, at)
		if err != nil || !matching || len(frozen) == 0 {
			t.Fatalf("history lost accepted terms at point%d: match%v %s %v", index, matching, frozen, err)
		}
	}
	if err := retainProviderTaxExpectation(t.Context(), db, tenant, &profiles[2]); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM purser.provider_tax_assessments WHERE tenant_id=$1::uuid`, tenant).Scan(&count); err != nil || count != 3 {
		t.Fatalf("unchanged sweep grew history: count%d %v", count, err)
	}
	// A later NL decision cannot be promoted into the earlier US tax point.
	if _, matching, err := providerTaxExpectationAtPoint(t.Context(), db, tenant, profiles[1], point.Add(-3*time.Hour+time.Minute)); err != nil || matching {
		t.Fatalf("future expected terms replaced historical terms: %v %v", matching, err)
	}
	other := seedPrepaidLedgerTenant(t, db)
	if frozen, matching, err := providerTaxExpectationAtPoint(t.Context(), db, other, profiles[1], point); err != nil || !matching || len(frozen) != 0 {
		t.Fatalf("another tenant's expected terms escaped scope: %v %s %v", matching, frozen, err)
	}
}
