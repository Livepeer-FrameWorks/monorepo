package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

type taxAssessor struct {
	db              purserdb.DBTX
	supplierCountry string
}

func (a *taxAssessor) loadEffectiveVATRate(ctx context.Context, country string, at time.Time) (vatDecision, error) {
	row, err := purserdb.New(a.db).GetEffectiveVATRate(ctx, purserdb.GetEffectiveVATRateParams{CountryCode: country, EffectiveAt: at})
	if err != nil {
		return vatDecision{}, fmt.Errorf("load effective VAT rate for %s: %w", country, err)
	}
	return vatDecision{Country: country, RateBPS: int(row.RateBps), Source: row.Source, CheckedOn: row.SourceCheckedOn, EffectiveFrom: row.EffectiveFrom}, nil
}

func (a *taxAssessor) assess(ctx context.Context, tenant string, profile BillingProfile, clientIP, ipCountry string, purpose billingtax.Purpose, at time.Time) (vatDecision, error) {
	if countries.Normalize(a.supplierCountry) != "NL" {
		return vatDecision{}, errors.New("the configured service tax policy requires a Dutch supplier")
	}
	address, addressErr := parseBillingAddress(profile.Address)
	if addressErr != nil {
		address = billingAddress{}
	}
	evidence := []billingtax.Evidence{{Source: "billing_address", Country: countries.Normalize(address.Country), Reference: "immutable-billing-profile"}}
	if ipCountry != "" {
		evidence = append(evidence, billingtax.Evidence{Source: "ip_country", Country: ipCountry, Reference: clientIP})
	}
	lookup := func(ctx context.Context, country string, at time.Time) (billingtax.Rate, error) {
		d, err := a.loadEffectiveVATRate(ctx, country, at)
		return billingtax.Rate{BPS: d.RateBPS, Source: d.Source, CheckedOn: d.CheckedOn, EffectiveFrom: d.EffectiveFrom}, err
	}
	var verification vatVerification
	validate := func(ctx context.Context, country, vat string) (bool, error) {
		observed, err := a.validateVIESVATEvidence(ctx, tenant, country, vat)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			var unavailable *viesUnavailableError
			if errors.As(err, &unavailable) {
				established := false
				if purpose == billingtax.Recurring {
					var establishedErr error
					established, establishedErr = unchangedRecurringReverseCharge(ctx, a.db, tenant, profile, at)
					if establishedErr != nil {
						return false, establishedErr
					}
				}
				return false, &billingtax.ValidationUnavailable{Cause: err, EstablishedClaim: established}
			}
			var claim *billingtax.ReviewError
			if errors.As(err, &claim) && claim.Code == "vat_number_invalid" {
				return false, nil
			}
			return false, err
		}
		verification = observed
		return observed.Valid, nil
	}
	typeOfCustomer := profile.EffectiveCustomerType()
	if typeOfCustomer == "" && purpose != billingtax.Checkout {
		typeOfCustomer = "consumer"
	}
	d, err := billingtax.Assess(ctx, billingtax.AssessmentRequest{Purpose: purpose, At: at, TenantID: tenant, Complete: profile.Complete(), Profile: billingtax.Profile{Type: billingtax.CustomerType(typeOfCustomer), Country: address.Country, VATNumber: profile.VATNumber, Evidence: evidence}}, lookup, validate)
	if err != nil {
		return vatDecision{}, err
	}
	result := vatDecision{Country: d.Country, RateBPS: d.Rate.BPS, Source: d.Rate.Source, CheckedOn: d.Rate.CheckedOn, EffectiveFrom: d.Rate.EffectiveFrom, Treatment: d.Treatment, TaxLabel: d.TaxLabel, Wording: d.Wording, ReverseCharge: d.ReverseCharge, VIESValidated: d.VATValidated, Evidence: d.Evidence, Verification: verification, ReviewRequired: d.ReviewRequired, ReviewReasons: d.ReviewReasons}
	return result, nil
}

func (h *X402Handler) loadEffectiveVATRate(ctx context.Context, country string, at time.Time) (vatDecision, error) {
	return (&taxAssessor{db: h.taxDatabase(), supplierCountry: h.supplierCountry}).loadEffectiveVATRate(ctx, country, at)
}

func loadSavedBillingProfile(ctx context.Context, db purserdb.DBTX, tenant string) (BillingProfile, error) {
	row, err := purserdb.New(db).GetCryptoDocumentBillingProfile(ctx, tenant)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BillingProfile{}, err
	}
	return BillingProfile{CustomerType: row.CustomerType, Name: row.BillingName.String, Company: row.BillingCompany.String, Email: row.BillingEmail.String, VATNumber: row.TaxID.String, Address: row.BillingAddress}, nil
}
