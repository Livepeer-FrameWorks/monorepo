package frameworks

import (
	"encoding/json"
	"testing"
)

func TestAutomaticTopupInputPreservesDefaultsAndExplicitZero(t *testing.T) {
	zero, empty := 0, ""
	for _, tc := range []struct {
		name  string
		input SetAutomaticTopupInput
		want  string
	}{
		{"omitted defaults", SetAutomaticTopupInput{Currency: "EUR"}, `{"enabled":false,"currency":"EUR"}`},
		{"explicit zero threshold", SetAutomaticTopupInput{Enabled: true, Currency: "EUR", ThresholdEurCents: &zero}, `{"enabled":true,"currency":"EUR","thresholdEurCents":0}`},
		{"explicit empty consent", SetAutomaticTopupInput{Currency: "EUR", ConsentVersion: &empty}, `{"enabled":false,"currency":"EUR","consentVersion":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := omitNulls(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if !jsonEqual(json.RawMessage(encoded), json.RawMessage(tc.want)) {
				t.Fatalf("encoded %s, want %s", encoded, tc.want)
			}
		})
	}
}
