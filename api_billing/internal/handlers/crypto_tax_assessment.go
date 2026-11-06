package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

const frozenCryptoTaxPolicy = CryptoTopupTaxPolicyRef

// CryptoTaxAssessment binds the customer, supplier and assessed tax treatment to
// the payment instructions. Settlement does not require a new VIES response.
type CryptoTaxAssessment struct {
	CustomerType      string           `json:"customer_type,omitempty"`
	Advance           *AdvanceMoney    `json:"advance_money,omitempty"`
	VATValidationMode string           `json:"vat_validation_mode,omitempty"`
	VATValidation     json.RawMessage  `json:"vat_validation,omitempty"`
	PolicyVersion     string           `json:"policy_version"`
	AssessedAt        time.Time        `json:"assessed_at"`
	SupplierCountry   string           `json:"supplier_country"`
	Supplier          DocumentSupplier `json:"supplier"`
	Decision          vatDecision      `json:"decision"`
	IPCountry         string           `json:"ip_country,omitempty"`
}

func (a *CryptoTaxAssessment) validatedDecision() (vatDecision, error) {
	if (a.PolicyVersion != frozenCryptoTaxPolicy && a.PolicyVersion != billing.FrozenInclusiveCryptoTopupTaxPolicy) || a.AssessedAt.IsZero() || !countries.IsValid(a.SupplierCountry) || !countries.IsValid(a.Decision.Country) || a.Decision.RateBPS < 0 || a.Decision.RateBPS > 10000 || strings.TrimSpace(a.Decision.Source) == "" || (a.Decision.ReverseCharge && (!a.Decision.VIESValidated || a.Decision.RateBPS != 0)) {
		return vatDecision{}, fmt.Errorf("invalid frozen crypto tax assessment")
	}
	return a.Decision, nil
}

// legacyCryptoVATDecision applies the version predating assessed quote snapshots.
// Foreign documents retain the legacy-policy reference for correction review;
// these rules are never used to advertise new payment instructions.
func (h *X402Handler) legacyCryptoVATDecision(ctx context.Context, tenantID string, profile CryptoBillingProfile, quotedAt, at time.Time) (vatDecision, error) {
	if address, err := parseBillingAddress(profile.Address); err == nil && countries.IsValid(address.Country) {
		country := countries.Normalize(address.Country)
		if billingtax.IsEU(country) {
			if strings.TrimSpace(profile.VATNumber) != "" && country != countries.Normalize(h.supplierCountry) {
				canonical, canonicalErr := billingtax.CanonicalVATNumber(country, profile.VATNumber)
				if canonicalErr != nil {
					return vatDecision{}, billingtax.Review("vat_number_invalid", "The retained VAT claim needs review before its tax invoice can be finalized")
				}
				digest := sha256.Sum256([]byte(canonical))
				prior, priorErr := purserdb.New(h.taxDatabase()).GetVATValidationAtQuote(ctx, purserdb.GetVATValidationAtQuoteParams{TenantID: tenantID, VatNumberHash: hex.EncodeToString(digest[:]), QuotedAt: quotedAt})
				var proof vatVerification
				if priorErr == nil && prior.Valid && prior.FreshAtQuote {
					proof = vatVerification{Valid: true, Snapshot: prior.ValidationSnapshot, Mode: "legacy_quote"}
				} else {
					if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
						return vatDecision{}, fmt.Errorf("read retained legacy VAT evidence: %w", priorErr)
					}
					current, validationErr := h.validateVIESVATEvidence(ctx, tenantID, country, canonical)
					if validationErr != nil {
						return vatDecision{}, billingtax.Review("vat_validation_unavailable", "VAT verification is pending; the received payment remains recorded while its tax invoice awaits verification")
					}
					proof = current
				}
				if !proof.Valid {
					return vatDecision{}, billingtax.Review("vat_number_invalid", "The retained VAT claim needs review before its tax invoice can be finalized")
				}
				if billingtax.IsEU(h.supplierCountry) && country != countries.Normalize(h.supplierCountry) {
					return vatDecision{verification: proof, Country: country, VIESValidated: true, ReverseCharge: true, Source: "Legacy quote with retained VIES validation", CheckedOn: at.Format("2006-01-02"), EffectiveFrom: at.Format("2006-01-02")}, nil
				}
				d, rateErr := h.loadEffectiveVATRate(ctx, country, at)
				d.VIESValidated = true
				d.verification = proof
				return d, rateErr
			}
			return h.loadEffectiveVATRate(ctx, country, at)
		}
		return vatDecision{Country: country, Source: "Legacy v1 non-EU treatment; review required", CheckedOn: at.Format("2006-01-02"), EffectiveFrom: at.Format("2006-01-02")}, nil
	}
	d, err := h.loadEffectiveVATRate(ctx, h.supplierCountry, at)
	d.Source += "; legacy v1 anonymous top-up"
	return d, err
}

