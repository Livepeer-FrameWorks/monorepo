package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
)

// An outage cannot change an unchanged accepted reverse-charge subscription.
// A different legal identity or VAT claim needs a new decision. Retained terms
// are consulted only to choose retry versus provisional domestic collection;
// they do not validate a newly issued historical tax document.
func unchangedRecurringReverseCharge(ctx context.Context, db purserdb.DBTX, tenant string, profile BillingProfile, at time.Time) (bool, error) {
	candidate, valid := recurringReverseChargeCandidate(profile)
	if !valid {
		return false, nil
	}
	q := purserdb.New(db)
	retained, err := q.GetProviderTaxExpectationAtPoint(ctx, purserdb.GetProviderTaxExpectationAtPointParams{TenantID: tenant, TaxPoint: at})
	if errors.Is(err, sql.ErrNoRows) {
		retained, err = q.GetProviderTaxProfile(ctx, purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: candidate.Fingerprint()})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return acceptedReverseChargeUnchanged(retained, candidate, at), nil
}

func acceptedReverseChargeUnchanged(retained json.RawMessage, candidate billingstripe.TaxProfile, at time.Time) bool {
	var accepted billingstripe.TaxProfile
	if json.Unmarshal(retained, &accepted) != nil {
		return false
	}
	var proof TaxAssessment
	if json.Unmarshal(accepted.Evidence, &proof) != nil {
		return false
	}
	decision, err := proof.ValidatedDecision()
	if err != nil || proof.AssessedAt.After(at) || !decision.ReverseCharge || !decision.VIESValidated || accepted.Fingerprint() != candidate.Fingerprint() {
		return false
	}
	return true
}

func recurringReverseChargeCandidate(profile BillingProfile) (billingstripe.TaxProfile, bool) {
	address, err := parseBillingAddress(profile.Address)
	if err != nil {
		return billingstripe.TaxProfile{}, false
	}
	vat, err := billingtax.CanonicalVATNumber(address.Country, profile.VATNumber)
	if err != nil {
		return billingstripe.TaxProfile{}, false
	}
	rt := appconfig.Runtime()
	candidate := billingstripe.TaxProfile{Name: profile.Name, Company: profile.Company, Email: profile.Email, Street: address.Street, City: address.City, PostalCode: address.PostalCode, Country: address.Country, VATNumber: vat, ReverseCharge: true, Wording: "VAT reverse charged / btw verlegd", SupplierCountry: rt.SupplierCountry, SupplierIdentity: billingstripe.SupplierTaxIdentity(rt.SupplierName, rt.SupplierAddress, rt.SupplierVATNumber, rt.SupplierRegistrationNumber)}
	candidate, err = normalizeProviderTaxProfile(candidate)
	if err != nil {
		return billingstripe.TaxProfile{}, false
	}
	return candidate, true
}
