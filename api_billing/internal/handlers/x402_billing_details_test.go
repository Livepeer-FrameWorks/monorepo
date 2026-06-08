package handlers

import (
	"database/sql"
	"testing"
)

func validEmail() sql.NullString { return sql.NullString{String: "ops@example.com", Valid: true} }

const fullAddress = `{"street":"Main 1","city":"Amsterdam","postal_code":"1011AB","country":"NL"}`

// isBillingDetailsComplete gates VAT-invoice eligibility: a valid email plus a
// fully populated billing address. Every missing piece must make it false.
func TestIsBillingDetailsComplete(t *testing.T) {
	cases := []struct {
		name    string
		email   sql.NullString
		address string
		want    bool
	}{
		{name: "complete", email: validEmail(), address: fullAddress, want: true},
		{name: "null email", email: sql.NullString{Valid: false}, address: fullAddress, want: false},
		{name: "empty email", email: sql.NullString{String: "", Valid: true}, address: fullAddress, want: false},
		{name: "whitespace email", email: sql.NullString{String: "   ", Valid: true}, address: fullAddress, want: false},
		{name: "empty address bytes", email: validEmail(), address: "", want: false},
		{name: "malformed address json", email: validEmail(), address: `{not json`, want: false},
		{name: "missing street", email: validEmail(), address: `{"city":"A","postal_code":"1","country":"NL"}`, want: false},
		{name: "missing city", email: validEmail(), address: `{"street":"S","postal_code":"1","country":"NL"}`, want: false},
		{name: "missing postal code", email: validEmail(), address: `{"street":"S","city":"A","country":"NL"}`, want: false},
		{name: "missing country", email: validEmail(), address: `{"street":"S","city":"A","postal_code":"1"}`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBillingDetailsComplete(tc.email, []byte(tc.address)); got != tc.want {
				t.Fatalf("isBillingDetailsComplete = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseBillingAddress(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		addr, err := parseBillingAddress([]byte(fullAddress))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if addr.Street != "Main 1" || addr.City != "Amsterdam" || addr.PostalCode != "1011AB" || addr.Country != "NL" {
			t.Fatalf("address fields not parsed: %+v", addr)
		}
	})
	t.Run("malformed returns error and zero value", func(t *testing.T) {
		addr, err := parseBillingAddress([]byte(`{"street":`))
		if err == nil {
			t.Fatal("expected error for malformed json")
		}
		if addr != (billingAddress{}) {
			t.Fatalf("expected zero-value address on error, got %+v", addr)
		}
	})
}
