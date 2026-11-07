//go:build schema_verify

package handlers

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func TestProviderPolicyRenewalRetainsOriginalTaxPointProof_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	newPoint := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	oldUntil := newPoint.Add(-time.Hour)
	oldPoint := oldUntil.Add(-time.Hour)
	raw, base := historicalStripeReverseCharge(t, db, tenant, "in_policy_template", oldPoint)
	q := purserdb.New(db)
	retain := func(reference string, from, until, assessed time.Time) billingstripe.TaxProfile {
		t.Helper()
		profile := base
		profile.Country, profile.City, profile.PostalCode, profile.VATNumber = "US", "Austin", "78701", ""
		profile.ReverseCharge = false
		profile.Wording = "Outside scope of Dutch VAT"
		profile.PolicyReference, profile.PolicyValidFrom, profile.PolicyValidUntil = reference, &from, &until
		proof := CryptoTaxAssessment{CustomerType: "consumer", PolicyVersion: frozenCryptoTaxPolicy, AssessedAt: assessed, SupplierCountry: "NL", Supplier: ConfiguredDocumentSupplier(), Decision: vatDecision{Country: "US", RateBPS: 0, Source: "Accepted US consumer policy", Treatment: "non_eu_b2c", Wording: profile.Wording, PolicyReference: reference, PolicyValidFrom: &from, PolicyValidUntil: &until}}
		var err error
		profile.Evidence, err = json.Marshal(proof)
		if err != nil {
			t.Fatal(err)
		}
		profile.ProfileSHA = profile.Fingerprint()
		frozen, err := json.Marshal(profile)
		if err != nil {
			t.Fatal(err)
		}
		if err = q.StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: profile.ProfileSHA, Profile: frozen}); err != nil {
			t.Fatal(err)
		}
		return profile
	}
	old := retain("US-policy-original", oldPoint.Add(-24*time.Hour), oldUntil, oldPoint.Add(-time.Hour))
	original, err := q.GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: old.ProfileSHA})
	if err != nil {
		t.Fatal(err)
	}
	renewed := retain("US-policy-renewed", oldUntil, newPoint.Add(24*time.Hour), newPoint.Add(-30*time.Minute))
	if old.ProfileSHA == renewed.ProfileSHA {
		t.Fatal("same-rate renewal reused the expired policy profile")
	}
	for _, tc := range []struct {
		name, status string
		point        time.Time
		profile      billingstripe.TaxProfile
	}{
		{"original_in_window", "ready", oldPoint, old},
		{"renewed_in_window", "ready", newPoint, renewed},
		{"original_after_expiry", "legacy_unknown", newPoint, old},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			id := "in_policy_" + tc.name
			payload["id"], payload["number"], payload["created"] = id, "DDV-"+id, tc.point.Unix()
			payload["footer"] = tc.profile.SupplierIdentity + "\n" + tc.profile.Wording
			payload["customer_address"] = map[string]string{"line1": tc.profile.Street, "city": tc.profile.City, "postal_code": tc.profile.PostalCode, "country": tc.profile.Country}
			payload["customer_tax_ids"] = []any{}
			payload["metadata"] = map[string]string{"frameworks_tax_profile": tc.profile.ProfileSHA}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", encoded); err != nil {
				t.Fatal(err)
			}
			var status string
			if err = db.QueryRowContext(t.Context(), `SELECT tax_status FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND provider_document_id=$2`, tenant, id).Scan(&status); err != nil || status != tc.status {
				t.Fatalf("issued policy treatment=%s want%s err=%v", status, tc.status, err)
			}
		})
	}
	retained, err := q.GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: old.ProfileSHA})
	if err != nil || !bytes.Equal(original, retained) {
		t.Fatalf("renewed policy rewrote original accepted proof: %v", err)
	}
}
