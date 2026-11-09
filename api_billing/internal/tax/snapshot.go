package tax

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

type Supplier struct {
	Name               string `json:"name"`
	Address            string `json:"address"`
	VATNumber          string `json:"vat_number"`
	RegistrationNumber string `json:"registration_number"`
}

func (s Supplier) Complete() bool {
	for _, value := range []string{s.Name, s.Address, s.VATNumber, s.RegistrationNumber} {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

type BillingProfile struct {
	TaxAssessment *Assessment     `json:"tax_assessment,omitempty"`
	CustomerType  string          `json:"customer_type,omitempty"`
	Email         string          `json:"email,omitempty"`
	Name          string          `json:"name,omitempty"`
	Company       string          `json:"company,omitempty"`
	VATNumber     string          `json:"vat_number,omitempty"`
	Address       json.RawMessage `json:"address,omitempty"`
}

func (p BillingProfile) EffectiveCustomerType() string {
	switch p.CustomerType {
	case "consumer", "business":
		return p.CustomerType
	case "", "unspecified":
		if strings.TrimSpace(p.VATNumber) != "" {
			return "business"
		}
	}
	return ""
}

func (p BillingProfile) Complete() bool {
	var address struct {
		Street     string `json:"street"`
		City       string `json:"city"`
		PostalCode string `json:"postal_code"`
		Country    string `json:"country"`
	}
	return strings.TrimSpace(p.Name) != "" && strings.TrimSpace(p.Email) != "" && json.Unmarshal(p.Address, &address) == nil && strings.TrimSpace(address.Street) != "" && strings.TrimSpace(address.City) != "" && strings.TrimSpace(address.PostalCode) != "" && countries.IsValid(address.Country)
}

type AdvanceMoney struct {
	NetOriginal   int64  `json:"net_original_minor"`
	VATOriginal   int64  `json:"vat_original_minor"`
	GrossOriginal int64  `json:"gross_original_minor"`
	NetEUR        int64  `json:"net_eur_cents"`
	VATEUR        int64  `json:"vat_eur_cents"`
	GrossEUR      int64  `json:"gross_eur_cents"`
	Currency      string `json:"currency"`
	RateBPS       int    `json:"rate_bps"`
}

type VATVerification struct {
	Valid    bool
	Snapshot json.RawMessage
	Mode     string
}

// RetainedDecision preserves the issued JSON shape independently of the
// collection provider. Verification is a live assessment detail, not a claim
// that can replace the immutable response recorded on Assessment.
type RetainedDecision struct {
	ReviewRequired bool            `json:"review_required,omitempty"`
	ReviewReasons  []string        `json:"review_reasons,omitempty"`
	Verification   VATVerification `json:"-"`
	TaxLabel       string          `json:"tax_label,omitempty"`
	Wording        string          `json:"wording,omitempty"`
	Treatment      string          `json:"treatment,omitempty"`
	PolicyEvidence []Evidence      `json:"policy_evidence,omitempty"`
	Evidence       []Evidence      `json:"evidence,omitempty"`
	RateBPS        int
	Country        string
	VIESValidated  bool
	ReverseCharge  bool
	Source         string
	CheckedOn      string
	EffectiveFrom  string
}

type Assessment struct {
	ExpectedTaxAssessment json.RawMessage  `json:"expected_tax_assessment,omitempty"`
	ReviewRequired        bool             `json:"review_required,omitempty"`
	ReviewReasons         []string         `json:"review_reasons,omitempty"`
	ClaimedProfile        json.RawMessage  `json:"claimed_profile,omitempty"`
	CustomerType          string           `json:"customer_type,omitempty"`
	Advance               *AdvanceMoney    `json:"advance_money,omitempty"`
	VATValidationMode     string           `json:"vat_validation_mode,omitempty"`
	VATValidation         json.RawMessage  `json:"vat_validation,omitempty"`
	PolicyVersion         string           `json:"policy_version"`
	AssessedAt            time.Time        `json:"assessed_at"`
	SupplierCountry       string           `json:"supplier_country"`
	Supplier              Supplier         `json:"supplier"`
	Decision              RetainedDecision `json:"decision"`
	IPCountry             string           `json:"ip_country,omitempty"`
}

func (a *Assessment) ValidatedDecision() (RetainedDecision, error) {
	if a == nil || (a.PolicyVersion != billing.CurrentCryptoTopupTaxPolicy && a.PolicyVersion != billing.FrozenInclusiveCryptoTopupTaxPolicy && a.PolicyVersion != billing.CurrentInvoiceTaxPolicy) || a.AssessedAt.IsZero() || !countries.IsValid(a.SupplierCountry) || !countries.IsValid(a.Decision.Country) || a.Decision.RateBPS < 0 || a.Decision.RateBPS > 10000 || strings.TrimSpace(a.Decision.Source) == "" || a.Decision.ReverseCharge && (!a.Decision.VIESValidated || a.Decision.RateBPS != 0) {
		return RetainedDecision{}, fmt.Errorf("invalid retained tax assessment")
	}
	return a.Decision, nil
}

func (a *Assessment) QuoteExpiry(deadline, now time.Time) (time.Time, error) {
	if !deadline.After(now) {
		return time.Time{}, Review("tax_quote_expired", "The payment quote expired; request a new quote")
	}
	return deadline, nil
}
