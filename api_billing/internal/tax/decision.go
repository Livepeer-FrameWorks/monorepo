package tax

import (
	"context"
	"errors"
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

type Evidence struct {
	Source    string
	Country   string
	Reference string
}

type Profile struct {
	Type      CustomerType
	Country   string
	VATNumber string
	Evidence  []Evidence
}

type Rate struct {
	BPS           int
	Source        string
	CheckedOn     string
	EffectiveFrom string
}

type Decision struct {
	ReviewRequired bool         `json:"review_required,omitempty"`
	ReviewReasons  []string     `json:"review_reasons,omitempty"`
	TaxLabel       string       `json:"tax_label,omitempty"`
	PolicyVersion  string       `json:"policy_version"`
	CustomerType   CustomerType `json:"customer_type"`
	Country        string       `json:"country"`
	Treatment      string       `json:"treatment"`
	Rate           Rate         `json:"rate"`
	VATValidated   bool         `json:"vat_validated"`
	ReverseCharge  bool         `json:"reverse_charge"`
	Wording        string       `json:"wording,omitempty"`
	Evidence       []Evidence   `json:"evidence,omitempty"`
}

type ReviewError struct {
	Code    string
	Message string
}

func (e *ReviewError) Error() string { return e.Code + ": " + e.Message }

type TransientError struct{ Cause error }

func (e *TransientError) Error() string { return e.Cause.Error() }
func (e *TransientError) Unwrap() error { return e.Cause }

func IsTransient(err error) bool {
	var transient *TransientError
	return errors.As(err, &transient)
}

func Review(code, message string) error {
	err := &ReviewError{Code: code, Message: message}
	if code == "vat_validation_unavailable" || code == "business_evidence_unavailable" {
		return &TransientError{Cause: err}
	}
	return err
}

type RateLookup func(context.Context, string, time.Time) (Rate, error)
type VATValidator func(context.Context, string, string) (bool, error)

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

func IsEU(country string) bool {
	switch countries.Normalize(country) {
	case "AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR", "DE", "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL", "PL", "PT", "RO", "SK", "SI", "ES", "SE":
		return true
	default:
		return false
	}
}
