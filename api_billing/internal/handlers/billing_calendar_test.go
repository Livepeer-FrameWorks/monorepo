package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestBillingCalendarKeepsMonthEndAnchors(t *testing.T) {
	parse := func(value string) time.Time {
		t.Helper()
		at, err := time.Parse(time.DateOnly, value)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	for _, tc := range []struct{ start, end, next string }{
		{"2027-01-31", "2027-02-28", "2027-03-31"},
		{"2027-02-28", "2027-03-31", "2027-04-30"},
		{"2027-01-31", "2027-04-30", "2027-07-31"},
		{"2026-10-01", "2026-10-31", "2026-11-30"},
	} {
		t.Run(tc.start+"/"+tc.end, func(t *testing.T) {
			start := parse(tc.start)
			end := parse(tc.end)
			if got := nextBillingPeriodEnd(start, end).Format(time.DateOnly); got != tc.next {
				t.Fatalf("next period after [%s,%s) ends %s; want %s", tc.start, tc.end, got, tc.next)
			}
		})
	}
}

func TestBillingCalendarPreservesExactMollieUpgradeStart(t *testing.T) {
	start := time.Date(2026, time.March, 17, 18, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.April, 18, 0, 0, 0, 0, time.UTC)
	gotStart, gotEnd := currentBillingPeriod(sql.NullTime{Time: start, Valid: true}, sql.NullTime{Time: end, Valid: true}, sql.NullTime{Time: end, Valid: true}, start)
	if !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatalf("recorded Mollie upgrade lost its first hours: [%s,%s); want [%s,%s)", gotStart, gotEnd, start, end)
	}
}

func TestBillingCalendarCapsClusterMonthlyFee(t *testing.T) {
	start := time.Date(2027, time.January, 31, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, time.February, 28, 0, 0, 0, 0, time.UTC)
	_, _, active, month := monthlyFeeOverlap(start, end, start, sql.NullTime{})
	if active != 28*86400 || month != 28*86400 {
		t.Fatalf("full February fee was prorated against the wrong month: active=%d month=%d", active, month)
	}
}
