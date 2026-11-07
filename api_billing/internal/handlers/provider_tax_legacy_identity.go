package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func clearProviderPolicyFields(profile *billingstripe.TaxProfile) {
	profile.PolicyReference, profile.PolicyValidFrom, profile.PolicyValidUntil = "", nil, nil
}

func sameProviderPolicyTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func canonicalProviderTaxTerms(profile billingstripe.TaxProfile) (billingstripe.TaxProfile, bool) {
	canonical, err := normalizeProviderTaxProfile(profile)
	if err != nil {
		return profile, false
	}
	var proof CryptoTaxAssessment
	if json.Unmarshal(profile.Evidence, &proof) != nil {
		return profile, false
	}
	decision, err := proof.validatedDecision()
	if err != nil || decision.RateBPS != profile.RateBPS || decision.ReverseCharge != profile.ReverseCharge || !proof.Supplier.Complete() || profile.SupplierIdentity != billingstripe.SupplierTaxIdentity(proof.Supplier.Name, proof.Supplier.Address, proof.Supplier.VATNumber, proof.Supplier.RegistrationNumber) || profile.SupplierCountry != proof.SupplierCountry {
		return profile, false
	}
	if (profile.PolicyReference != "" && profile.PolicyReference != decision.PolicyReference) || (profile.PolicyValidFrom != nil && !sameProviderPolicyTime(profile.PolicyValidFrom, decision.PolicyValidFrom)) || (profile.PolicyValidUntil != nil && !sameProviderPolicyTime(profile.PolicyValidUntil, decision.PolicyValidUntil)) {
		return profile, false
	}
	// Older provider setups kept these exact terms inside immutable evidence.
	// Missing evidence remains missing; it never stands for a current policy.
	canonical.PolicyReference = decision.PolicyReference
	canonical.PolicyValidFrom = decision.PolicyValidFrom
	canonical.PolicyValidUntil = decision.PolicyValidUntil
	return canonical, true
}

// ProviderTaxTermsEquivalent compares full legal/rate terms and the accepted
// policy reference/window, including policies retained only inside old proof.
func ProviderTaxTermsEquivalent(accepted, current billingstripe.TaxProfile) bool {
	a, acceptedOK := canonicalProviderTaxTerms(accepted)
	b, currentOK := canonicalProviderTaxTerms(current)
	if !acceptedOK || !currentOK || a.PolicyReference != b.PolicyReference || !sameProviderPolicyTime(a.PolicyValidFrom, b.PolicyValidFrom) || !sameProviderPolicyTime(a.PolicyValidUntil, b.PolicyValidUntil) {
		return false
	}
	clearProviderPolicyFields(&a)
	clearProviderPolicyFields(&b)
	return a.Fingerprint() == b.Fingerprint()
}

func retainLegacyProviderTaxShape(ctx context.Context, db *sql.DB, tenant string, current *billingstripe.TaxProfile) error {
	if current.IncompleteProfile || (current.PolicyReference == "" && current.PolicyValidFrom == nil && current.PolicyValidUntil == nil) {
		return nil
	}
	legacyShape := *current
	clearProviderPolicyFields(&legacyShape)
	legacySHA := legacyShape.Fingerprint()
	frozen, err := purserdb.New(db).GetProviderTaxProfile(ctx, purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: legacySHA})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read retained provider tax identity: %w", err)
	}
	var accepted billingstripe.TaxProfile
	if json.Unmarshal(frozen, &accepted) == nil && accepted.PolicyReference == "" && accepted.PolicyValidFrom == nil && accepted.PolicyValidUntil == nil && accepted.Fingerprint() == legacySHA && ProviderTaxTermsEquivalent(accepted, *current) {
		// Keep the actual legacy field shape so SDK fingerprint calls also retain
		// its identity. The original stored proof is never replaced or refreshed.
		clearProviderPolicyFields(current)
	}
	return nil
}
