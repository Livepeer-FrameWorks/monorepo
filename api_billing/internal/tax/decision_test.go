package tax

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBusinessVATCanonicalFormsReachValidation(t *testing.T) {
	for _, tc := range []struct{ country, raw, want string }{
		{"DE", "123456789", "DE123456789"},
		{"GR", "GR123456789", "EL123456789"},
		{"GR", "123456789", "EL123456789"},
		{"GR", "EL123456789", "EL123456789"},
	} {
		t.Run(tc.country+tc.raw, func(t *testing.T) {
			called := false
			validate := func(_ context.Context, country, vat string) (bool, error) {
				called = true
				if country != tc.country || vat != tc.want {
					t.Fatalf("validation received %s/%s, want %s/%s", country, vat, tc.country, tc.want)
				}
				return true, nil
			}
			d, err := Evaluate(context.Background(), Policy{SupplierCountry: "NL"}, Profile{Type: Business, Country: tc.country, VATNumber: tc.raw}, 100, time.Now(), nil, validate)
			if err != nil || !called || !d.ReverseCharge {
				t.Fatalf("valid national/Greek VAT form was blocked: decision=%+v err=%v", d, err)
			}
		})
	}
}

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

func TestConsumerEvidenceMustBeIndependentAndConsistent(t *testing.T) {
	for _, tt := range []struct {
		name     string
		evidence []Evidence
		code     string
	}{
		{"duplicate source", []Evidence{{"billing_address", "DE", "one"}, {"billing_address", "DE", "two"}}, "consumer_location_evidence_required"},
		{"conflict", []Evidence{{"billing_address", "DE", "one"}, {"payment_provider", "NL", "two"}}, "consumer_location_conflict"},
		{"unreferenced", []Evidence{{"billing_address", "DE", ""}, {"payment_provider", "DE", "two"}}, "consumer_location_evidence_required"},
		{"jurisdiction review is not payer location", []Evidence{{"billing_address", "DE", "one"}, {"jurisdiction_review", "DE", "policy-memo"}}, "consumer_location_source_unsupported"},
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

func TestFrenchNationalVATKeyIsNotACountryPrefix(t *testing.T) {
	for _, national := range []string{"DE123456789", "FR123456789", "EL123456789", "A1123456789"} {
		got, err := CanonicalVATNumber("FR", national)
		if err != nil || got != "FR"+national {
			t.Fatalf("French national key %s mistaken for country prefix: %q %v", national, got, err)
		}
		full, fullErr := CanonicalVATNumber("FR", "FR"+national)
		if fullErr != nil || full != got {
			t.Fatalf("French full/national forms disagree: %q %v", full, fullErr)
		}
	}
}

func TestUnknownLocationEvidenceSourceIsExplicitlyRejected(t *testing.T) {
	err := validateConsumerEvidence(Profile{Country: "DE", Evidence: []Evidence{{Source: "billing_address", Country: "DE", Reference: "profile"}, {Source: "unregistered_provider", Country: "DE", Reference: "claim"}}})
	var review *ReviewError
	if !errors.As(err, &review) || review.Code != "consumer_location_source_unsupported" {
		t.Fatalf("new source silently discarded: %v", err)
	}
}
