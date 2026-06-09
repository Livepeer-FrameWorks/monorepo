package operator

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestStorageProviderCreditErrorPropagates verifies that an error from
// persistStorageProviderCredits aborts ComputeAndPersistCredits (the call is
// not best-effort: a failed storage-provider settlement must roll the whole
// invoice atom back).
func TestStorageProviderCreditErrorPropagates(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.invoice_line_items li`).
		WithArgs("inv-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "cluster_owner_tenant_id", "operator_credit_cents", "platform_fee_cents", "currency", "period_start", "period_end",
		}))
	mock.ExpectQuery(`all_provider_rows`).
		WithArgs("inv-1").
		WillReturnError(errors.New("boom"))

	tx, _ := db.BeginTx(context.Background(), nil)
	err = ComputeAndPersistCredits(context.Background(), tx, "inv-1", "paid")
	if err == nil {
		t.Fatal("expected error from storage provider credit failure, got nil")
	}
}

// TestDollarStringToCents_BoundaryDigits pins the per-character digit guard and
// the empty-string / fractional-truncation boundaries that mutation testing
// flagged. Each row is constructed to differ between the original predicate and
// its boundary/negation mutant.
func TestDollarStringToCents_BoundaryDigits(t *testing.T) {
	okCases := []struct {
		in   string
		want int64
	}{
		{"9", 900},        // '9' in whole part: kills `c > '9'` → `c >= '9'`
		{"0", 0},          // '0' in whole part: kills `c < '0'` → `c <= '0'`
		{"909.09", 90909}, // '9' and '0' in both parts
		{"1.999", 199},    // 3 fractional digits truncate to 2: kills `len(frac) > 2` negation
		{"1.99", 199},     // exactly 2 fractional digits, no truncation
	}
	for _, tc := range okCases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := dollarStringToCents(tc.in)
			if err != nil {
				t.Fatalf("dollarStringToCents(%q) err = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("dollarStringToCents(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}

	// Empty string must not panic and must parse to zero. Kills the
	// `len(s) > 0` → `len(s) >= 0` boundary mutant (which would index s[0]).
	t.Run("empty", func(t *testing.T) {
		got, err := dollarStringToCents("")
		if err != nil {
			t.Fatalf("dollarStringToCents(\"\") err = %v", err)
		}
		if got != 0 {
			t.Errorf("dollarStringToCents(\"\") = %d, want 0", got)
		}
	})
}

// TestPlatformFeeCents_SignAndRounding pins the half-up rounding and the
// sign-handling boundary of platformFeeCents to the cent. A positive and an
// equal-magnitude negative gross must produce equal-magnitude, opposite-sign
// fees; a zero gross must yield a zero fee.
func TestPlatformFeeCents_SignAndRounding(t *testing.T) {
	cases := []struct {
		name   string
		gross  int64
		feeBps int
		want   int64
	}{
		{"zero gross", 0, 2000, 0},
		{"positive exact", 1000, 2000, 200},       // 1000*2000=2_000_000 → /10000 = 200
		{"positive round half up", 333, 2000, 67}, // 333*2000=666000 +5000 =671000 /10000 = 67.1 → 67
		{"negative mirrors positive", -333, 2000, -67},
		{"positive boundary round", 25, 2000, 5}, // 25*2000=50000 +5000=55000 /10000=5.5→5
		{"negative boundary round", -25, 2000, -5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := platformFeeCents(tc.gross, tc.feeBps)
			if got != tc.want {
				t.Errorf("platformFeeCents(%d, %d) = %d, want %d", tc.gross, tc.feeBps, got, tc.want)
			}
		})
	}
}
