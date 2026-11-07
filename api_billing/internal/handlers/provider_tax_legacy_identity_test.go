package handlers

import (
	"encoding/json"
	"testing"
	"time"

	billingstripe "frameworks/api_billing/internal/stripe"
)

func providerPolicyTermsFixture(t *testing.T) billingstripe.TaxProfile {
	t.Helper()
	supplier := DocumentSupplier{Name: "DDVTech B.V.", Address: "Leiden NL", VATNumber: "NL853319145B01", RegistrationNumber: "59104694"}
	from, until := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	proof := CryptoTaxAssessment{CustomerType: "business", PolicyVersion: frozenCryptoTaxPolicy, AssessedAt: from, SupplierCountry: "NL", Supplier: supplier, Decision: vatDecision{Country: "US", RateBPS: 0, Source: "Accepted service policy", Treatment: "non_eu_b2b", PolicyReference: "policy-original", PolicyValidFrom: &from, PolicyValidUntil: &until}}
	evidence, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	return billingstripe.TaxProfile{Name: "Buyer", Email: "buyer@example.test", Street: "Main1", City: "Austin", PostalCode: "78701", Country: "US", SupplierCountry: "NL", SupplierIdentity: billingstripe.SupplierTaxIdentity(supplier.Name, supplier.Address, supplier.VATNumber, supplier.RegistrationNumber), PolicyReference: proof.Decision.PolicyReference, PolicyValidFrom: &from, PolicyValidUntil: &until, Evidence: evidence}
}

func TestProviderTaxTermsCompatibilityRetainsOnlyIdenticalAcceptedPolicy(t *testing.T) {
	current := providerPolicyTermsFixture(t)
	legacy := current
	clearProviderPolicyFields(&legacy)
	if !ProviderTaxTermsEquivalent(legacy, current) {
		t.Fatal("same immutable policy window differed only by legacy field placement")
	}
	for _, change := range []string{"renewed_window", "changed_reference", "missing_policy_proof", "contradicting_top_fields", "changed_buyer", "changed_supplier", "changed_rate", "missing_evidence"} {
		t.Run(change, func(t *testing.T) {
			candidate := legacy
			var proof CryptoTaxAssessment
			if err := json.Unmarshal(candidate.Evidence, &proof); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "renewed_window":
				later := proof.Decision.PolicyValidUntil.Add(time.Hour)
				proof.Decision.PolicyValidUntil = &later
			case "changed_reference":
				proof.Decision.PolicyReference = "policy-renewed"
			case "missing_policy_proof":
				proof.Decision.PolicyReference, proof.Decision.PolicyValidFrom, proof.Decision.PolicyValidUntil = "", nil, nil
			case "contradicting_top_fields":
				candidate.PolicyReference = "unproven-top-policy"
			case "changed_buyer":
				candidate.Name = "Different Buyer"
			case "changed_supplier":
				candidate.SupplierIdentity = "Different supplier"
			case "changed_rate":
				candidate.RateBPS, proof.Decision.RateBPS = 2100, 2100
			}
			candidate.Evidence, _ = json.Marshal(proof)
			if change == "missing_evidence" {
				candidate.Evidence = nil
			}
			if ProviderTaxTermsEquivalent(candidate, current) {
				t.Fatal("different or unproven accepted terms reused legacy identity")
			}
		})
	}
	var refreshed CryptoTaxAssessment
	if err := json.Unmarshal(legacy.Evidence, &refreshed); err != nil {
		t.Fatal(err)
	}
	refreshed.AssessedAt = time.Now().UTC()
	refreshed.VATValidation = json.RawMessage(`{"checked_at":"2026-11-07T12:00:00Z","request_date":"2026-11-07"}`)
	legacy.Evidence, _ = json.Marshal(refreshed)
	if !ProviderTaxTermsEquivalent(legacy, current) {
		t.Fatal("daily validation refresh changed unchanged accepted policy terms")
	}
}
