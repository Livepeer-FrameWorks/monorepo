package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	billingstripe "frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

func AssessMollieTaxTerms(ctx context.Context, db *sql.DB, tenant string, net int64) (*billingmollie.TaxTerms, error) {
	profile, err := AssessStripeTaxProfile(ctx, db, tenant, "")
	if err != nil {
		return nil, err
	}
	terms, err := billingmollie.NewTaxTerms(profile.ProfileSHA, net, profile.RateBPS)
	if err != nil {
		return nil, err
	}
	if err = RetainProviderTaxExpectation(ctx, db, tenant, profile); err != nil {
		return nil, err
	}
	return terms, nil
}

func mollieSubscriptionNet(tenant string, sub *mollieapi.Subscription) (int64, error) {
	if mollieMetadataString(sub.Metadata, "tenant_id") != tenant {
		return 0, errors.New("mollie subscription tax owner mismatch")
	}
	if sub.Amount == nil || sub.Amount.Currency != "EUR" {
		return 0, errors.New("native Mollie tax synchronization requires EUR")
	}
	if frozen := mollieMetadataString(sub.Metadata, "frameworks_net_minor"); frozen != "" {
		return strconv.ParseInt(frozen, 10, 64)
	}
	// Subscriptions created before frozen tax terms stored the net fee as amount.
	net, _, err := mollieAmountToCents(sub.Amount.Value, sub.Amount.Currency)
	return net, err
}

func (s *Service) syncMollieUpgradeTax(ctx context.Context, tenant, customer, subscription string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sub, err := s.mollieClient.GetSubscription(ctx, customer, subscription)
	if err != nil {
		return err
	}
	// Keep the existing net fee here: changing it to the requested tier fee would
	// make the later upgrade mistake the new fee for the previous paid basis.
	net, err := mollieSubscriptionNet(tenant, sub)
	if err != nil {
		return err
	}
	terms, err := AssessMollieTaxTerms(ctx, s.db, tenant, net)
	if err != nil {
		return err
	}
	return s.mollieClient.SyncSubscriptionTax(ctx, tenant, customer, subscription, *terms)
}

