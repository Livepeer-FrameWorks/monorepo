package billing

import (
	"testing"
	"time"
)

func TestMonthlyUpgradeAnniversaryCapsCalendarDay(t *testing.T) {
	for _, tc := range []struct{ at, want string }{{"2027-01-31T18:23:00Z", "2027-02-28T18:23:00Z"}, {"2028-01-31T18:23:00Z", "2028-02-29T18:23:00Z"}, {"2026-03-31T18:23:00Z", "2026-04-30T18:23:00Z"}} {
		t.Run(tc.at, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			if got := NextMonthlyAnniversary(at).Format(time.RFC3339); got != tc.want {
				t.Fatalf("anniversary=%s want %s", got, tc.want)
			}
		})
	}
}

func TestPreviousMonthlyAnniversaryCapsCalendarDay(t *testing.T) {
	for _, tc := range []struct{ at, want string }{
		{"2027-03-31T18:23:00Z", "2027-02-28T18:23:00Z"},
		{"2028-03-31T18:23:00Z", "2028-02-29T18:23:00Z"},
		{"2026-01-31T18:23:00Z", "2025-12-31T18:23:00Z"},
	} {
		t.Run(tc.at, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			if got := PreviousMonthlyAnniversary(at).Format(time.RFC3339); got != tc.want {
				t.Fatalf("prior anniversary=%s want %s", got, tc.want)
			}
		})
	}
}

func TestMonthlyDateBoundaryNeverShortensTheMonth(t *testing.T) {
	for _, tc := range []struct{ at, want string }{
		{"2026-01-31T18:23:00Z", "2026-03-01T00:00:00Z"},
		{"2026-01-31T00:00:00Z", "2026-02-28T00:00:00Z"},
		{"2028-01-31T18:23:00Z", "2028-03-01T00:00:00Z"},
		{"2026-03-17T18:23:00Z", "2026-04-18T00:00:00Z"},
	} {
		t.Run(tc.at, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			if got := NextMonthlyDateBoundary(at).Format(time.RFC3339); got != tc.want {
				t.Fatalf("date boundary=%s want %s", got, tc.want)
			}
		})
	}
}
