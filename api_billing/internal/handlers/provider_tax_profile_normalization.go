package handlers

import (
	"strings"

	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

func normalizeProviderTaxProfile(profile billingstripe.TaxProfile) (billingstripe.TaxProfile, error) {
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Company = strings.TrimSpace(profile.Company)
	profile.Email = strings.TrimSpace(profile.Email)
	profile.Street = strings.TrimSpace(profile.Street)
	profile.City = strings.TrimSpace(profile.City)
	profile.PostalCode = strings.TrimSpace(profile.PostalCode)
	profile.Country = countries.Normalize(profile.Country)
	profile.VATNumber = strings.TrimSpace(profile.VATNumber)
	if profile.VATNumber != "" && billingtax.IsEU(profile.Country) {
		canonical, err := billingtax.CanonicalVATNumber(profile.Country, profile.VATNumber)
		if err != nil {
			return profile, err
		}
		profile.VATNumber = canonical
	}
	return profile, nil
}
