package billing

import (
	"testing"
	"time"
)

func TestOperatorGrantSpec(t *testing.T) {
	for _, tc := range []struct {
		name       string
		amount     *string
		collection string
		until      string
		valid      bool
	}{
		{"zero fee", grantAmount("0"), "", "", true},
		{"maximum fee", grantAmount("99999999.99"), "", "", true},
		{"fee overflow", grantAmount("100000000.00"), "", "", false},
		{"invoice collection", nil, "invoice", "2026-12-31", true},
		{"negative fee", grantAmount("-1"), "", "", false},
		{"too many decimals", grantAmount("1.001"), "", "", false},
		{"invalid collection", nil, "cash", "", false},
		{"relative expiry", grantAmount("0"), "", "7d", false},
		{"no change", nil, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := OperatorGrantSpec{BaseFee: tc.amount, Collection: tc.collection, Until: tc.until, Reason: "test"}
			if err := spec.Validate(); (err == nil) != tc.valid {
				t.Fatalf("validation=%v, want valid=%v", err, tc.valid)
			}
		})
	}
	day := OperatorGrantSpec{Until: "2026-12-31"}
	end, err := day.Expiry()
	if err != nil || !end.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("date expiry=%v, %v", end, err)
	}
	offset, err := (OperatorGrantSpec{Until: "2026-12-31T12:00:00+02:00"}).Expiry()
	if err != nil || offset.Location() != time.UTC || !offset.Equal(time.Date(2026, 12, 31, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("offset deadline=%v location=%v err=%v", offset, offset.Location(), err)
	}
}

func grantAmount(value string) *string { return &value }
