package tax

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
	"github.com/shopspring/decimal"
)

type CustomerType string

const (
	Consumer                  CustomerType = "consumer"
	Business                  CustomerType = "business"
	PolicyVersion                          = "digital-services-v1"
	CrossBorderThresholdCents int64        = 1_000_000
)

type ConsumerMode string

const (
	Destination     ConsumerMode = "destination"
	OriginThreshold ConsumerMode = "origin_threshold"
)

type Evidence struct {
	Source    string
	Country   string
	Reference string
}

type Profile struct {
	Type                      CustomerType
	Country                   string
	VATNumber                 string
	BusinessEvidenceReference string
	Evidence                  []Evidence
}

type Rate struct {
	BPS           int
	Source        string
	CheckedOn     string
	EffectiveFrom string
}

type Jurisdiction struct {
	Rate              Rate
	EvidenceReference string
}

type Policy struct {
	SupplierCountry                  string
	EUConsumerMode                   ConsumerMode
	DestinationRegistrationConfirmed bool
	SingleEUEstablishment            bool
	ThresholdTotalsKnown             bool
	PreviousYearCrossBorderNetCents  int64
	CurrentYearCrossBorderNetCents   int64
	NonEUConsumers                   map[string]Jurisdiction
	NonEUBusinesses                  map[string]Jurisdiction
}

type Decision struct {
	PolicyVersion string       `json:"policy_version"`
	CustomerType  CustomerType `json:"customer_type"`
	Country       string       `json:"country"`
	Treatment     string       `json:"treatment"`
	Rate          Rate         `json:"rate"`
	VATValidated  bool         `json:"vat_validated"`
	ReverseCharge bool         `json:"reverse_charge"`
	Wording       string       `json:"wording,omitempty"`
	Evidence      []Evidence   `json:"evidence,omitempty"`
}

type ReviewError struct {
	Code    string
	Message string
}

func (e *ReviewError) Error() string { return e.Code + ": " + e.Message }

func Review(code, message string) error { return &ReviewError{Code: code, Message: message} }

type RateLookup func(context.Context, string, time.Time) (Rate, error)
type VATValidator func(context.Context, string, string) (bool, error)

