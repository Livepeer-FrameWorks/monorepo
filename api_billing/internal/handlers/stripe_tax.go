package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
)

func AssessStripeTaxProfile(ctx context.Context, db *sql.DB, tenant, clientIP string) (*billingstripe.TaxProfile, error) {
	rt := appconfig.Runtime()
	if DocumentSupplierSnapshot() == "" {
		return nil, fmt.Errorf("complete supplier identity required before checkout")
	}
	h := &X402Handler{db: db, supplierCountry: rt.SupplierCountry, supplierName: rt.SupplierName, supplierAddress: rt.SupplierAddress, supplierVAT: rt.SupplierVATNumber, supplierRegistration: rt.SupplierRegistrationNumber}
	requirement, err := h.GetCryptoDocumentRequirement(ctx, tenant, 1, clientIP)
	if err != nil {
		return nil, err
	}
	profile := requirement.Profile
	if requirement.RequiresCustomerType && !requirement.RequiresBillingCountry && profile.Complete() {
		profile.CustomerType = "consumer"
		decision, decisionErr := h.getVATDecisionForProfile(ctx, tenant, profile, clientIP, time.Now().UTC())
		if decisionErr != nil {
			return nil, decisionErr
		}
		profile.TaxAssessment = &CryptoTaxAssessment{Decision: decision, PolicyVersion: frozenCryptoTaxPolicy, SupplierCountry: rt.SupplierCountry, Supplier: ConfiguredDocumentSupplier(), AssessedAt: time.Now().UTC()}
		requirement.RequiresCompleteProfile = false
	}
	if requirement.RequiresCompleteProfile || !profile.Complete() || profile.TaxAssessment == nil {
		return nil, billingtax.Review("billing_profile_required", "Complete your saved billing details before paying")
	}
	address, err := parseBillingAddress(profile.Address)
	if err != nil {
		return nil, err
	}
	d := profile.TaxAssessment.Decision
	vatNumber := profile.VATNumber
	if vatNumber != "" && billingtax.IsEU(address.Country) {
		vatNumber, err = billingtax.CanonicalVATNumber(address.Country, vatNumber)
		if err != nil {
			return nil, billingtax.Review("vat_number_invalid", "Check the VAT number in your billing details")
		}
	}
	assessment := *profile.TaxAssessment
	assessment.AssessedAt = time.Time{}
	evidence, err := json.Marshal(assessment)
	if err != nil {
		return nil, err
	}
	p := &billingstripe.TaxProfile{Name: profile.Name, Company: profile.Company, Email: profile.Email, Street: address.Street, City: address.City, PostalCode: address.PostalCode, Country: address.Country, VATNumber: vatNumber, RateBPS: d.RateBPS, ReverseCharge: d.ReverseCharge, Wording: d.Wording, SupplierCountry: rt.SupplierCountry, SupplierIdentity: billingstripe.SupplierTaxIdentity(rt.SupplierName, rt.SupplierAddress, rt.SupplierVATNumber, rt.SupplierRegistrationNumber), Evidence: evidence}
	p.ProfileSHA = p.Fingerprint()
	frozen, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if err = purserdb.New(db).StoreProviderTaxProfile(ctx, purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: p.ProfileSHA, Profile: frozen}); err != nil {
		return nil, err
	}
	return p, nil
}

func (jm *JobManager) reconcileStripeTaxTenant(ctx context.Context, tenant string) error {
	if jm.billing == nil || jm.billing.stripeClient == nil {
		return nil
	}
	subscriptions, err := purserdb.New(jm.db).ListTenantStripeTaxSubscriptions(ctx, tenant)
	if err != nil {
		return err
	}
	if len(subscriptions) == 0 {
		return nil
	}
	assessed, err := AssessStripeTaxProfile(ctx, jm.db, tenant, "")
	if err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		if subscription.SubscriptionID != "" {
			err = jm.billing.stripeClient.SyncSubscriptionTax(ctx, tenant, subscription.CustomerID, subscription.SubscriptionID, *assessed)
		} else {
			_, err = jm.billing.stripeClient.PrepareCustomerTax(ctx, tenant, subscription.CustomerID, *assessed)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
