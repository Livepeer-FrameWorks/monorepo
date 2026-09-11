package pricing

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPlacementQuoteExpiryIntersectsObservedBoundaries(t *testing.T) {
	now := snapshotObservedAt
	pendingTier := uuid.MustParse(snapshotTier)
	at := func(offset time.Duration) *time.Time { value := now.Add(offset); return &value }
	for _, test := range []struct {
		name        string
		modify      func(*PlacementTariffSnapshot)
		want        time.Duration
		unavailable bool
	}{
		{"short lease", func(_ *PlacementTariffSnapshot) {}, time.Minute, false},
		{"cluster repricing", func(s *PlacementTariffSnapshot) { s.NextPricingChange["owned"] = *at(20 * time.Second) }, 20 * time.Second, false},
		{"other cluster", func(s *PlacementTariffSnapshot) { s.NextPricingChange["other"] = *at(time.Second) }, time.Minute, false},
		{"period reset", func(s *PlacementTariffSnapshot) {
			s.Subscription.PeriodStart, s.Subscription.PeriodEnd = at(-time.Hour), at(10*time.Second)
		}, 10 * time.Second, false},
		{"scheduled tier", func(s *PlacementTariffSnapshot) {
			s.Subscription.PendingTierID, s.Subscription.PendingEffectiveAt = &pendingTier, at(15*time.Second)
		}, 15 * time.Second, false},
		{"due tier", func(s *PlacementTariffSnapshot) {
			s.Subscription.PendingTierID, s.Subscription.PendingEffectiveAt = &pendingTier, &now
		}, 0, true},
		{"overdue tier", func(s *PlacementTariffSnapshot) {
			s.Subscription.PendingTierID, s.Subscription.PendingEffectiveAt = &pendingTier, at(-time.Second)
		}, 0, true},
		{"pending without time", func(s *PlacementTariffSnapshot) { s.Subscription.PendingTierID = &pendingTier }, 0, true},
		{"pending without tier", func(s *PlacementTariffSnapshot) { s.Subscription.PendingEffectiveAt = at(time.Second) }, 0, true},
		{"zero pending tier", func(s *PlacementTariffSnapshot) {
			id := uuid.Nil
			s.Subscription.PendingTierID, s.Subscription.PendingEffectiveAt = &id, at(time.Second)
		}, 0, true},
		{"expired evidence", func(s *PlacementTariffSnapshot) { s.AsOf = now.Add(-time.Minute) }, 0, true},
		{"unknown cluster", func(s *PlacementTariffSnapshot) { delete(s.Clusters, "owned") }, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := &PlacementTariffSnapshot{AsOf: now, Clusters: map[string]*ClusterPricing{"owned": {}}, NextPricingChange: map[string]time.Time{}}
			test.modify(snapshot)
			expires, err := snapshot.QuoteExpiry("owned", now, now.Add(time.Hour))
			if test.unavailable {
				if !errors.Is(err, ErrPlacementQuoteUnavailable) || !expires.IsZero() {
					t.Fatalf("unavailable expiry: %v %v", expires, err)
				}
				return
			}
			if err != nil || !expires.Equal(now.Add(test.want)) {
				t.Fatalf("expiry %v %v, want %v", expires, err, test.want)
			}
			if got, err := snapshot.QuoteExpiry("owned", now, now.Add(time.Second)); err != nil || !got.Equal(now.Add(time.Second)) {
				t.Fatal("pricing extended access expiry")
			}
			if _, err := snapshot.QuoteExpiry("owned", expires, now.Add(time.Hour)); !errors.Is(err, ErrPlacementQuoteUnavailable) {
				t.Fatal("quote survived exact deadline")
			}
		})
	}
}

func TestPlacementAllowancePeriodRequiresExplicitCurrentWindow(t *testing.T) {
	now := snapshotObservedAt
	before, after := now.Add(-time.Hour), now.Add(time.Hour)
	for _, period := range []PlacementSubscriptionContext{{}, {PeriodStart: &before}, {PeriodEnd: &after}, {PeriodStart: &after, PeriodEnd: &before}, {PeriodStart: &now, PeriodEnd: &now}, {PeriodStart: &after, PeriodEnd: &after}, {PeriodStart: &before, PeriodEnd: &now}} {
		snapshot := &PlacementTariffSnapshot{AsOf: now, Subscription: period}
		if _, _, known := snapshot.AllowancePeriod(); known {
			t.Fatal("missing or non-current period invented allowances")
		}
	}
	snapshot := &PlacementTariffSnapshot{AsOf: now, Subscription: PlacementSubscriptionContext{PeriodStart: &now, PeriodEnd: &after}}
	start, end, known := snapshot.AllowancePeriod()
	if !known || !start.Equal(now) || !end.Equal(after) {
		t.Fatal("explicit current period lost")
	}
}
