package tax

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

type CustomerType string

const (
	Consumer      CustomerType = "consumer"
	Business      CustomerType = "business"
	PolicyVersion              = "digital-services-v1"
)

type ConsumerMode string

const (
	Destination ConsumerMode = "destination"
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
	TaxLabel          string
	Wording           string
	Rate              Rate
	EvidenceReference string
}

type Policy struct {
	SupplierCountry                  string
	EUConsumerMode                   ConsumerMode
	DestinationRegistrationConfirmed bool
	NonEUConsumers                   map[string]Jurisdiction
	NonEUBusinesses                  map[string]Jurisdiction
}

type Decision struct {
	TaxLabel      string       `json:"tax_label,omitempty"`
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
			vat, vatErr := CanonicalVATNumber(profile.Country, profile.VATNumber)
			if vatErr != nil {
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
		if policy.EUConsumerMode != Destination {
			return d, Review("consumer_vat_regime_required", "configure the supplier's cross-border consumer VAT regime")
		}

		if !policy.DestinationRegistrationConfirmed {
			return d, Review("destination_registration_required", "confirm OSS or destination VAT registration before collection")
		}
		return load(profile.Country, "eu_b2c_destination")
	}
	return applyJurisdiction(d, policy.NonEUConsumers[profile.Country], "non_eu_b2c")
}

// NormalizeVATCountry accepts the VAT alias EL at validation boundaries while
// billing addresses continue to use ISO GR.
func NormalizeVATCountry(country string) string {
	country = countries.Normalize(country)
	if country == "EL" {
		return "GR"
	}
	return country
}

// VIESCountryCode maps the address country's ISO code to the VAT identifier.
func VIESCountryCode(country string) string {
	if NormalizeVATCountry(country) == "GR" {
		return "EL"
	}
	return NormalizeVATCountry(country)
}

// CanonicalVATNumber accepts a national number in a known billing country and
// both Greek prefixes. A different member state's explicit prefix is a conflict.
func CanonicalVATNumber(country, raw string) (string, error) {
	country = NormalizeVATCountry(country)
	prefix := VIESCountryCode(country)
	vat := strings.ToUpper(strings.Join(strings.Fields(raw), ""))
	if !IsEU(country) || len(vat) < 3 {
		return "", errors.New("an EU country and VAT number are required")
	}
	// The French two-character national key can itself spell an EU country
	// code. Shape and billing country disambiguate it; VIES decides validity.
	// https://entreprise.api.gouv.fr/catalogue/dgfip/numero_tva
	if country == "FR" && isFrenchNationalVATNumber(vat) {
		return prefix + vat, nil
	}
	if strings.HasPrefix(vat, prefix) {
		vat = strings.TrimPrefix(vat, prefix)
	} else if country == "GR" && strings.HasPrefix(vat, "GR") {
		vat = strings.TrimPrefix(vat, "GR")
	} else if IsEU(vat[:2]) || vat[:2] == "EL" {
		return "", errors.New("VAT prefix conflicts with billing country")
	}
	if len(vat) < 3 || len(vat) > 14 {
		return "", errors.New("invalid national VAT number length")
	}
	for _, c := range vat {
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return "", errors.New("VAT number must be alphanumeric")
		}
	}
	return prefix + vat, nil
}

func isFrenchNationalVATNumber(vat string) bool {
	if len(vat) != 11 {
		return false
	}
	for i, c := range vat {
		if i < 2 {
			if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
				return false
			}
		} else if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func applyJurisdiction(d Decision, rule Jurisdiction, treatment string) (Decision, error) {
	if strings.TrimSpace(rule.EvidenceReference) == "" || strings.TrimSpace(rule.Rate.Source) == "" || rule.Rate.BPS < 0 || rule.Rate.BPS > 10000 {
		return d, Review("foreign_tax_policy_required", "this destination needs a reviewed tax and registration policy")
	}
	d.Treatment, d.Rate = treatment, rule.Rate
	d.TaxLabel, d.Wording = rule.TaxLabel, rule.Wording
	return d, nil
}

func validateConsumerEvidence(profile Profile) error {
	sources := map[string]bool{}
	for _, e := range profile.Evidence {
		// A review of tax liability does not locate this particular customer.
		if e.Source != "billing_address" && e.Source != "ip_country" && e.Source != "payment_provider" {
			return Review("consumer_location_source_unsupported", "Location verification is unavailable for this evidence source; contact support before paying")
		}
		if strings.TrimSpace(e.Reference) == "" {
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
