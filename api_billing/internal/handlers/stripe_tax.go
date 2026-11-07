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
	return assessStripeTaxProfile(ctx, db, tenant, clientIP, true)
}

func AssessStripeTaxProfile(ctx context.Context, db *sql.DB, tenant, clientIP string) (*billingstripe.TaxProfile, error) {
	return assessStripeTaxProfile(ctx, db, tenant, clientIP, false)
}

func assessStripeTaxProfile(ctx context.Context, db *sql.DB, tenant, clientIP string, recurring bool) (*billingstripe.TaxProfile, error) {
	rt := appconfig.Runtime()
	if DocumentSupplierSnapshot() == "" {
		return nil, &supplierTaxConfigurationError{}
	}
	h := &X402Handler{db: db, supplierCountry: rt.SupplierCountry, supplierName: rt.SupplierName, supplierAddress: rt.SupplierAddress, supplierVAT: rt.SupplierVATNumber, supplierRegistration: rt.SupplierRegistrationNumber}
	var requirement CryptoDocumentRequirement
	var err error
	if recurring {
		requirement, err = h.recurringTaxRequirement(ctx, tenant, clientIP)
	} else {
		requirement, err = h.GetCryptoDocumentRequirement(ctx, tenant, 1, clientIP)
	}
	if err != nil {
		return nil, err
	}
	profile := requirement.Profile
	if recurring && profile.TaxAssessment != nil && profile.TaxAssessment.ReviewRequired {
		return retainRecurringDomesticTaxProfile(ctx, db, tenant, profile, profile.TaxAssessment.ReviewReasons)
	}
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
	assessment.CustomerType = profile.effectiveCustomerType()
	evidence, err := json.Marshal(assessment)
	if err != nil {
		return nil, err
	}
	p := &billingstripe.TaxProfile{Name: profile.Name, Company: profile.Company, Email: profile.Email, Street: address.Street, City: address.City, PostalCode: address.PostalCode, Country: address.Country, VATNumber: vatNumber, RateBPS: d.RateBPS, ReverseCharge: d.ReverseCharge, Wording: d.Wording, SupplierCountry: rt.SupplierCountry, SupplierIdentity: billingstripe.SupplierTaxIdentity(rt.SupplierName, rt.SupplierAddress, rt.SupplierVATNumber, rt.SupplierRegistrationNumber), Evidence: evidence}
	p.PolicyReference, p.PolicyValidFrom, p.PolicyValidUntil = d.PolicyReference, d.PolicyValidFrom, d.PolicyValidUntil
	normalized, err := normalizeProviderTaxProfile(*p)
	if err != nil {
		return nil, billingtax.Review("vat_number_invalid", "Check the VAT number in your billing details")
	}
	p = &normalized
	if p.Name == "" || p.Email == "" || p.Street == "" || p.City == "" || p.PostalCode == "" {
		return nil, billingtax.Review("billing_profile_required", "Complete your saved billing details before paying")
	}
	p.ProfileSHA = p.Fingerprint()
	return retainProviderTaxProfile(ctx, db, tenant, p)
}

func retainProviderTaxProfile(ctx context.Context, db *sql.DB, tenant string, p *billingstripe.TaxProfile) (*billingstripe.TaxProfile, error) {
	if err := retainLegacyProviderTaxShape(ctx, db, tenant, p); err != nil {
		return nil, err
	}
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
	at := time.Now().UTC()
	decision, err := (&X402Handler{db: db}).loadEffectiveVATRate(ctx, supplierCountry, at)
	if err != nil {
		return nil, err
	}
	if decision.RateBPS <= 0 {
		return nil, errors.New("recurring domestic fallback requires a positive VAT rate")
	}
	decision.TaxLabel = "VAT"
	decision.Treatment = reasons[0]
	decision.Wording = "Dutch VAT applies while the billing tax claim requires review"
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
	proof := CryptoTaxAssessment{ReviewRequired: true, ReviewReasons: reasons, ClaimedProfile: rawClaim, CustomerType: claimed.effectiveCustomerType(), PolicyVersion: frozenCryptoTaxPolicy, AssessedAt: at, SupplierCountry: supplierCountry, Supplier: ConfiguredDocumentSupplier(), Decision: decision}
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
			if sub.SubscriptionID != "" && review.Code != "billing_profile_required" {
				failure = errors.Join(failure, jm.billing.stripeClient.HoldSubscriptionTaxReview(ctx, tenant, sub.CustomerID, sub.SubscriptionID))
			}
		}
		return failure
	}
	var failures error
	for _, subscription := range subscriptions {
		if subscription.SubscriptionID != "" {
			var report *billingstripe.TaxRecoveryReport
			report, err = jm.billing.stripeClient.ReconcileSubscriptionTax(ctx, tenant, subscription.CustomerID, subscription.SubscriptionID, *assessed)
			persisted := true
			if report != nil {
				for _, review := range report.Reviews {
					if recordErr := jm.recordStripeInvoiceTaxReview(ctx, tenant, review); recordErr != nil {
						err = errors.Join(err, recordErr)
						persisted = false
					}
				}
				if persisted {
					err = errors.Join(err, jm.billing.stripeClient.AckTaxRecoveryReport(ctx, report))
				}
			}
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
