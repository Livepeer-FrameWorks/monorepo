package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

const incompleteRecurringProfileReview = "billing_profile_incomplete_domestic_vat"

func AssessRecurringProviderTaxProfile(ctx context.Context, db *sql.DB, tenant, clientIP string) (*billingstripe.TaxProfile, error) {
	return AssessProviderTaxProfileAt(ctx, db, tenant, clientIP, billingtax.Recurring, time.Now().UTC())
}

func AssessStripeTaxProfile(ctx context.Context, db *sql.DB, tenant, clientIP string) (*billingstripe.TaxProfile, error) {
	return AssessProviderTaxProfileAt(ctx, db, tenant, clientIP, billingtax.Checkout, time.Now().UTC())
}

func AssessProviderTaxProfileAt(ctx context.Context, db *sql.DB, tenant, clientIP string, purpose billingtax.Purpose, at time.Time) (*billingstripe.TaxProfile, error) {
	rt := appconfig.Runtime()
	if DocumentSupplierSnapshot() == "" {
		return nil, &supplierTaxConfigurationError{}
	}
	profile, err := loadSavedBillingProfile(ctx, db, tenant)
	if err != nil {
		return nil, err
	}
	profile.Name, profile.Company, profile.Email, profile.VATNumber = strings.TrimSpace(profile.Name), strings.TrimSpace(profile.Company), strings.TrimSpace(profile.Email), strings.TrimSpace(profile.VATNumber)
	profile.CustomerType = profile.EffectiveCustomerType()
	if profile.CustomerType == "" {
		profile.CustomerType = "consumer"
	}
	if purpose != billingtax.Recurring && !profile.Complete() {
		return nil, billingtax.Review("billing_profile_required", "Complete the saved billing name, email and address before paying")
	}
	d, err := (&taxAssessor{db: db, supplierCountry: rt.SupplierCountry}).assess(ctx, tenant, profile, clientIP, "", purpose, at)
	if err != nil {
		return nil, err
	}
	profile.TaxAssessment = &TaxAssessment{CustomerType: profile.CustomerType, PolicyVersion: invoiceTaxPolicyVersion, AssessedAt: at, SupplierCountry: rt.SupplierCountry, Supplier: ConfiguredDocumentSupplier(), Decision: d, VATValidation: d.Verification.Snapshot, VATValidationMode: d.Verification.Mode, ReviewRequired: d.ReviewRequired, ReviewReasons: d.ReviewReasons}
	if d.ReviewRequired {
		return retainRecurringDomesticTaxProfile(ctx, db, tenant, profile, d.ReviewReasons)
	}
	address, err := parseBillingAddress(profile.Address)
	if err != nil {
		return nil, err
	}
	vat := profile.VATNumber
	if vat != "" && billingtax.IsEU(address.Country) {
		vat, err = billingtax.CanonicalVATNumber(address.Country, vat)
		if err != nil {
			return nil, billingtax.Review("vat_number_invalid", "Check the VAT number against the billing country")
		}
	}
	evidence, err := json.Marshal(profile.TaxAssessment)
	if err != nil {
		return nil, err
	}
	p := billingstripe.TaxProfile{Name: profile.Name, Company: profile.Company, Email: profile.Email, Street: address.Street, City: address.City, PostalCode: address.PostalCode, Country: address.Country, VATNumber: vat, RateBPS: d.RateBPS, ReverseCharge: d.ReverseCharge, Wording: d.Wording, SupplierCountry: rt.SupplierCountry, SupplierIdentity: billingstripe.SupplierTaxIdentity(rt.SupplierName, rt.SupplierAddress, rt.SupplierVATNumber, rt.SupplierRegistrationNumber), Evidence: evidence}
	p, err = normalizeProviderTaxProfile(p)
	if err != nil {
		return nil, billingtax.Review("vat_number_invalid", "Check the VAT number against the billing country")
	}
	return retainProviderTaxProfile(ctx, db, tenant, &p)
}

