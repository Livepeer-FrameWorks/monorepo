package handlers

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRetainedStripeVATProofBelongsToPartyAndTaxPoint(t *testing.T) {
	point := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, key string
		value     any
		want      bool
	}{
		{"Greek VIES country maps to billing country", "country_code", "EL", true},
		{"another tenant", "tenant_id", "other", false},
		{"another VAT number", "vat_number_hash", "other", false},
		{"another country", "country_code", "DE", false},
		{"later evidence", "checked_at", point.Add(time.Second), false},
		{"expired evidence", "expires_at", point, false},
		{"negative evidence", "valid", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := map[string]any{"tenant_id": "tenant", "country_code": "EL", "vat_number_hash": "hash", "valid": true, "checked_at": point.Add(-time.Hour), "expires_at": point.Add(time.Hour)}
			facts[tc.key] = tc.value
			raw, err := json.Marshal(facts)
			if err != nil {
				t.Fatal(err)
			}
			if got := retainedVATCoversStripeTaxPoint(raw, "tenant", "GR", "hash", point); got != tc.want {
				t.Fatalf("retained evidence validity=%v want%v", got, tc.want)
			}
		})
	}
}
