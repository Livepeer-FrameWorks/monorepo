package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
)

// Issued provider documents are verified against retained acceptance and facts
// that existed at their tax point. Today's customer profile is irrelevant.
func (s *Service) stripeTaxEvidenceAtIssue(ctx context.Context, tenant string, doc billingstripe.ProviderTaxDocument, profile billingstripe.TaxProfile) (billingstripe.TaxProfile, []string, error) {
	// Stripe timestamps have whole-second precision.
	return historicalProviderTaxEvidence(ctx, s.db, tenant, profile, doc.IssuedAt.Add(time.Second-time.Nanosecond))
}

func historicalProviderTaxEvidence(ctx context.Context, db purserdb.DBTX, tenant string, profile billingstripe.TaxProfile, point time.Time) (billingstripe.TaxProfile, []string, error) {
	var proof TaxAssessment
	if json.Unmarshal(profile.Evidence, &proof) != nil {
		return profile, []string{"retained_tax_evidence_invalid"}, nil //nolint:nilerr // Malformed retained proof is a permanent review outcome.
	}
	supplier := proof.Supplier
	if !supplier.Complete() || billingstripe.SupplierTaxIdentity(supplier.Name, supplier.Address, supplier.VATNumber, supplier.RegistrationNumber) != profile.SupplierIdentity || proof.SupplierCountry != profile.SupplierCountry {
		return profile, []string{"retained_supplier_evidence_invalid"}, nil
	}
	if proof.Decision.RateBPS != profile.RateBPS || proof.Decision.ReverseCharge != profile.ReverseCharge {
		return profile, []string{"retained_tax_terms_mismatch"}, nil
	}
	request := billingtax.AssessmentRequest{Purpose: billingtax.Historical, At: point, TenantID: tenant, Retained: &proof, Profile: billingtax.Profile{Country: profile.Country, VATNumber: profile.VATNumber}}
	_, err := billingtax.Assess(ctx, request, nil, nil)
	var review *billingtax.ReviewError
	if errors.As(err, &review) && review.Code == "vat_evidence_missing_at_tax_point" {
		canonical, canonicalErr := billingtax.CanonicalVATNumber(profile.Country, profile.VATNumber)
		if canonicalErr != nil {
			return profile, []string{"retained_tax_terms_invalid"}, nil //nolint:nilerr // An invalid retained claim requires review rather than an operational retry.
		}
		hash := sha256.Sum256([]byte(canonical))
		prior, readErr := purserdb.New(db).GetVATValidationAtQuote(ctx, purserdb.GetVATValidationAtQuoteParams{TenantID: tenant, VatNumberHash: hex.EncodeToString(hash[:]), QuotedAt: point})
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return profile, nil, readErr
		}
		if readErr == nil && prior.Valid && !prior.FreshAtQuote {
			return profile, []string{"vat_evidence_requires_document_review"}, nil
		}
		if readErr == nil && prior.Valid && prior.FreshAtQuote {
			proof.VATValidation = prior.ValidationSnapshot
			proof.VATValidationMode = "tax_point"
			request.Retained = &proof
			_, err = billingtax.Assess(ctx, request, nil, nil)
		}
	}
	if err != nil {
		if errors.As(err, &review) {
			return profile, []string{review.Code}, nil
		}
		return profile, nil, err
	}
	expected, matching, expectedErr := providerTaxExpectationAtPoint(ctx, db, tenant, profile, point)
	if expectedErr != nil {
		return profile, nil, expectedErr
	}
	if !matching {
		proof.ExpectedTaxAssessment = expected
		profile.Evidence, err = json.Marshal(proof)
		return profile, []string{"provider_tax_sync_stale_at_tax_point"}, err
	}
	if proof.VATValidationMode == "outage_grace" {
		return profile, []string{"vat_evidence_requires_document_review"}, nil
	}
	profile.Evidence, err = json.Marshal(proof)
	return profile, nil, err
}

func retainedVATCoversStripeTaxPoint(raw json.RawMessage, tenant, country, hash string, point time.Time) bool {
	var snapshot struct {
		Tenant    string    `json:"tenant_id"`
		Country   string    `json:"country_code"`
		Hash      string    `json:"vat_number_hash"`
		Valid     bool      `json:"valid"`
		CheckedAt time.Time `json:"checked_at"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	return json.Unmarshal(raw, &snapshot) == nil && snapshot.Valid && snapshot.Tenant == tenant && snapshot.Hash == hash &&
		billingtax.NormalizeVATCountry(snapshot.Country) == billingtax.NormalizeVATCountry(country) &&
		!snapshot.CheckedAt.IsZero() && !snapshot.CheckedAt.After(point) && point.Before(snapshot.ExpiresAt)
}

func providerTaxEvidenceNeedsReview(raw json.RawMessage) bool {
	var proof struct {
		Review   bool   `json:"review_required"`
		Mode     string `json:"vat_validation_mode"`
		Evidence struct {
			Review bool   `json:"review_required"`
			Mode   string `json:"vat_validation_mode"`
		} `json:"evidence"`
	}
	if json.Unmarshal(raw, &proof) != nil {
		return true
	}
	return proof.Review || proof.Evidence.Review || proof.Mode == "outage_grace" || proof.Evidence.Mode == "outage_grace"
}
