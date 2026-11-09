package tax

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

type Purpose string

const (
	Checkout   Purpose = "checkout"
	Recurring  Purpose = "recurring"
	Invoice    Purpose = "invoice"
	Historical Purpose = "historical"
)

// AssessmentRequest fixes the time and workflow before any rate or evidence
// lookup. Historical requests consume retained proof; they never ask VIES now.
type AssessmentRequest struct {
	Purpose  Purpose
	At       time.Time
	TenantID string
	Profile  Profile
	Complete bool
	Retained *Assessment
}

type ValidationUnavailable struct {
	Cause            error
	EstablishedClaim bool
}

func (e *ValidationUnavailable) Error() string { return "VAT verification is temporarily unavailable" }
func (e *ValidationUnavailable) Unwrap() error { return e.Cause }

// Assess applies the selected Dutch supplier service policy. Only recurring
// collections replace an unproven claim with domestic VAT and a visible review;
// purchases and ordinary invoice issuance retain their explicit validation error.
func Assess(ctx context.Context, request AssessmentRequest, lookup RateLookup, validate VATValidator) (Decision, error) {
	if request.At.IsZero() {
		return Decision{}, errors.New("tax assessment requires an explicit time")
	}
	if request.Purpose == Historical {
		return historicalDecision(request)
	}
	if request.Purpose != Checkout && request.Purpose != Recurring && request.Purpose != Invoice {
		return Decision{}, errors.New("tax assessment purpose is required")
	}
	p := request.Profile
	p.Country = countries.Normalize(p.Country)
	d := Decision{PolicyVersion: PolicyVersion, CustomerType: p.Type, Country: p.Country, Evidence: p.Evidence, TaxLabel: "VAT"}
	domestic := func(treatment string) (Decision, error) {
		if lookup == nil {
			return d, errors.New("effective Dutch VAT rate lookup is required")
		}
		rate, err := lookup(ctx, "NL", request.At)
		if err != nil {
			return d, fmt.Errorf("load effective Dutch VAT rate: %w", err)
		}
		if rate.BPS <= 0 || rate.BPS > 10000 || strings.TrimSpace(rate.Source) == "" {
			return d, errors.New("positive authoritative Dutch VAT rate required")
		}
		d.Country, d.Treatment, d.Rate = "NL", treatment, rate
		return d, nil
	}
	claim := func(code, message string) (Decision, error) {
		if request.Purpose != Recurring {
			return d, Review(code, message)
		}
		result, err := domestic("recurring_claim_domestic_vat")
		result.ReviewRequired = true
		result.ReviewReasons = []string{code}
		result.Wording = "Dutch VAT applies while the billing tax claim requires review"
		return result, err
	}
	if request.Purpose == Recurring && !request.Complete {
		return claim("billing_profile_incomplete_domestic_vat", "Complete the saved billing details")
	}
	if !countries.IsValid(p.Country) {
		return claim("billing_country_required", "Confirm a valid billing country")
	}
	if p.Type != Consumer && p.Type != Business {
		return claim("customer_type_required", "Select personal or business billing")
	}
	if p.Type == Consumer && strings.TrimSpace(p.VATNumber) != "" {
		return claim("consumer_vat_claim", "Use business billing to claim a business VAT treatment")
	}
	if strings.TrimSpace(p.VATNumber) != "" && IsEU(p.Country) {
		if _, err := CanonicalVATNumber(p.Country, p.VATNumber); err != nil {
			return claim("vat_number_invalid", "Check the VAT number against the billing country")
		}
	}
	if p.Country == "NL" {
		return domestic("domestic_vat")
	}
	if !IsEU(p.Country) {
		d.Treatment = "non_eu_service"
		d.Wording = "Outside scope of Dutch VAT"
		d.Rate = Rate{Source: "Dutch service place-of-supply policy", CheckedOn: request.At.Format(time.DateOnly), EffectiveFrom: request.At.Format(time.DateOnly)}
		return d, nil
	}
	if p.Type == Consumer {
		return domestic("eu_b2c_home")
	}
	vat, err := CanonicalVATNumber(p.Country, p.VATNumber)
	if err != nil {
		return claim("vat_number_required", "Enter a VAT number matching the business billing country")
	}
	if validate == nil {
		return d, errors.New("VAT validator is required")
	}
	valid, err := validate(ctx, p.Country, vat)
	if err != nil {
		var unavailable *ValidationUnavailable
		if errors.As(err, &unavailable) {
			if unavailable.EstablishedClaim {
				return d, Review("vat_validation_unavailable", "Retry the unchanged accepted VAT treatment when verification is available")
			}
			return claim("vat_validation_unavailable", "VAT verification is temporarily unavailable")
		}
		return d, err
	}
	if !valid {
		return claim("vat_number_invalid", "The business VAT number could not be validated")
	}
	d.Treatment, d.VATValidated, d.ReverseCharge, d.Wording = "eu_b2b_reverse_charge", true, true, "VAT reverse charged / btw verlegd"
	d.Rate = Rate{Source: "VIES validated cross-border EU business", CheckedOn: request.At.Format(time.DateOnly), EffectiveFrom: request.At.Format(time.DateOnly)}
	return d, nil
}

func historicalDecision(request AssessmentRequest) (Decision, error) {
	a := request.Retained
	d, err := a.ValidatedDecision()
	if err != nil {
		return Decision{}, Review("retained_tax_evidence_invalid", "Retain the accepted amount and review its missing tax evidence")
	}
	if a.AssessedAt.After(request.At) {
		return Decision{}, Review("tax_evidence_after_tax_point", "Tax evidence was obtained after the payment")
	}
	if d.ReverseCharge && !VATValidationCovers(a.VATValidation, request.TenantID, request.Profile.Country, request.Profile.VATNumber, request.At) {
		return Decision{}, Review("vat_evidence_missing_at_tax_point", "Reverse-charge evidence does not cover the payment time")
	}
	return Decision{PolicyVersion: PolicyVersion, CustomerType: CustomerType(a.CustomerType), Country: d.Country, Treatment: d.Treatment, Rate: Rate{BPS: d.RateBPS, Source: d.Source, CheckedOn: d.CheckedOn, EffectiveFrom: d.EffectiveFrom}, VATValidated: d.VIESValidated, ReverseCharge: d.ReverseCharge, Wording: d.Wording, TaxLabel: d.TaxLabel, Evidence: d.Evidence, ReviewRequired: a.ReviewRequired, ReviewReasons: append([]string(nil), a.ReviewReasons...)}, nil
}

func VATValidationCovers(raw json.RawMessage, tenant, country, vat string, at time.Time) bool {
	var proof struct {
		Tenant  string    `json:"tenant_id"`
		Country string    `json:"country_code"`
		Hash    string    `json:"vat_number_hash"`
		Valid   bool      `json:"valid"`
		Checked time.Time `json:"checked_at"`
		Expires time.Time `json:"expires_at"`
	}
	if json.Unmarshal(raw, &proof) != nil || !proof.Valid || proof.Tenant != tenant || proof.Checked.IsZero() || proof.Checked.After(at) || !at.Before(proof.Expires) || NormalizeVATCountry(proof.Country) != NormalizeVATCountry(country) {
		return false
	}
	canonical, err := CanonicalVATNumber(country, vat)
	if err != nil {
		return false
	}
	hash := sha256.Sum256([]byte(canonical))
	return proof.Hash == hex.EncodeToString(hash[:])
}
