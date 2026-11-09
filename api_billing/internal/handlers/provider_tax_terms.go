package handlers

import (
	"encoding/json"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func canonicalProviderTaxTerms(profile billingstripe.TaxProfile) (billingstripe.TaxProfile, bool) {
	canonical, err := normalizeProviderTaxProfile(profile)
	if err != nil {
		return profile, false
	}
	var proof CryptoTaxAssessment
	if json.Unmarshal(profile.Evidence, &proof) != nil {
		return profile, false
	}
	decision, err := proof.ValidatedDecision()
	if err != nil || decision.RateBPS != profile.RateBPS || decision.ReverseCharge != profile.ReverseCharge || !proof.Supplier.Complete() || profile.SupplierIdentity != billingstripe.SupplierTaxIdentity(proof.Supplier.Name, proof.Supplier.Address, proof.Supplier.VATNumber, proof.Supplier.RegistrationNumber) || profile.SupplierCountry != proof.SupplierCountry {
		return profile, false
	}
	return canonical, true
}

// ProviderTaxTermsEquivalent compares the full retained legal and charged terms.
// Validation history is checked separately at the payment or invoice tax point.
func ProviderTaxTermsEquivalent(accepted, current billingstripe.TaxProfile) bool {
	a, acceptedOK := canonicalProviderTaxTerms(accepted)
	b, currentOK := canonicalProviderTaxTerms(current)
	return acceptedOK && currentOK && a.Fingerprint() == b.Fingerprint()
}