// Evaluate determines tax independently of the payment provider. Country claims
// cannot establish business status, foreign registration, or reverse charge.
func Evaluate(ctx context.Context, policy Policy, profile Profile, netCents int64, at time.Time, lookup RateLookup, validate VATValidator) (Decision, error) {
	profile.Country = countries.Normalize(profile.Country)
	policy.SupplierCountry = countries.Normalize(policy.SupplierCountry)
	d := Decision{PolicyVersion: PolicyVersion, CustomerType: profile.Type, Country: profile.Country, Evidence: profile.Evidence}
	if netCents < 0 || at.IsZero() || !countries.IsValid(policy.SupplierCountry) {
		return d, errors.New("valid supplier, tax point and nonnegative net amount required")
	}
	if !countries.IsValid(profile.Country) {
		return d, Review("billing_country_required", "confirm the customer's billing country")
	}
	if profile.Type != Consumer && profile.Type != Business {
		return d, Review("customer_type_required", "select personal or business billing")
	}
	load := func(country, treatment string) (Decision, error) {
		if lookup == nil {
			return d, errors.New("tax rate lookup required")
		}
		rate, err := lookup(ctx, country, at)
		if err != nil {
			return d, fmt.Errorf("load effective tax rate: %w", err)
		}
		if rate.BPS < 0 || rate.BPS > 10000 || strings.TrimSpace(rate.Source) == "" {
			return d, errors.New("invalid effective tax rate")
		}
		d.Rate, d.Country, d.Treatment = rate, country, treatment
		return d, nil
	}
	if profile.Type == Consumer && strings.TrimSpace(profile.VATNumber) != "" {
		return d, Review("consumer_vat_claim", "use business billing to claim a business VAT treatment")
	}
	if profile.Country == policy.SupplierCountry {
		return load(policy.SupplierCountry, "domestic_vat")
	}
	if profile.Type == Business {
		if IsEU(profile.Country) && IsEU(policy.SupplierCountry) {
			prefix := profile.Country
			if prefix == "GR" {
				prefix = "EL"
			}
			vat := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(profile.VATNumber), " ", ""))
			if !strings.HasPrefix(vat, prefix) || len(vat) <= 2 {
				return d, Review("vat_number_required", "enter a VAT number matching the business billing country")
			}
			if validate == nil {
				return d, Review("vat_validation_unavailable", "VAT validation is temporarily unavailable")
			}
			valid, err := validate(ctx, profile.Country, vat)
			if err != nil {
				return d, Review("vat_validation_unavailable", "VAT validation could not be completed; retry without changing the tax claim")
			}
			if !valid {
				return d, Review("vat_number_invalid", "the business VAT number could not be validated")
			}
			d.Treatment, d.VATValidated, d.ReverseCharge, d.Wording = "eu_b2b_reverse_charge", true, true, "VAT reverse charged / btw verlegd"
			d.Rate.Source = "VIES validated cross-border EU business"
			return d, nil
		}
		if strings.TrimSpace(profile.BusinessEvidenceReference) == "" {
			return d, Review("business_evidence_required", "business establishment evidence is required for this jurisdiction")
		}
		return applyJurisdiction(d, policy.NonEUBusinesses[profile.Country], "non_eu_b2b")
	}
	if err := validateConsumerEvidence(profile); err != nil {
		return d, err
	}
	if IsEU(profile.Country) {
		if policy.EUConsumerMode == OriginThreshold {
			if !IsEU(policy.SupplierCountry) || !policy.SingleEUEstablishment || !policy.ThresholdTotalsKnown || policy.PreviousYearCrossBorderNetCents < 0 || policy.CurrentYearCrossBorderNetCents < 0 {
				return d, Review("consumer_threshold_unproven", "the supplier-country consumer VAT exception needs verified establishment and annual totals")
			}
			if policy.PreviousYearCrossBorderNetCents <= CrossBorderThresholdCents && policy.CurrentYearCrossBorderNetCents <= CrossBorderThresholdCents && netCents <= CrossBorderThresholdCents-policy.CurrentYearCrossBorderNetCents {
				return load(policy.SupplierCountry, "eu_b2c_origin_threshold")
			}
		} else if policy.EUConsumerMode != Destination {
			return d, Review("consumer_vat_regime_required", "configure the supplier's cross-border consumer VAT regime")
		}
		if !policy.DestinationRegistrationConfirmed {
			return d, Review("destination_registration_required", "confirm OSS or destination VAT registration before collection")
		}
		return load(profile.Country, "eu_b2c_destination")
	}
	return applyJurisdiction(d, policy.NonEUConsumers[profile.Country], "non_eu_b2c")
}

func applyJurisdiction(d Decision, rule Jurisdiction, treatment string) (Decision, error) {
	if strings.TrimSpace(rule.EvidenceReference) == "" || strings.TrimSpace(rule.Rate.Source) == "" || rule.Rate.BPS < 0 || rule.Rate.BPS > 10000 {
		return d, Review("foreign_tax_policy_required", "this destination needs a reviewed tax and registration policy")
	}
	d.Treatment, d.Rate = treatment, rule.Rate
	return d, nil
}

func validateConsumerEvidence(profile Profile) error {
	sources := map[string]bool{}
	for _, e := range profile.Evidence {
		if strings.TrimSpace(e.Reference) == "" || strings.TrimSpace(e.Source) == "" {
			continue
		}
		if countries.Normalize(e.Country) != profile.Country {
			return Review("consumer_location_conflict", "confirm the consumer's country where location evidence conflicts")
		}
		sources[e.Source] = true
	}
	if len(sources) < 2 {
		return Review("consumer_location_evidence_required", "two consistent location evidence sources are required")
	}
	return nil
}

func IsEU(country string) bool {
	switch countries.Normalize(country) {
	case "AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR", "DE", "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL", "PL", "PT", "RO", "SK", "SI", "ES", "SE":
		return true
	default:
		return false
	}
}

type Totals struct {
	Net   int64 `json:"net_minor"`
	VAT   int64 `json:"vat_minor"`
	Gross int64 `json:"gross_minor"`
}

func Exclusive(net int64, rateBPS int) (Totals, error) {
	if net < 0 || rateBPS < 0 || rateBPS > 10000 {
		return Totals{}, errors.New("invalid net amount or tax rate")
	}
	vat := decimal.NewFromInt(net).Mul(decimal.NewFromInt(int64(rateBPS))).Div(decimal.NewFromInt(10000)).Round(0)
	if vat.GreaterThan(decimal.NewFromInt(math.MaxInt64 - net)) {
		return Totals{}, errors.New("gross amount exceeds supported money range")
	}
	return Totals{Net: net, VAT: vat.IntPart(), Gross: net + vat.IntPart()}, nil
}