func (jm *JobManager) reconcileMollieTaxTenant(ctx context.Context, tenant string) error {
	if jm.billing == nil || jm.billing.mollieClient == nil {
		return nil
	}
	rows, err := purserdb.New(jm.db).ListTenantMollieTaxSubscriptions(ctx, tenant)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	untaxed, err := keepsUnassessedTax(ctx, jm.db, tenant)
	if err != nil || untaxed {
		return err
	}
	profile, err := AssessRecurringProviderTaxProfile(ctx, jm.db, tenant, "")
	if err != nil {
		return err
	}
	if err = RetainProviderTaxExpectation(ctx, jm.db, tenant, profile); err != nil {
		return err
	}
	for _, row := range rows {
		sub, err := jm.billing.mollieClient.GetSubscription(ctx, row.CustomerID, row.SubscriptionID)
		if err != nil {
			return err
		}
		net, err := mollieSubscriptionNet(tenant, sub)
		if err != nil {
			return err
		}
		terms, err := billingmollie.NewTaxTerms(profile.ProfileSHA, net, profile.RateBPS)
		if err != nil {
			return err
		}
		if profile.IncompleteProfile {
			evidence, marshalErr := recurringTaxReviewEvidence(profile, row.CustomerID, row.SubscriptionID)
			if marshalErr != nil {
				return marshalErr
			}
			if err = purserdb.New(jm.db).RecordBillingTaxReview(ctx, purserdb.RecordBillingTaxReviewParams{TenantID: tenant, Kind: "provider_tax_review", Network: "mollie", ReferenceType: "mollie_subscription", ReferenceID: row.SubscriptionID, Detail: "Recurring invoices use domestic VAT while billing tax claims require review", EvidenceJson: evidence}); err != nil {
				return err
			}
		}
		if err = jm.billing.mollieClient.SyncSubscriptionTax(ctx, tenant, row.CustomerID, row.SubscriptionID, *terms); err != nil {
			return err
		}
		if !profile.IncompleteProfile {
			if err = purserdb.New(jm.db).ResolveMollieSubscriptionTaxReview(ctx, purserdb.ResolveMollieSubscriptionTaxReviewParams{TenantID: tenant, SubscriptionID: row.SubscriptionID}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Native subscriptions collect their base fee outside the usage invoice. The
// payment carries the exact assessment and net price accepted before collection.
func (s *Service) issueMollieNativeTaxInvoice(ctx context.Context, tenant string, payment *mollieapi.Payment) error {
	sha := mollieMetadataString(payment.Metadata, "frameworks_tax_profile")
	if sha == "" {
		return nil
	}
	if payment.Amount == nil || payment.PaidAt == nil {
		return fmt.Errorf("settled Mollie subscription payment lacks amount or tax point")
	}
	netInput := mollieMetadataString(payment.Metadata, "frameworks_net_minor")
	net, netErr := strconv.ParseInt(netInput, 10, 64)
	acceptedNet := sql.NullInt64{Int64: net, Valid: netErr == nil && net > 0}
	gross, currency, err := mollieAmountToCents(payment.Amount.Value, payment.Amount.Currency)
	if err != nil {
		return err
	}
	if currency != "EUR" {
		return fmt.Errorf("native Mollie subscription tax invoice requires EUR")
	}
	q := purserdb.New(s.db)
	if _, existingErr := q.GetNativeMollieInvoiceID(ctx, purserdb.GetNativeMollieInvoiceIDParams{TenantID: tenant, PaymentID: sql.NullString{String: payment.ID, Valid: true}}); existingErr == nil {
		return nil
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return existingErr
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenant, subscriptions.PreservePricing); err != nil {
			return err
		}
		q := purserdb.New(tx)
		customer, err := q.GetMollieCustomerID(ctx, tenant)
		if err != nil {
			return err
		}
		if customer != payment.CustomerID {
			return fmt.Errorf("mollie tax payment customer owner mismatch")
		}
		frozen, err := q.GetProviderTaxProfile(ctx, purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: sha})
		acceptedReview := ""
		if errors.Is(err, sql.ErrNoRows) {
			frozen = json.RawMessage(`null`)
			acceptedReview = "accepted_tax_profile_missing"
		} else if err != nil {
			return err
		}
		var profile billingstripe.TaxProfile
		var proof CryptoTaxAssessment
		if acceptedReview == "" && (json.Unmarshal(frozen, &profile) != nil || json.Unmarshal(profile.Evidence, &proof) != nil) {
			acceptedReview = "accepted_tax_assessment_invalid"
		} else if acceptedReview == "" {
			if _, validationErr := proof.ValidatedDecision(); validationErr != nil {
				acceptedReview = "accepted_tax_assessment_invalid"
			} else if !proof.Supplier.Complete() {
				acceptedReview = "accepted_supplier_identity_incomplete"
			} else if profile.RateBPS != proof.Decision.RateBPS || profile.ReverseCharge != proof.Decision.ReverseCharge || profile.SupplierIdentity != billingstripe.SupplierTaxIdentity(proof.Supplier.Name, proof.Supplier.Address, proof.Supplier.VATNumber, proof.Supplier.RegistrationNumber) {
				acceptedReview = "accepted_tax_terms_mismatch"
			}
		}
		if acceptedReview == "" && !acceptedNet.Valid {
			acceptedReview = "accepted_net_amount_missing_or_invalid"
		}
		var vat int64
		var acceptedGross sql.NullInt64
		if acceptedReview == "" {
			var expected int64
			vat, expected = exclusiveTax(net, profile.RateBPS)
			acceptedGross = sql.NullInt64{Int64: expected, Valid: true}
			if expected != gross {
				acceptedReview = "accepted_tax_gross_mismatch"
			}
		}
		at := *payment.PaidAt
		row, err := q.InsertNativeMollieTaxInvoice(ctx, purserdb.InsertNativeMollieTaxInvoiceParams{TenantID: tenant, GrossMinor: gross, NetMinor: acceptedNet, TaxPoint: at, PaymentID: sql.NullString{String: payment.ID, Valid: true}})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if acceptedReview != "" {
			if err = retainNativeMollieTaxReviewTx(ctx, tx, tenant, row.ID, payment.ID, sha, profile, proof, frozen, netInput, acceptedNet, gross, acceptedGross, at, acceptedReview); err != nil {
				return err
			}
			return q.RecordNativeMollieInvoicePayment(ctx, purserdb.RecordNativeMollieInvoicePaymentParams{TenantID: tenant, InvoiceID: row.ID, GrossMinor: gross, PaymentID: sql.NullString{String: payment.ID, Valid: true}, PaidAt: sql.NullTime{Time: at, Valid: true}})
		}
		reviewReason := ""
		if profile.IncompleteProfile {
			reviewReason = "Recurring payment charged Dutch VAT while its billing tax claim requires review"
		}
		normalized, normalizationErr := normalizeProviderTaxProfile(profile)
		if normalizationErr == nil {
			profile = normalized
		} else {
			reviewReason = "Accepted payment legal profile requires review"
		}
		verified, reasons, evidenceErr := historicalProviderTaxEvidence(ctx, tx, tenant, profile, at)
		if evidenceErr != nil {
			return evidenceErr
		}
		profile = verified
		if len(reasons) > 0 {
			reviewReason = "Accepted payment evidence requires review at its tax point"
		}
		if err = json.Unmarshal(profile.Evidence, &proof); err != nil {
			return err
		}
		amount := decimal.New(net, -2)
		lines := &clusterRatingResult{BaseLine: pricedLine{PricingSource: pricing.SourceTier, LineItem: rating.LineItem{LineKey: "native_subscription_base", Description: "Monthly subscription", Currency: "EUR", Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1), UnitPrice: amount, Amount: amount}}}
		if err = persistInvoiceLineItems(ctx, tx, row.ID, tenant, lines); err != nil {
			return err
		}
		address, addressErr := json.Marshal(map[string]string{"street": profile.Street, "city": profile.City, "postal_code": profile.PostalCode, "country": profile.Country})
		if addressErr != nil {
			return addressErr
		}
		assessment := invoiceTaxAssessment{Net: net, VAT: vat, Gross: gross, TaxPoint: at, Decision: proof.Decision, Supplier: proof.Supplier, Validation: proof.VATValidation, ValidationMode: proof.VATValidationMode, Customer: CryptoBillingProfile{CustomerType: proof.CustomerType, Name: profile.Name, Company: profile.Company, Email: profile.Email, Address: address, VATNumber: profile.VATNumber}}
		assessment.ProviderAssessment = append(json.RawMessage(nil), profile.Evidence...)
		assessment.ReviewRequired = proof.ReviewRequired
		assessment.ReviewReasons = append([]string(nil), proof.ReviewReasons...)
		if normalizationErr != nil {
			assessment.ReviewReasons = append(assessment.ReviewReasons, "accepted_profile_invalid")
		}
		for _, reason := range reasons {
			if !slices.Contains(assessment.ReviewReasons, reason) {
				assessment.ReviewReasons = append(assessment.ReviewReasons, reason)
			}
		}
		if reviewReason != "" {
			assessment.ReviewRequired = true
			if profile.IncompleteProfile && len(assessment.ReviewReasons) == 0 {
				assessment.ReviewReasons = append(assessment.ReviewReasons, "billing_profile_incomplete_domestic_vat")
			}
		}
		if assessment.Supplier.Name == "" {
			return fmt.Errorf("mollie tax terms lack frozen supplier identity")
		}
		cash, err := fx.RecordFromEUR(gross, fx.Identity(at))
		if err != nil {
			return err
		}
		if err = freezeInvoiceTaxTx(ctx, tx, tenant, row.ID, assessment, cash); err != nil {
			return err
		}
		if err = q.RecordNativeMollieInvoicePayment(ctx, purserdb.RecordNativeMollieInvoicePaymentParams{TenantID: tenant, InvoiceID: row.ID, GrossMinor: gross, PaymentID: sql.NullString{String: payment.ID, Valid: true}, PaidAt: sql.NullTime{Time: at, Valid: true}}); err != nil {
			return err
		}
		if reviewReason != "" {
			evidence, marshalErr := json.Marshal(map[string]any{"review_required": true, "payment_id": payment.ID, "accepted_profile": sha, "reason": reviewReason, "review_reasons": assessment.ReviewReasons})
			if marshalErr != nil {
				return marshalErr
			}
			if err = q.RecordBillingTaxReview(ctx, purserdb.RecordBillingTaxReviewParams{TenantID: tenant, Kind: "provider_tax_document_review", Network: "mollie", ReferenceType: "invoice", ReferenceID: row.ID, AmountCents: gross, Detail: reviewReason, EvidenceJson: evidence}); err != nil {
				return err
			}
		}
		return enqueueInvoiceEmailTx(ctx, tx, row.ID, tenant, profile.Email, "paid")
	})
}

