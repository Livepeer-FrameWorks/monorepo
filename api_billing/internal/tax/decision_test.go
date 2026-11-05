package tax

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestConsumerAndBusinessTaxDecisions(t *testing.T) {
	evidence := []Evidence{{Source: "billing_address", Country: "DE", Reference: "profile-v1"}, {Source: "payment_provider", Country: "DE", Reference: "payment-country-1"}}
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	lookup := func(_ context.Context, country string, got time.Time) (Rate, error) {
		if !got.Equal(at) {
			t.Fatalf("tax point = %v", got)
		}
		rates := map[string]int{"NL": 2100, "DE": 1900}
		return Rate{BPS: rates[country], Source: "effective rate table", CheckedOn: "2026-11-01", EffectiveFrom: "2026-01-01"}, nil
	}
	base := Policy{SupplierCountry: "NL", EUConsumerMode: Destination, DestinationRegistrationConfirmed: true}
	tests := []struct {
		name          string
		profile       Profile
		policy        Policy
		valid         bool
		validationErr error
		wantRate      int
		wantTreatment string
		wantReview    string
	}{
		{name: "Dutch consumer", profile: Profile{Type: Consumer, Country: "NL"}, policy: base, wantRate: 2100, wantTreatment: "domestic_vat"},
		{name: "German consumer destination VAT", profile: Profile{Type: Consumer, Country: "DE", Evidence: evidence}, policy: base, wantRate: 1900, wantTreatment: "eu_b2c_destination"},
		{name: "German business reverse charge", profile: Profile{Type: Business, Country: "DE", VATNumber: "DE123456789"}, policy: base, valid: true, wantTreatment: "eu_b2b_reverse_charge"},
		{name: "invalid VAT claim never silently becomes consumer", profile: Profile{Type: Business, Country: "DE", VATNumber: "DE123456789"}, policy: base, wantReview: "vat_number_invalid"},
		{name: "VIES outage never silently becomes consumer", profile: Profile{Type: Business, Country: "DE", VATNumber: "DE123456789"}, policy: base, validationErr: errors.New("timeout"), wantReview: "vat_validation_unavailable"},
		{name: "VAT country mismatch", profile: Profile{Type: Business, Country: "DE", VATNumber: "NL123456789B01"}, policy: base, wantReview: "vat_number_required"},
		{name: "consumer cannot claim reverse charge", profile: Profile{Type: Consumer, Country: "DE", VATNumber: "DE123456789", Evidence: evidence}, policy: base, wantReview: "consumer_vat_claim"},
		{name: "foreign consumer needs evidence", profile: Profile{Type: Consumer, Country: "DE"}, policy: base, wantReview: "consumer_location_evidence_required"},
		{name: "foreign country alone is not exemption", profile: Profile{Type: Consumer, Country: "GB", Evidence: []Evidence{{"billing_address", "GB", "profile"}, {"payment_provider", "GB", "provider"}}}, policy: base, wantReview: "foreign_tax_policy_required"},
		{name: "foreign business needs establishment proof", profile: Profile{Type: Business, Country: "US"}, policy: base, wantReview: "business_evidence_required"},
		{name: "unknown customer classification", profile: Profile{Country: "DE"}, policy: base, wantReview: "customer_type_required"},
		{name: "unconfigured consumer regime", profile: Profile{Type: Consumer, Country: "DE", Evidence: evidence}, policy: Policy{SupplierCountry: "NL"}, wantReview: "consumer_vat_regime_required"},
		{name: "registration unconfirmed", profile: Profile{Type: Consumer, Country: "DE", Evidence: evidence}, policy: Policy{SupplierCountry: "NL", EUConsumerMode: Destination}, wantReview: "destination_registration_required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validate := func(context.Context, string, string) (bool, error) { return tt.valid, tt.validationErr }
			got, err := Evaluate(context.Background(), tt.policy, tt.profile, 10000, at, lookup, validate)
			if tt.wantReview != "" {
				var review *ReviewError
				if !errors.As(err, &review) || review.Code != tt.wantReview {
					t.Fatalf("decision=%+v error=%v, want review %s", got, err, tt.wantReview)
				}
				return
			}
			if err != nil || got.Rate.BPS != tt.wantRate || got.Treatment != tt.wantTreatment {
				t.Fatalf("decision=%+v error=%v", got, err)
			}
			if got.ReverseCharge && (!got.VATValidated || got.Wording == "") {
				t.Fatalf("reverse charge missing validation/wording: %+v", got)
			}
		})
	}
}