// CryptoBusinessProfileFingerprint ties operator establishment evidence to the
// saved legal identity; changing billing details invalidates the approval.
func CryptoBusinessProfileFingerprint(profile CryptoBillingProfile) (string, error) {
	if profile.effectiveCustomerType() != "business" || !profile.Complete() {
		return "", fmt.Errorf("complete business billing profile required")
	}
	address, err := parseBillingAddress(profile.Address)
	if err != nil || !countries.IsValid(address.Country) {
		return "", fmt.Errorf("valid business billing address required")
	}
	profile.CustomerType = "business"
	profile.TaxAssessment = nil
	profile.Email = strings.TrimSpace(profile.Email)
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Company = strings.TrimSpace(profile.Company)
	profile.VATNumber = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(profile.VATNumber), " ", ""))
	address.Street = strings.TrimSpace(address.Street)
	address.City = strings.TrimSpace(address.City)
	address.PostalCode = strings.TrimSpace(address.PostalCode)
	address.Country = countries.Normalize(address.Country)
	profile.Address, err = json.Marshal(address)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

// cryptoSettlementVATDecision retains accepted exclusive advance terms.
// Inclusive legacy quotes use the confirmation rate without changing their
// retained treatment or VIES proof.
func (h *X402Handler) cryptoSettlementVATDecision(ctx context.Context, tenantID string, profile CryptoBillingProfile, quotedAt, at time.Time) (vatDecision, error) {
	if profile.TaxAssessment == nil {
		return h.legacyCryptoVATDecision(ctx, tenantID, profile, quotedAt, at)
	}
	d, err := profile.TaxAssessment.validatedDecision()
	if err != nil {
		return vatDecision{}, err
	}
	if profile.TaxAssessment.Advance != nil {
		return d, nil
	}
	if d.Treatment == "domestic_vat" || d.Treatment == "eu_b2c_destination" || d.Treatment == "eu_b2c_home" {
		rateCountry := d.Country
		if d.Treatment == "eu_b2c_home" {
			rateCountry = profile.TaxAssessment.SupplierCountry
		}
		rate, err := h.loadEffectiveVATRate(ctx, rateCountry, at)
		if err != nil {
			return vatDecision{}, err
		}
		d.RateBPS = rate.RateBPS
		d.Source = rate.Source
		d.CheckedOn = rate.CheckedOn
		d.EffectiveFrom = rate.EffectiveFrom
	}
	return d, nil
}

// QuoteExpiry never advertises payment instructions beyond the review that
// authorized them. Late on-chain receipts still reconcile and retain a review.
func (a *CryptoTaxAssessment) QuoteExpiry(deadline, now time.Time) (time.Time, error) {
	if a != nil && a.Decision.PolicyValidUntil != nil && a.Decision.PolicyValidUntil.Before(deadline) {
		deadline = *a.Decision.PolicyValidUntil
	}
	if a != nil && len(a.Decision.BusinessEstablishment) > 0 {
		proof, err := decodeBusinessReview(a.Decision.BusinessEstablishment)
		if err != nil {
			return time.Time{}, billingtax.Review("business_evidence_required", "business establishment review has no valid reference or expiry")
		}
		if proof.ValidUntil.Before(deadline) {
			deadline = proof.ValidUntil
		}
	}
	if !deadline.After(now) {
		return time.Time{}, billingtax.Review("foreign_tax_policy_required", "the reviewed destination policy expired before payment instructions could be issued")
	}
	return deadline, nil
}

type businessReviewValidity struct {
	Reference  string    `json:"reference"`
	ValidUntil time.Time `json:"valid_until"`
}

func decodeBusinessReview(raw json.RawMessage) (businessReviewValidity, error) {
	var proof businessReviewValidity
	if err := json.Unmarshal(raw, &proof); err != nil {
		return proof, fmt.Errorf("decode business establishment review: %w", err)
	}
	if strings.TrimSpace(proof.Reference) == "" || proof.ValidUntil.IsZero() {
		return proof, fmt.Errorf("business establishment review has no reference or expiry")
	}
	return proof, nil
}

func (h *X402Handler) loadBusinessReview(ctx context.Context, tenantID, fingerprint, country string, at time.Time) (json.RawMessage, businessReviewValidity, error) {
	raw, err := purserdb.New(h.taxDatabase()).GetBusinessEstablishmentReviewSnapshot(ctx, purserdb.GetBusinessEstablishmentReviewSnapshotParams{TenantID: tenantID, ProfileSha256: sql.NullString{String: fingerprint, Valid: true}, Country: sql.NullString{String: country, Valid: true}, ValidAt: at})
	if err != nil {
		return nil, businessReviewValidity{}, err
	}
	proof, err := decodeBusinessReview(raw)
	return raw, proof, err
}