func mollieTaxProfileMatchesSavedBilling(profile billingstripe.TaxProfile, current purserdb.GetInvoiceTaxProfileRow) bool {
	var proof CryptoTaxAssessment
	if err := json.Unmarshal(profile.Evidence, &proof); err != nil {
		return false
	}
	effectiveType := (CryptoBillingProfile{CustomerType: current.CustomerType, VATNumber: current.VatNumber}).EffectiveCustomerType()
	if effectiveType == "" && (current.CustomerType == "" || current.CustomerType == "unspecified") {
		effectiveType = "consumer"
	}
	if proof.CustomerType == "" || proof.CustomerType != effectiveType {
		return false
	}
	address, err := parseBillingAddress(current.Address)
	if err != nil {
		return false
	}
	profile, err = normalizeProviderTaxProfile(profile)
	if err != nil {
		return false
	}
	saved, err := normalizeProviderTaxProfile(billingstripe.TaxProfile{Name: current.Name, Company: current.Company, Email: current.Email, Street: address.Street, City: address.City, PostalCode: address.PostalCode, Country: address.Country, VATNumber: current.VatNumber})
	if err != nil {
		return false
	}
	return profile.Name == saved.Name && profile.Company == saved.Company && profile.Email == saved.Email && profile.Street == saved.Street && profile.City == saved.City && profile.PostalCode == saved.PostalCode && profile.Country == saved.Country && profile.VATNumber == saved.VATNumber
}