func retainProviderTaxProfile(ctx context.Context, db *sql.DB, tenant string, p *billingstripe.TaxProfile) (*billingstripe.TaxProfile, error) {
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

func retainRecurringDomesticTaxProfile(ctx context.Context, db *sql.DB, tenant string, claimed CryptoBillingProfile, reasons []string) (*billingstripe.TaxProfile, error) {
	rt := appconfig.Runtime()
	supplierCountry := countries.Normalize(rt.SupplierCountry)
	if supplierCountry != "NL" {
		return nil, billingtax.Review("billing_profile_required", "Complete your saved billing details before recurring tax can be assessed")
	}
	if claimed.TaxAssessment == nil {
		return nil, errors.New("retained recurring assessment required")
	}
	at := claimed.TaxAssessment.AssessedAt
	decision := claimed.TaxAssessment.Decision
	if decision.RateBPS <= 0 || decision.ReverseCharge {
		return nil, errors.New("positive domestic fallback assessment required")
	}
	claimed.TaxAssessment = nil
	rawClaim, err := json.Marshal(claimed)
	if err != nil {
		return nil, err
	}
	address, addressErr := parseBillingAddress(claimed.Address)
	if addressErr != nil {
		address = billingAddress{}
	}
	country := countries.Normalize(address.Country)
	if !countries.IsValid(country) {
		country = ""
	}
	proof := CryptoTaxAssessment{ReviewRequired: true, ReviewReasons: reasons, ClaimedProfile: rawClaim, CustomerType: claimed.EffectiveCustomerType(), PolicyVersion: invoiceTaxPolicyVersion, AssessedAt: at, SupplierCountry: supplierCountry, Supplier: ConfiguredDocumentSupplier(), Decision: decision}
	evidence, err := json.Marshal(proof)
	if err != nil {
		return nil, err
	}
	p := &billingstripe.TaxProfile{Name: strings.TrimSpace(claimed.Name), Company: strings.TrimSpace(claimed.Company), Email: strings.TrimSpace(claimed.Email), Street: strings.TrimSpace(address.Street), City: strings.TrimSpace(address.City), PostalCode: strings.TrimSpace(address.PostalCode), Country: country, RateBPS: decision.RateBPS, Wording: proof.Decision.Wording, SupplierCountry: supplierCountry, SupplierIdentity: billingstripe.SupplierTaxIdentity(rt.SupplierName, rt.SupplierAddress, rt.SupplierVATNumber, rt.SupplierRegistrationNumber), IncompleteProfile: true, Evidence: evidence}
	return retainProviderTaxProfile(ctx, db, tenant, p)
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
	assessed, err := AssessRecurringProviderTaxProfile(ctx, jm.db, tenant, "")
	if err != nil {
		var review *billingtax.ReviewError
		if !errors.As(err, &review) || billingtax.IsTransient(err) {
			return err
		}
		failure := err
		for _, sub := range subscriptions {
			proof, marshalErr := json.Marshal(map[string]any{"review_required": true, "reason": err.Error(), "customer_id": sub.CustomerID, "subscription_id": sub.SubscriptionID})
			if marshalErr != nil {
				failure = errors.Join(failure, marshalErr)
				continue
			}
			refType, refID := "stripe_subscription", sub.SubscriptionID
			if refID == "" {
				refType, refID = "stripe_customer", sub.CustomerID
			}
			recordErr := purserdb.New(jm.db).RecordBillingTaxReview(ctx, purserdb.RecordBillingTaxReviewParams{TenantID: tenant, Kind: "provider_tax_review", Network: "stripe", ReferenceType: refType, ReferenceID: refID, Detail: "Recurring tax assessment requires billing review", EvidenceJson: proof})
			failure = errors.Join(failure, recordErr)

		}
		return failure
	}
	if err = RetainProviderTaxExpectation(ctx, jm.db, tenant, assessed); err != nil {
		return err
	}
	var failures error
	for _, subscription := range subscriptions {
		if subscription.SubscriptionID != "" {
			err = jm.billing.stripeClient.SyncSubscriptionTax(ctx, tenant, subscription.CustomerID, subscription.SubscriptionID, *assessed)
		} else {
			_, err = jm.billing.stripeClient.PrepareCustomerTax(ctx, tenant, subscription.CustomerID, *assessed)
		}
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		refType, refID := "stripe_subscription", subscription.SubscriptionID
		if refID == "" {
			refType, refID = "stripe_customer", subscription.CustomerID
		}
		if assessed.IncompleteProfile {
			proof, encodeErr := recurringTaxReviewEvidence(assessed, subscription.CustomerID, subscription.SubscriptionID)
			if encodeErr != nil {
				failures = errors.Join(failures, encodeErr)
				continue
			}
			failures = errors.Join(failures, purserdb.New(jm.db).RecordBillingTaxReview(ctx, purserdb.RecordBillingTaxReviewParams{TenantID: tenant, Kind: "provider_tax_review", Network: "stripe", ReferenceType: refType, ReferenceID: refID, Detail: "Recurring invoices use domestic VAT while billing tax claims require review", EvidenceJson: proof}))
			continue
		}
		if _, err = purserdb.New(jm.db).ResolveProviderTaxReview(ctx, purserdb.ResolveProviderTaxReviewParams{TenantID: tenant, ReferenceType: refType, ReferenceID: refID, Note: "Current legal billing profile and exclusive tax terms synchronized"}); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}
