package tax

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConsumerAndBusinessTaxDecisions(t *testing.T) {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	lookup := func(_ context.Context, country string, point time.Time) (Rate, error) {
		if country != "NL" || !point.Equal(at) {
			t.Fatalf("wrong rate boundary %s/%v", country, point)
		}
		return Rate{BPS: 2100, Source: "effective rate table", CheckedOn: "2026-11-01", EffectiveFrom: "2026-01-01"}, nil
	}
	for _, tc := range []struct {
		name            string
		p               Profile
		purpose         Purpose
		valid           bool
		failure         error
		rate            int
		treatment, code string
		review          bool
	}{
		{name: "Dutch consumer", p: Profile{Type: Consumer, Country: "NL"}, rate: 2100, treatment: "domestic_vat"},
		{name: "Dutch business", p: Profile{Type: Business, Country: "NL", VATNumber: "NL853319145B01"}, rate: 2100, treatment: "domestic_vat"},
		{name: "German consumer home VAT", p: Profile{Type: Consumer, Country: "DE"}, rate: 2100, treatment: "eu_b2c_home"},
		{name: "German reverse charge", p: Profile{Type: Business, Country: "DE", VATNumber: "DE123456789"}, valid: true, treatment: "eu_b2b_reverse_charge"},
		{name: "US business outside Dutch VAT", p: Profile{Type: Business, Country: "US"}, treatment: "non_eu_service"},
		{name: "US consumer outside Dutch VAT", p: Profile{Type: Consumer, Country: "US"}, treatment: "non_eu_service"},
		{name: "unproven recurring VAT", p: Profile{Type: Business, Country: "DE", VATNumber: "DE123456789"}, purpose: Recurring, rate: 2100, treatment: "recurring_claim_domestic_vat", review: true},
		{name: "expired VIES recurring", p: Profile{Type: Business, Country: "DE", VATNumber: "DE123456789"}, purpose: Recurring, failure: &ValidationUnavailable{Cause: errors.New("outage")}, rate: 2100, treatment: "recurring_claim_domestic_vat", review: true},
		{name: "invalid invoice claim visible", p: Profile{Type: Business, Country: "DE", VATNumber: "NL853319145B01"}, purpose: Invoice, code: "vat_number_invalid"},
		{name: "DB failure retries unchanged", p: Profile{Type: Business, Country: "DE", VATNumber: "DE123456789"}, purpose: Recurring, failure: errors.New("database unavailable"), code: "operational"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			purpose := tc.purpose
			if purpose == "" {
				purpose = Checkout
			}
			d, err := Assess(t.Context(), AssessmentRequest{Purpose: purpose, At: at, Complete: true, Profile: tc.p}, lookup, func(context.Context, string, string) (bool, error) { return tc.valid, tc.failure })
			if tc.code != "" {
				var review *ReviewError
				if tc.code == "operational" {
					if err == nil || errors.As(err, &review) {
						t.Fatalf("operational error swallowed: %v", err)
					}
				} else if !errors.As(err, &review) || review.Code != tc.code {
					t.Fatalf("wrong visible review: %v", err)
				}
				return
			}
			if err != nil || d.Rate.BPS != tc.rate || d.Treatment != tc.treatment || d.ReviewRequired != tc.review {
				t.Fatalf("%+v %v", d, err)
			}
			if d.ReverseCharge && (!d.VATValidated || d.Wording == "") {
				t.Fatal("unproven reverse charge")
			}
		})
	}
}

func TestBusinessVATCanonicalFormsReachValidation(t *testing.T) {
	for _, tc := range []struct{ country, raw, want string }{{"DE", "123456789", "DE123456789"}, {"GR", "GR123456789", "EL123456789"}, {"GR", "123456789", "EL123456789"}, {"GR", "EL123456789", "EL123456789"}} {
		t.Run(tc.country+tc.raw, func(t *testing.T) {
			called := false
			d, err := Assess(t.Context(), AssessmentRequest{Purpose: Checkout, At: time.Now(), Complete: true, Profile: Profile{Type: Business, Country: tc.country, VATNumber: tc.raw}}, nil, func(_ context.Context, country, vat string) (bool, error) {
				called = true
				if country != tc.country || vat != tc.want {
					t.Fatalf("wrong VAT %s/%s", country, vat)
				}
				return true, nil
			})
			if err != nil || !called || !d.ReverseCharge {
				t.Fatalf("%+v %v", d, err)
			}
		})
	}
}

func TestFrenchNationalVATKeyIsNotACountryPrefix(t *testing.T) {
	for _, national := range []string{"DE123456789", "FR123456789", "EL123456789", "A1123456789"} {
		got, err := CanonicalVATNumber("FR", national)
		if err != nil || got != "FR"+national {
			t.Fatalf("%q %v", got, err)
		}
		full, err := CanonicalVATNumber("FR", "FR"+national)
		if err != nil || full != got {
			t.Fatalf("%q %v", full, err)
		}
	}
}
