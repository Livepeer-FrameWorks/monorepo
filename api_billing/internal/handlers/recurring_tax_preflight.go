package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
)

func recurringTaxReviewEvidence(profile *billingstripe.TaxProfile, customer, subscription string) (json.RawMessage, error) {
	var proof CryptoTaxAssessment
	if err := json.Unmarshal(profile.Evidence, &proof); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"review_required": true, "reasons": proof.ReviewReasons, "fallback_rate_bps": profile.RateBPS, "profile_sha": profile.Fingerprint(), "customer_id": customer, "subscription_id": subscription})
}

// Recurring collection retains an unresolved claim separately from the domestic
// tax it charges, so an incomplete profile cannot select an exemption first.
func (h *X402Handler) recurringTaxRequirement(ctx context.Context, tenant, clientIP string) (CryptoDocumentRequirement, error) {
	row, err := purserdb.New(h.db).GetCryptoDocumentBillingProfile(ctx, tenant)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CryptoDocumentRequirement{}, err
	}
	p := CryptoBillingProfile{CustomerType: row.CustomerType, Email: strings.TrimSpace(row.BillingEmail.String), Name: strings.TrimSpace(row.BillingName.String), Company: strings.TrimSpace(row.BillingCompany.String), VATNumber: strings.TrimSpace(row.TaxID.String), Address: append(json.RawMessage(nil), row.BillingAddress...)}
	p.CustomerType = p.effectiveCustomerType()
	if p.CustomerType == "" {
		p.CustomerType = "consumer"
	}
	review := func(code string) (CryptoDocumentRequirement, error) {
		p.TaxAssessment = &CryptoTaxAssessment{ReviewRequired: true, ReviewReasons: []string{code}}
		return CryptoDocumentRequirement{Profile: p}, nil
	}
	if !p.Complete() {
		return review(incompleteRecurringProfileReview)
	}
	address, err := parseBillingAddress(p.Address)
	if err != nil {
		return CryptoDocumentRequirement{}, err
	}
	if p.CustomerType == "consumer" && p.VATNumber != "" {
		return review("consumer_vat_claim")
	}
	if p.VATNumber != "" && billingtax.IsEU(address.Country) {
		if _, err = billingtax.CanonicalVATNumber(address.Country, p.VATNumber); err != nil {
			return review("vat_number_invalid")
		}
	}
	at := time.Now().UTC()
	d, err := h.getVATDecisionForProfile(ctx, tenant, p, clientIP, at)
	if err != nil {
		var claim *billingtax.ReviewError
		if errors.As(err, &claim) && !billingtax.IsTransient(err) {
			switch claim.Code {
			case "billing_profile_required", "vat_number_invalid", "vat_number_required", "consumer_vat_claim", "business_evidence_required":
				return review(claim.Code)
			}
		}
		return CryptoDocumentRequirement{}, err
	}
	p.TaxAssessment = &CryptoTaxAssessment{PolicyVersion: frozenCryptoTaxPolicy, AssessedAt: at, SupplierCountry: h.supplierCountry, Supplier: DocumentSupplier{Name: h.supplierName, Address: h.supplierAddress, VATNumber: h.supplierVAT, RegistrationNumber: h.supplierRegistration}, Decision: d, IPCountry: h.getCountryFromIP(clientIP)}
	if d.VIESValidated {
		if len(d.verification.Snapshot) == 0 {
			return CryptoDocumentRequirement{}, billingtax.Review("vat_validation_unavailable", "VAT evidence could not be retained; retry before paying")
		}
		p.TaxAssessment.VATValidation = append(json.RawMessage(nil), d.verification.Snapshot...)
		p.TaxAssessment.VATValidationMode = d.verification.Mode
	}
	return CryptoDocumentRequirement{Profile: p}, nil
}
