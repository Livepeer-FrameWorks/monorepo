package stripe

import (
	"encoding/json"
	"strconv"
	"testing"
)

func providerTermsLine(net, vat int64) ProviderTaxLine {
	line := ProviderTaxLine{Amount: net}
	line.Taxes = append(line.Taxes, struct {
		Amount int64 `json:"amount"`
	}{Amount: vat})
	return line
}

func providerTermsCredit(net, vat, originalNet, originalVAT, rate int64) ProviderTaxLine {
	line := providerTermsLine(net, vat)
	rates, _ := json.Marshal([]providerCreditRate{{ID: "txr_original", RateBPS: rate}})
	line.Metadata = map[string]string{
		"frameworks_credit_invoice": "in_original", "frameworks_credit_line": "il_original",
		"frameworks_credit_original_net": strconv.FormatInt(originalNet, 10),
		"frameworks_credit_original_vat": strconv.FormatInt(originalVAT, 10),
		"frameworks_credit_rates":        string(rates),
	}
	return line
}

func providerTermsDocument(profile TaxProfile, net, vat int64, lines ...ProviderTaxLine) ProviderTaxDocument {
	return ProviderTaxDocument{ID: "in_new", Kind: "provider_invoice", Number: "INV-NEW", Currency: "EUR", ProfileSHA: profile.Fingerprint(), Net: &net, VAT: &vat, Gross: net + vat, Lines: lines}
}

func TestValidateProviderTaxTermsAcceptsAssessedSuppliesAndOriginalRateCredits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rate  int
		net   int64
		vat   int64
		lines []ProviderTaxLine
	}{
		{name: "Dutch21percent", rate: 2100, net: 10000, vat: 2100},
		{name: "zero rate", rate: 0, net: 10000, vat: 0},
		{name: "old untaxed credit and new21percent", rate: 2100, net: 5000, vat: 2100, lines: []ProviderTaxLine{providerTermsLine(10000, 2100), providerTermsCredit(-5000, 0, 10000, 0, 0)}},
		{name: "old21percent credit and new zero rate", rate: 0, net: 5000, vat: -1050, lines: []ProviderTaxLine{providerTermsLine(10000, 0), providerTermsCredit(-5000, -1050, 10000, 2100, 2100)}},
		{name: "old21percent credit and new21percent", rate: 2100, net: 5000, vat: 1050, lines: []ProviderTaxLine{providerTermsLine(10000, 2100), providerTermsCredit(-5000, -1050, 10000, 2100, 2100)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := assessedTaxProfile()
			profile.RateBPS = tc.rate
			if tc.rate == 0 {
				profile.Country, profile.VATNumber = "DE", "DE123456789"
				profile.ReverseCharge, profile.Wording = true, "VAT reverse charged / btw verlegd"
			}
			doc := providerTermsDocument(profile, tc.net, tc.vat, tc.lines...)
			if err := ValidateProviderTaxTerms(doc, profile); err != nil {
				t.Fatalf("valid retained provider terms rejected: %v", err)
			}
		})
	}
}

func TestValidateProviderTaxTermsRejectsMissingOrDifferentAssessment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ProviderTaxDocument, *TaxProfile)
	}{
		{"missing profile identity", func(d *ProviderTaxDocument, _ *TaxProfile) { d.ProfileSHA = "" }},
		{"wrong profile identity", func(d *ProviderTaxDocument, _ *TaxProfile) { d.ProfileSHA = "different-profile" }},
		{"missing retained buyer", func(_ *ProviderTaxDocument, p *TaxProfile) { p.Name = "" }},
		{"missing retained country", func(_ *ProviderTaxDocument, p *TaxProfile) { p.Country = "" }},
		{"missing retained supplier", func(_ *ProviderTaxDocument, p *TaxProfile) { p.SupplierIdentity = "" }},
		{"missing net", func(d *ProviderTaxDocument, _ *TaxProfile) { d.Net = nil }},
		{"missing VAT", func(d *ProviderTaxDocument, _ *TaxProfile) { d.VAT = nil }},
		{"Dutch supply has no VAT", func(d *ProviderTaxDocument, p *TaxProfile) { *d = providerTermsDocument(*p, 10000, 0) }},
		{"gross does not reconcile", func(d *ProviderTaxDocument, _ *TaxProfile) { d.Gross++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := assessedTaxProfile()
			doc := providerTermsDocument(profile, 10000, 2100)
			tc.mutate(&doc, &profile)
			if err := ValidateProviderTaxTerms(doc, profile); err == nil {
				t.Fatal("invalid or missing retained tax terms accepted")
			}
		})
	}
}

