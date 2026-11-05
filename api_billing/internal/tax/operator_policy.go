package tax

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
)

// OperatorJurisdiction is an explicit reviewed rate, including an explicit zero.
// It asserts that the referenced review covers the service and regional rules.
type OperatorJurisdiction struct {
	TaxLabel          string `json:"tax_label"`
	Wording           string `json:"wording"`
	RateBPS           *int   `json:"rate_bps"`
	Source            string `json:"source"`
	EvidenceReference string `json:"evidence_reference"`
}

// OperatorPolicy is supplier-owned setup, never a customer-submitted exemption.
// Its validity window limits new quotes; accepted assessments retain the proof.
type OperatorPolicy struct {
	Reference       string                          `json:"reference"`
	SupplierCountry string                          `json:"supplier_country"`
	ValidFrom       time.Time                       `json:"valid_from"`
	ValidUntil      time.Time                       `json:"valid_until"`
	NonEUConsumers  map[string]OperatorJurisdiction `json:"non_eu_consumers"`
	NonEUBusinesses map[string]OperatorJurisdiction `json:"non_eu_businesses"`
}

func DecodeOperatorPolicy(raw, supplierCountry string) (*OperatorPolicy, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > 256*1024 {
		return nil, errors.New("tax policy exceeds 256 KiB")
	}
	var p OperatorPolicy
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid tax policy JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("tax policy must contain one JSON object")
	}
	if strings.TrimSpace(p.Reference) == "" || !countries.IsValid(p.SupplierCountry) || p.SupplierCountry != countries.Normalize(supplierCountry) || p.ValidFrom.IsZero() || !p.ValidUntil.After(p.ValidFrom) {
		return nil, errors.New("tax policy requires a review reference, matching supplier country and validity window")
	}
	if len(p.NonEUConsumers)+len(p.NonEUBusinesses) == 0 {
		return nil, errors.New("tax policy requires a reviewed foreign jurisdiction")
	}
	for _, rules := range []map[string]OperatorJurisdiction{p.NonEUConsumers, p.NonEUBusinesses} {
		for country, rule := range rules {
			if country != countries.Normalize(country) || !countries.IsValid(country) || IsEU(country) || country == p.SupplierCountry || rule.RateBPS == nil || *rule.RateBPS < 0 || *rule.RateBPS > 10000 || strings.TrimSpace(rule.TaxLabel) == "" || len([]rune(rule.TaxLabel)) > 50 || strings.ContainsAny(rule.TaxLabel, "\r\n\t") || strings.TrimSpace(rule.Wording) == "" || strings.TrimSpace(rule.Source) == "" || len([]rune(rule.Source)) > 255 || strings.TrimSpace(rule.EvidenceReference) == "" {
				return nil, fmt.Errorf("invalid reviewed tax jurisdiction %q: explicit rate, source and evidence reference required", country)
			}
		}
	}

	return &p, nil
}

func (p *OperatorPolicy) Active(at time.Time) bool {
	return p != nil && !at.Before(p.ValidFrom) && at.Before(p.ValidUntil)
}

func (p *OperatorPolicy) Apply(policy *Policy) {
	policy.NonEUConsumers = map[string]Jurisdiction{}
	policy.NonEUBusinesses = map[string]Jurisdiction{}
	for country, r := range p.NonEUConsumers {
		policy.NonEUConsumers[country] = Jurisdiction{Rate: Rate{BPS: *r.RateBPS, Source: r.Source, CheckedOn: p.ValidFrom.UTC().Format("2006-01-02"), EffectiveFrom: p.ValidFrom.UTC().Format("2006-01-02")}, EvidenceReference: r.EvidenceReference, TaxLabel: r.TaxLabel, Wording: r.Wording}
	}
	for country, r := range p.NonEUBusinesses {
		policy.NonEUBusinesses[country] = Jurisdiction{Rate: Rate{BPS: *r.RateBPS, Source: r.Source, CheckedOn: p.ValidFrom.UTC().Format("2006-01-02"), EffectiveFrom: p.ValidFrom.UTC().Format("2006-01-02")}, EvidenceReference: r.EvidenceReference, TaxLabel: r.TaxLabel, Wording: r.Wording}
	}
}
