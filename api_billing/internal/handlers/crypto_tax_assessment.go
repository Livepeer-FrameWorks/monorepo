package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

const frozenCryptoTaxPolicy = CryptoTopupTaxPolicyRef

type TaxAssessment = billingtax.Assessment

// CryptoTaxAssessment names historical crypto snapshots; all providers share the assessment contract.
type CryptoTaxAssessment = billingtax.Assessment

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
					return vatDecision{Verification: proof, Country: country, VIESValidated: true, ReverseCharge: true, Source: "Legacy quote with retained VIES validation", CheckedOn: at.Format("2006-01-02"), EffectiveFrom: at.Format("2006-01-02")}, nil
				}
				d, rateErr := h.loadEffectiveVATRate(ctx, country, at)
				d.VIESValidated = true
				d.Verification = proof
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

// cryptoSettlementVATDecision retains accepted exclusive advance terms.
// Inclusive legacy quotes use the confirmation rate without changing their
// retained treatment or VIES proof.
func (h *X402Handler) cryptoSettlementVATDecision(ctx context.Context, tenantID string, profile CryptoBillingProfile, quotedAt, at time.Time) (vatDecision, error) {
	if profile.TaxAssessment == nil {
		return h.legacyCryptoVATDecision(ctx, tenantID, profile, quotedAt, at)
	}
	d, err := profile.TaxAssessment.ValidatedDecision()
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