func TestValidateProviderTaxTermsRejectsUnprovedMixedRateCredits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ProviderTaxDocument)
	}{
		{"missing source lines", func(d *ProviderTaxDocument) { d.Lines = nil }},
		{"source page is incomplete", func(d *ProviderTaxDocument) { d.MoreLines = true }},
		{"credit exceeds original net", func(d *ProviderTaxDocument) { d.Lines[1].Metadata["frameworks_credit_original_net"] = "4999" }},
		{"source invoice missing", func(d *ProviderTaxDocument) { delete(d.Lines[1].Metadata, "frameworks_credit_invoice") }},
		{"self credit", func(d *ProviderTaxDocument) { d.Lines[1].Metadata["frameworks_credit_invoice"] = d.ID }},
		{"source line missing", func(d *ProviderTaxDocument) { delete(d.Lines[1].Metadata, "frameworks_credit_line") }},
		{"malformed original net", func(d *ProviderTaxDocument) { d.Lines[1].Metadata["frameworks_credit_original_net"] = "not-a-number" }},
		{"original VAT negative", func(d *ProviderTaxDocument) { d.Lines[1].Metadata["frameworks_credit_original_vat"] = "-1" }},
		{"source rates missing", func(d *ProviderTaxDocument) { delete(d.Lines[1].Metadata, "frameworks_credit_rates") }},
		{"source rate invalid", func(d *ProviderTaxDocument) {
			d.Lines[1].Metadata["frameworks_credit_rates"] = `[{"id":"txr_original","rate_bps":10001}]`
		}},
		{"source VAT does not match original rate", func(d *ProviderTaxDocument) { d.Lines[1].Metadata["frameworks_credit_original_vat"] = "1" }},
		{"debit VAT differs from current terms", func(d *ProviderTaxDocument) { d.Lines[0].Taxes[0].Amount = 2000 }},
		{"line totals do not reconcile", func(d *ProviderTaxDocument) { d.Lines[0].Amount = 9999 }},
		{"duplicate source credit", func(d *ProviderTaxDocument) {
			d.Lines = []ProviderTaxLine{providerTermsLine(10000, 2100), providerTermsCredit(-2500, 0, 10000, 0, 0), providerTermsCredit(-2500, 0, 10000, 0, 0)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := assessedTaxProfile()
			doc := providerTermsDocument(profile, 5000, 2100, providerTermsLine(10000, 2100), providerTermsCredit(-5000, 0, 10000, 0, 0))
			tc.mutate(&doc)
			if err := ValidateProviderTaxTerms(doc, profile); err == nil {
				t.Fatal("unproved mixed-rate credit accepted")
			}
		})
	}
}

func TestValidateProviderTaxTermsChecksCreditCapsEvenWhenAggregateMatchesCurrentRate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []ProviderTaxLine
	}{
		{"credit exceeds source", []ProviderTaxLine{providerTermsLine(10000, 2100), providerTermsCredit(-5000, -1050, 1000, 210, 2100)}},
		{"credit VAT exceeds source", []ProviderTaxLine{providerTermsLine(10000, 2100), providerTermsCredit(-5000, -1050, 10000, 1049, 2100)}},
		{"duplicate credit", []ProviderTaxLine{providerTermsLine(10000, 2100), providerTermsCredit(-2500, -525, 10000, 2100, 2100), providerTermsCredit(-2500, -525, 10000, 2100, 2100)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := assessedTaxProfile()
			doc := providerTermsDocument(profile, 5000, 1050, tc.lines...)
			if err := ValidateProviderTaxTerms(doc, profile); err == nil {
				t.Fatal("matching aggregate VAT bypassed original-source credit safeguards")
			}
		})
	}
}
