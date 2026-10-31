package handlers

import (
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
)

func TestTierUpgradeBillingStartPreservesPrecision(t *testing.T) {
	anchor := time.Date(2026, time.October, 30, 12, 0, 0, 0, time.UTC)
	oldStart := anchor.Add(123456 * time.Microsecond)
	for _, tc := range []struct {
		name, provider string
		start, end     time.Time
		applied, valid bool
	}{
		{"Stripe rounded grant boundary", "stripe", anchor, anchor.AddDate(0, 1, 0), true, true},
		{"exact boundary", "mollie", oldStart, oldStart.AddDate(0, 1, 0), true, true},
		{"earlier Stripe second", "stripe", anchor.Add(-time.Second), anchor.AddDate(0, 1, 0), true, false},
		{"advance boundary cannot rewind", "advance", anchor, anchor.AddDate(0, 1, 0), true, false},
		{"provider did not apply", "stripe", anchor, anchor.AddDate(0, 1, 0), false, false},
		{"period ends before local phase", "stripe", anchor, oldStart, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tierUpgradeBillingStart(purserdb.GetTierUpgradeRow{Provider: tc.provider, OldPeriodStart: oldStart}, billing.TierUpgradeReceipt{Applied: tc.applied, PeriodStart: tc.start, PeriodEnd: tc.end})
			if tc.valid {
				if err != nil || !got.Equal(oldStart) {
					t.Fatalf("billing start=%s err=%v, want %s", got, err, oldStart)
				}
			} else if err == nil {
				t.Fatalf("invalid provider period admitted at %s", got)
			}
		})
	}
}