func TestConsumerThresholdIncludesCurrentPurchaseAndPreviousYear(t *testing.T) {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	profile := Profile{Type: Consumer, Country: "DE", Evidence: []Evidence{{"billing_address", "DE", "profile"}, {"payment_provider", "DE", "provider"}}}
	policy := Policy{SupplierCountry: "NL", EUConsumerMode: OriginThreshold, SingleEUEstablishment: true, ThresholdTotalsKnown: true, DestinationRegistrationConfirmed: true}
	lookup := func(_ context.Context, c string, _ time.Time) (Rate, error) {
		return Rate{BPS: map[string]int{"NL": 2100, "DE": 1900}[c], Source: "rate table"}, nil
	}
	for _, tt := range []struct {
		name                        string
		current, previous, purchase int64
		known                       bool
		want                        int
		review                      string
	}{
		{"below threshold", 900_000, 900_000, 10_000, true, 2100, ""},
		{"exactly threshold", 990_000, 900_000, 10_000, true, 2100, ""},
		{"crossing purchase", 990_001, 900_000, 10_000, true, 1900, ""},
		{"previous year exceeded", 0, 1_000_001, 10_000, true, 1900, ""},
		{"unknown totals", 0, 0, 10_000, false, 0, "consumer_threshold_unproven"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := policy
			p.CurrentYearCrossBorderNetCents, p.PreviousYearCrossBorderNetCents, p.ThresholdTotalsKnown = tt.current, tt.previous, tt.known
			got, err := Evaluate(context.Background(), p, profile, tt.purchase, at, lookup, nil)
			if tt.review != "" {
				var review *ReviewError
				if !errors.As(err, &review) || review.Code != tt.review {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil || got.Rate.BPS != tt.want {
				t.Fatalf("decision=%+v error=%v", got, err)
			}
		})
	}
}

func TestConsumerEvidenceMustBeIndependentAndConsistent(t *testing.T) {
	for _, tt := range []struct {
		name     string
		evidence []Evidence
		code     string
	}{
		{"duplicate source", []Evidence{{"billing_address", "DE", "one"}, {"billing_address", "DE", "two"}}, "consumer_location_evidence_required"},
		{"conflict", []Evidence{{"billing_address", "DE", "one"}, {"payment_provider", "NL", "two"}}, "consumer_location_conflict"},
		{"unreferenced", []Evidence{{"billing_address", "DE", ""}, {"payment_provider", "DE", "two"}}, "consumer_location_evidence_required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var review *ReviewError
			err := validateConsumerEvidence(Profile{Country: "DE", Evidence: tt.evidence})
			if !errors.As(err, &review) || review.Code != tt.code {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestExclusiveMoneyUsesMinorUnitsAndRejectsOverflow(t *testing.T) {
	for _, tt := range []struct {
		net  int64
		rate int
		want Totals
	}{
		{10000, 2100, Totals{10000, 2100, 12100}},
		{10000, 1900, Totals{10000, 1900, 11900}},
		{10000, 0, Totals{10000, 0, 10000}},
		{50, 2100, Totals{50, 11, 61}},
	} {
		got, err := Exclusive(tt.net, tt.rate)
		if err != nil || got != tt.want {
			t.Fatalf("Exclusive(%d,%d)=%+v,%v", tt.net, tt.rate, got, err)
		}
	}
	if _, err := Exclusive(math.MaxInt64, 2100); err == nil {
		t.Fatal("overflow accepted")
	}
}
