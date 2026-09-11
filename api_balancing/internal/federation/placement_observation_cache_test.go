package federation

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

func TestPlacementObservationReuseDoesNotReuseViewerRanking(t *testing.T) {
	now := time.Now()
	cache := NewPlacementObservationCache(func() time.Time { return now })
	key := placementObservationKey{CellID: "cell", Query: "signed-query", TenantVersion: 1, ObjectVersion: 2}
	calls := 0
	load := func(context.Context) (balancer.PlacementCellObservation, error) {
		calls++
		result := balancer.PlacementCellObservation{Complete: true, ObservedAt: now, ExpiresAt: now.Add(10 * time.Second)}
		for i, longitude := range []float64{-74, 5} {
			result.Candidates = append(result.Candidates, placement.Candidate{TenantID: "tenant", OwnerTenantID: "tenant", ClusterID: "cluster", NodeID: fmt.Sprint(i),
				AllowedVerbs: []placement.Verb{placement.Serve}, Location: &placement.Coordinates{Latitude: 50, Longitude: longitude},
				ObservedAt: now, ExpiresAt: result.ExpiresAt, Capacity: placement.CapacityAvailable, SourceFeasible: true, Presence: placement.Present,
				BWLimit: 1000, BWAvailable: 900, RAMMax: 100, RAMUsed: 10, Price: &placement.Price{AmountMicros: 1}, Prices: []placement.Price{{AmountMicros: 2}}})
		}
		return result, nil
	}
	for i, longitude := range []float64{-74, 5} {
		observation, err := cache.observe(context.Background(), key, load)
		if err != nil {
			t.Fatal(err)
		}
		decision, err := placement.Evaluate(placement.Request{TenantID: "tenant", Verb: placement.Serve, Now: now, Complete: observation.Complete,
			Location: &placement.Coordinates{Latitude: 50, Longitude: longitude}, Candidates: observation.Candidates})
		if err != nil || len(decision.Choices) != 2 || decision.Choices[0].NodeID != fmt.Sprint(i) {
			t.Fatalf("viewer %d lost geographic ranking: %+v %v", i, decision, err)
		}
		observation.Candidates[0].Location.Longitude = 170
		observation.Candidates[0].AllowedVerbs[0] = placement.Ingest
		observation.Candidates[0].Price.AmountMicros = 900
		observation.Candidates[0].Prices[0].AmountMicros = 900
	}
	if calls != 1 {
		t.Fatalf("repeated census: %d", calls)
	}
	got, err := cache.observe(context.Background(), key, load)
	if err != nil || got.Candidates[0].Price.AmountMicros != 1 || got.Candidates[0].Prices[0].AmountMicros != 2 {
		t.Fatal("cached facts aliased caller mutations")
	}
	now = now.Add(placementObservationReuse)
	if _, err := cache.observe(context.Background(), key, load); err != nil || calls != 2 {
		t.Fatalf("reuse outlived its window: %d %v", calls, err)
	}
}

func TestPlacementObservationCacheNeverRetainsUnavailableOrExpiredEvidence(t *testing.T) {
	for _, scenario := range []string{"error", "incomplete", "expired", "future", "expired candidate", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now()
			cache := NewPlacementObservationCache(func() time.Time { return now })
			calls := 0
			load := func(context.Context) (balancer.PlacementCellObservation, error) {
				calls++
				value := balancer.PlacementCellObservation{Complete: true, ObservedAt: now, ExpiresAt: now.Add(time.Second)}
				switch scenario {
				case "error":
					return value, errors.New("unreachable")
				case "incomplete":
					value.Complete = false
				case "expired":
					value.ExpiresAt = now
				case "future":
					value.ObservedAt = now.Add(time.Second)
				case "expired candidate":
					value.Candidates = []placement.Candidate{{ExpiresAt: now}}
				}
				return value, nil
			}
			for range 2 {
				observed, _ := cache.observe(context.Background(), placementObservationKey{}, load)
				if scenario == "expired candidate" && !observed.Candidates[0].ExpiresAt.Equal(now) {
					t.Fatal("reuse extended expired candidate evidence")
				}
			}
			want := 2
			if scenario == "empty" || scenario == "expired candidate" {
				want = 1
			}
			if calls != want {
				t.Fatalf("%s reads=%d want=%d", scenario, calls, want)
			}
		})
	}
}

func TestPlacementObservationWaiterCancellationDoesNotCancelSharedRead(t *testing.T) {
	cache := NewPlacementObservationCache(nil)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	load := func(ctx context.Context) (balancer.PlacementCellObservation, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return balancer.PlacementCellObservation{}, ctx.Err()
		}
		now := time.Now()
		return balancer.PlacementCellObservation{Complete: true, ObservedAt: now, ExpiresAt: now.Add(time.Second)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := cache.observe(ctx, placementObservationKey{}, load); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	close(release)
	if value, err := cache.observe(context.Background(), placementObservationKey{}, load); err != nil || !value.Complete || calls.Load() != 1 {
		t.Fatalf("shared read lost after waiter cancellation: %+v %v calls=%d", value, err, calls.Load())
	}
}

func TestPlacementObservationCacheBoundsRetainedEntriesAndCandidates(t *testing.T) {
	now := time.Now()
	for _, count := range []int{0, 4096} {
		cache := NewPlacementObservationCache(func() time.Time { return now })
		load := func(context.Context) (balancer.PlacementCellObservation, error) {
			value := balancer.PlacementCellObservation{Complete: true, ObservedAt: now, ExpiresAt: now.Add(time.Second)}
			for range count {
				value.Candidates = append(value.Candidates, placement.Candidate{ExpiresAt: value.ExpiresAt})
			}
			return value, nil
		}
		for i := range placementObservationEntries + 1 {
			if _, err := cache.observe(context.Background(), placementObservationKey{Query: fmt.Sprint(i)}, load); err != nil {
				t.Fatal(err)
			}
		}
		if len(cache.entries) > placementObservationEntries || cache.candidates > placementObservationCandidates || cache.entries[placementObservationKey{Query: "0"}] != nil {
			t.Fatal("observation cache exceeded bounds or failed to evict oldest entry")
		}
	}
}
