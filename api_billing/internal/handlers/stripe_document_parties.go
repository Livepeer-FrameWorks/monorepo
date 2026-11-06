package handlers

import (
	"encoding/json"
	"strings"

	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

func stripeIssuedPartiesReview(doc billingstripe.ProviderTaxDocument, profile billingstripe.TaxProfile) []string {
	var buyer struct {
		Name    string `json:"name"`
		Address struct {
			Street     string `json:"line1"`
			City       string `json:"city"`
			PostalCode string `json:"postal_code"`
			Country    string `json:"country"`
		} `json:"address"`
		TaxIDs []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"tax_ids"`
	}
	if json.Unmarshal(doc.Customer, &buyer) != nil {
		return []string{"issued_buyer_evidence_invalid"}
	}
	legalName := strings.TrimSpace(profile.Company)
	if legalName == "" {
		legalName = strings.TrimSpace(profile.Name)
	}
	if legalName == "" || strings.TrimSpace(buyer.Name) != legalName || strings.TrimSpace(profile.Street) == "" || strings.TrimSpace(buyer.Address.Street) != strings.TrimSpace(profile.Street) || strings.TrimSpace(profile.City) == "" || strings.TrimSpace(buyer.Address.City) != strings.TrimSpace(profile.City) || strings.TrimSpace(profile.PostalCode) == "" || strings.TrimSpace(buyer.Address.PostalCode) != strings.TrimSpace(profile.PostalCode) || !countries.IsValid(buyer.Address.Country) || countries.Normalize(buyer.Address.Country) != countries.Normalize(profile.Country) {
		return []string{"issued_buyer_identity_mismatch"}
	}
	if profile.ReverseCharge {
		expected, err := billingtax.CanonicalVATNumber(profile.Country, profile.VATNumber)
		if err != nil {
			return []string{"issued_buyer_vat_missing"}
		}
		found := false
		for _, id := range buyer.TaxIDs {
			if id.Type != "eu_vat" {
				continue
			}
			actual, err := billingtax.CanonicalVATNumber(profile.Country, id.Value)
			if err == nil && actual == expected {
				found = true
			}
		}
		if !found {
			return []string{"issued_buyer_vat_missing"}
		}
	}
	var supplier struct {
		Country string `json:"country"`
		Footer  string `json:"footer"`
	}
	if json.Unmarshal(doc.Supplier, &supplier) != nil || !countries.IsValid(supplier.Country) || countries.Normalize(supplier.Country) != countries.Normalize(profile.SupplierCountry) || strings.TrimSpace(profile.SupplierIdentity) == "" {
		return []string{"issued_supplier_identity_mismatch"}
	}
	// Stripe's account display name may be a brand. The configured footer
	// carries the complete accepted legal seller and registration details.
	if !strings.Contains(supplier.Footer, strings.TrimSpace(profile.SupplierIdentity)) {
		return []string{"issued_supplier_identity_missing"}
	}
	return nil
}
