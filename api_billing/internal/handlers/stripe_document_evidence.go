package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
)

// Issued provider documents are verified against retained acceptance and facts
// that existed at their tax point. Today's customer profile is irrelevant.
func (s *Service) stripeTaxEvidenceAtIssue(ctx context.Context, tenant string, doc billingstripe.ProviderTaxDocument, profile billingstripe.TaxProfile) (billingstripe.TaxProfile, []string, error) {
	var proof CryptoTaxAssessment
	if err := json.Unmarshal(profile.Evidence, &proof); err != nil {
		return profile, []string{"retained_tax_evidence_invalid"}, nil //nolint:nilerr // Malformed immutable evidence needs review; retries cannot repair it.
	}
	if _, err := proof.validatedDecision(); err != nil {
		return profile, []string{"retained_tax_evidence_invalid"}, nil //nolint:nilerr // An invalid retained claim is a review outcome, not an operational failure.
	}
	// Stripe timestamps have whole-second precision.
	point := doc.IssuedAt.Add(time.Second - time.Nanosecond)
	if proof.AssessedAt.After(point) {
		return profile, []string{"tax_evidence_after_tax_point"}, nil
	}
	supplier := proof.Supplier
	if strings.TrimSpace(supplier.Name) == "" || strings.TrimSpace(supplier.Address) == "" || strings.TrimSpace(supplier.VATNumber) == "" || strings.TrimSpace(supplier.RegistrationNumber) == "" ||
		billingstripe.SupplierTaxIdentity(supplier.Name, supplier.Address, supplier.VATNumber, supplier.RegistrationNumber) != profile.SupplierIdentity || proof.SupplierCountry != profile.SupplierCountry {
		return profile, []string{"retained_supplier_evidence_invalid"}, nil
	}
	if proof.Decision.PolicyValidFrom != nil && doc.IssuedAt.Before(*proof.Decision.PolicyValidFrom) || proof.Decision.PolicyValidUntil != nil && !doc.IssuedAt.Before(*proof.Decision.PolicyValidUntil) {
		return profile, []string{"policy_evidence_not_valid_at_tax_point"}, nil
	}
	if proof.Decision.RateBPS != profile.RateBPS || proof.Decision.ReverseCharge != profile.ReverseCharge {
		return profile, []string{"retained_tax_terms_mismatch"}, nil
	}
	if reasons, err := s.stripeBusinessEvidenceAtIssue(ctx, tenant, doc, profile, proof); err != nil || len(reasons) > 0 {
		return profile, reasons, err
	}
	if profile.ReverseCharge {
		hash := sha256.Sum256([]byte(profile.VATNumber))
		if retainedVATCoversStripeTaxPoint(proof.VATValidation, tenant, profile.Country, hex.EncodeToString(hash[:]), point) {
			proof.VATValidationMode = "tax_point"
			encoded, err := json.Marshal(proof)
			profile.Evidence = encoded
			return profile, nil, err
		}
		validation, err := purserdb.New(s.db).GetVATValidationAtQuote(ctx, purserdb.GetVATValidationAtQuoteParams{TenantID: tenant, VatNumberHash: hex.EncodeToString(hash[:]), QuotedAt: point})
		if errors.Is(err, sql.ErrNoRows) {
			return profile, []string{"vat_evidence_missing_at_tax_point"}, nil
		}
		if err != nil {
			return profile, nil, err
		}
		if !validation.Valid {
			return profile, []string{"vat_evidence_invalid_at_tax_point"}, nil
		}
		proof.VATValidation = validation.ValidationSnapshot
		if !validation.FreshAtQuote {
			proof.VATValidationMode = "outage_grace"
			profile.Evidence, err = json.Marshal(proof)
			return profile, []string{"vat_evidence_requires_document_review"}, err
		}
		proof.VATValidationMode = "tax_point"
	}
	var err error
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
