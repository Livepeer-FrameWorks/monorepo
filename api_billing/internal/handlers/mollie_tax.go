package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	billingstripe "frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/subscriptions"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

func AssessMollieTaxTerms(ctx context.Context, db *sql.DB, tenant string, net int64) (*billingmollie.TaxTerms, error) {
	profile, err := AssessStripeTaxProfile(ctx, db, tenant, "")
	if err != nil {
		return nil, err
	}
	return billingmollie.NewTaxTerms(profile.ProfileSHA, net, profile.RateBPS)
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
	for _, row := range rows {
		sub, err := jm.billing.mollieClient.GetSubscription(ctx, row.CustomerID, row.SubscriptionID)
		if err != nil {
			return err
		}
		net, err := mollieSubscriptionNet(tenant, sub)
		if err != nil {
			return err
		}
		terms, err := AssessMollieTaxTerms(ctx, jm.db, tenant, net)
		if err != nil {
			return err
		}
		if err = jm.billing.mollieClient.SyncSubscriptionTax(ctx, tenant, row.CustomerID, row.SubscriptionID, *terms); err != nil {
			return err
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
	net, err := strconv.ParseInt(mollieMetadataString(payment.Metadata, "frameworks_net_minor"), 10, 64)
	if err != nil {
		return err
	}
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
	// VIES and the assessment's database reads happen before taking the tenant
	// lock. The saved profile is compared again under that lock before use.
	before, beforeErr := q.GetInvoiceTaxProfile(ctx, tenant)
	fresh, freshErr := AssessStripeTaxProfile(ctx, s.db, tenant, "")
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
		if err != nil {
			return err
		}
		var profile billingstripe.TaxProfile
		if err = json.Unmarshal(frozen, &profile); err != nil {
			return err
		}
		var proof CryptoTaxAssessment
		if err = json.Unmarshal(profile.Evidence, &proof); err != nil {
			return err
		}
		vat, expected := exclusiveTax(net, profile.RateBPS)
		if expected != gross {
			return fmt.Errorf("mollie subscription payment differs from frozen gross tax terms")
		}
		at := *payment.PaidAt
		row, err := q.InsertNativeMollieTaxInvoice(ctx, purserdb.InsertNativeMollieTaxInvoiceParams{TenantID: tenant, GrossMinor: gross, NetMinor: net, TaxPoint: at, PaymentID: sql.NullString{String: payment.ID, Valid: true}})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		reviewReason := ""
		if freshErr != nil {
			reviewReason = "Current tax evidence could not be verified at issuance: " + freshErr.Error()
		} else if fresh.Fingerprint() != profile.Fingerprint() {
			reviewReason = "Accepted payment tax terms differ from the current legal billing profile"
		} else {
			current, currentErr := q.GetInvoiceTaxProfile(ctx, tenant)
			if currentErr != nil {
				return currentErr
			}
			if beforeErr != nil || before.CustomerType != current.CustomerType || !mollieTaxProfileMatchesSavedBilling(*fresh, current) {
				reviewReason = "Billing details changed while current tax evidence was assessed"
			} else {
				profile = *fresh
				if err = json.Unmarshal(profile.Evidence, &proof); err != nil {
					return err
				}
			}
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
		assessment := invoiceTaxAssessment{Net: net, VAT: vat, Gross: gross, TaxPoint: at, Decision: proof.Decision, Supplier: proof.Supplier, Validation: proof.VATValidation, ValidationMode: proof.VATValidationMode, Customer: CryptoBillingProfile{Name: profile.Name, Company: profile.Company, Email: profile.Email, Address: address, VATNumber: profile.VATNumber}}
		assessment.ProviderAssessment = append(json.RawMessage(nil), profile.Evidence...)
		if reviewReason != "" {
			assessment.ReviewRequired = true
			assessment.ReviewReasons = append(assessment.ReviewReasons, "provider_tax_terms_changed")
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
			evidence, marshalErr := json.Marshal(map[string]any{"review_required": true, "payment_id": payment.ID, "accepted_profile": sha, "reason": reviewReason})
			if marshalErr != nil {
				return marshalErr
			}
			if err = q.RecordCryptoAccountingAnomaly(ctx, purserdb.RecordCryptoAccountingAnomalyParams{TenantID: tenant, Kind: "provider_tax_document_review", Network: "mollie", ReferenceType: "invoice", ReferenceID: row.ID, AmountCents: gross, Detail: reviewReason, EvidenceJson: evidence}); err != nil {
				return err
			}
		}
		return enqueueInvoiceEmailTx(ctx, tx, row.ID, tenant, profile.Email, "paid")
	})
}

func mollieTaxProfileMatchesSavedBilling(profile billingstripe.TaxProfile, current purserdb.GetInvoiceTaxProfileRow) bool {
	address, err := parseBillingAddress(current.Address)
	if err != nil {
		return false
	}
	vat := current.VatNumber
	if vat != "" && billingtax.IsEU(address.Country) {
		vat, err = billingtax.CanonicalVATNumber(address.Country, vat)
		if err != nil {
			return false
		}
	}
	return profile.Name == current.Name && profile.Company == current.Company && profile.Email == current.Email && profile.Street == address.Street && profile.City == address.City && profile.PostalCode == address.PostalCode && profile.Country == address.Country && profile.VATNumber == vat
}
