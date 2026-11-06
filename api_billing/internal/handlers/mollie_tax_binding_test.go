package handlers

import (
	"encoding/json"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func TestMollieAssessedCustomerTypeBindsSavedProfile(t *testing.T) {
	for _, tc := range []struct {
		name, assessed, current, vat string
		want                         bool
	}{
		{"classification changed during ABA", "consumer", "business", "", false},
		{"business became consumer", "business", "consumer", "", false},
		{"same consumer", "consumer", "consumer", "", true},
		{"unspecified consumer fallback", "consumer", "unspecified", "", true},
		{"VAT claim still identifies business", "business", "unspecified", "NL853319145B01", true},
		{"explicit consumer retains classification despite VAT field", "business", "consumer", "NL853319145B01", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof, _ := json.Marshal(map[string]string{"customer_type": tc.assessed})
			profile := billingstripe.TaxProfile{Name: "Buyer", Email: "buyer@example.test", Street: "Main1", City: "Leiden", PostalCode: "2332ED", Country: "NL", VATNumber: tc.vat, Evidence: proof}
			current := purserdb.GetInvoiceTaxProfileRow{Name: profile.Name, Email: profile.Email, VatNumber: tc.vat, Address: json.RawMessage(`{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}`), CustomerType: tc.current}
			if got := mollieTaxProfileMatchesSavedBilling(profile, current); got != tc.want {
				t.Fatalf("assessed type%s locked type%s VAT%s: match=%v want%v", tc.assessed, tc.current, tc.vat, got, tc.want)
			}
		})
	}
}

func TestMollieBillingProfileComparisonNormalizesWhitespace(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved whitespace", true: "accepted whitespace"}[reverse], func(t *testing.T) {
			profile := billingstripe.TaxProfile{Name: "Buyer", Company: "Company", Email: "buyer@example.test", Street: "Main1", City: "Leiden", PostalCode: "2332ED", Country: "NL", VATNumber: "NL853319145B01", Evidence: json.RawMessage(`{"customer_type":"business"}`)}
			current := purserdb.GetInvoiceTaxProfileRow{Name: " \tBuyer ", Company: " Company ", Email: " buyer@example.test ", VatNumber: " nl 853319145b01 ", Address: json.RawMessage(`{"street":" Main1 ","city":" Leiden ","postal_code":" 2332ED ","country":" nl "}`), CustomerType: "business"}
			if reverse {
				profile.Name, profile.Company, profile.Email = " Buyer ", " Company ", " buyer@example.test "
				profile.Street, profile.City, profile.PostalCode, profile.Country, profile.VATNumber = " Main1 ", " Leiden ", " 2332ED ", " nl ", " nl 853319145b01 "
				current.Name, current.Company, current.Email, current.VatNumber = "Buyer", "Company", "buyer@example.test", "NL853319145B01"
				current.Address = json.RawMessage(`{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}`)
			}
			if !mollieTaxProfileMatchesSavedBilling(profile, current) {
				t.Fatal("equivalent billing whitespace became a permanent tax review")
			}
		})
	}
}
