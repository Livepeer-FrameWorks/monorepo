package balancer

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

func placementRouteFixture() (PlacementRouteRequest, PlacementRouter, map[string]PlacementCellObservation) {
	now := time.Unix(1800000000, 0)
	req := PlacementRouteRequest{
		TenantID: "tenant", ObjectID: "stream", InternalName: "internal-stream", SourceGeneration: "source-1", Verb: placement.Serve, Protocol: "hls",
		Location: &placement.Coordinates{Latitude: 40.7, Longitude: -74}, PolicyRevision: 7, ParentRevision: 3,
		Cells: []PlacementCell{{ID: "eu-cell", ClusterIDs: []string{"eu"}}, {ID: "us-cell", ClusterIDs: []string{"us"}}},
	}
	observations := map[string]PlacementCellObservation{}
	for _, cell := range req.Cells {
		candidate := placement.Candidate{
			TenantID: req.TenantID, NodeID: cell.ID + "-node", ClusterID: cell.ClusterIDs[0], OwnerTenantID: "tenant",
			AllowedVerbs: []placement.Verb{placement.Serve, placement.Ingest}, ObservedAt: now.Add(-time.Second), ExpiresAt: now.Add(20 * time.Second),
			Capacity: placement.CapacityAvailable, CPUPercent: 20, RAMMax: 1000, RAMUsed: 100, BWLimit: 1000, BWAvailable: 900,
			Presence: placement.Present, Location: &placement.Coordinates{Latitude: 52, Longitude: 5},
		}
		if cell.ID == "us-cell" {
			candidate.Location = req.Location
			candidate.Presence, candidate.SourceFeasible = placement.Absent, true
		}
		observations[cell.ID] = PlacementCellObservation{Candidates: []placement.Candidate{candidate}, Complete: true, ObservedAt: now, ExpiresAt: now.Add(20 * time.Second)}
	}
	router := PlacementRouter{
		Now: func() time.Time { return now },
		Observe: func(_ context.Context, cell PlacementCell, _ PlacementRouteRequest) (PlacementCellObservation, error) {
			return observations[cell.ID], nil
		},
		Prepare: func(_ context.Context, _ PlacementCell, preparation PlacementPreparationRequest) (PlacementPreparationResult, error) {
			return matchingPreparation(preparation, now), nil
		},
	}
	return req, router, observations
}

func matchingPreparation(req PlacementPreparationRequest, now time.Time) PlacementPreparationResult {
	expiresAt := now.Add(20 * time.Second)
	if req.ExpiresAt.Before(expiresAt) {
		expiresAt = req.ExpiresAt
	}
	return PlacementPreparationResult{
		Outcome: PlacementAccepted, TenantID: req.Route.TenantID, ObjectID: req.Route.ObjectID, SourceGeneration: req.Route.SourceGeneration,
		ClusterID: req.Choice.ClusterID, NodeID: req.Choice.NodeID, Protocol: req.Route.Protocol, PolicyRevision: req.Route.PolicyRevision,
		ParentRevision: req.Route.ParentRevision, AttemptID: req.AttemptID, ExpiresAt: expiresAt, Endpoint: "https://" + req.Choice.NodeID + "/hls/stream/index.m3u8",
		PolicyDigest: req.Route.PolicyDigest,
		Ready:        !req.Choice.RequiresPull,
	}
}

func TestPlacementRouterBoundsAttemptAtMillisecondIssuance(t *testing.T) {
	req, router, observations := placementRouteFixture()
	now := router.Now().Add(999999 * time.Nanosecond)
	router.Now = func() time.Time { return now }
	for cell, observation := range observations {
		observation.ObservedAt, observation.ExpiresAt = now, now.Add(placement.PreparationLifetime)
		for i := range observation.Candidates {
			observation.Candidates[i].ObservedAt = now
			observation.Candidates[i].ExpiresAt = observation.ExpiresAt
		}
		observations[cell] = observation
	}
	called := false
	router.Prepare = func(_ context.Context, _ PlacementCell, attempt PlacementPreparationRequest) (PlacementPreparationResult, error) {
		called = true
		issued, err := placement.PreparationIssuedAt(attempt.AttemptID)
		if err != nil || !attempt.ExpiresAt.Equal(issued.Add(placement.PreparationLifetime)) {
			t.Fatalf("submillisecond observation extended attempt lifetime: %v, %v, %v", issued, attempt.ExpiresAt, err)
		}
		return matchingPreparation(attempt, now), nil
	}
	if _, err := router.Route(context.Background(), req); err != nil || !called {
		t.Fatalf("fresh maximum-lifetime decision rejected: %v", err)
	}
}

func TestPlacementRouterUSViewerPreparesColdUSNodeIndependentOfCellOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		req, router, _ := placementRouteFixture()
		if reverse {
			slices.Reverse(req.Cells)
		}
		var preparedCell string
		prepare := router.Prepare
		router.Prepare = func(ctx context.Context, cell PlacementCell, p PlacementPreparationRequest) (PlacementPreparationResult, error) {
			preparedCell = cell.ID
			return prepare(ctx, cell, p)
		}
		result, err := router.Route(context.Background(), req)
		if err != nil || result.Preparation.NodeID != "us-cell-node" || preparedCell != "us-cell" || result.Preparation.Ready || !result.Decision.Choices[0].RequiresPull {
			t.Fatalf("coordinator order changed viewer destination: %+v, %v", result, err)
		}
	}
}

func TestPlacementRouterPreparationDeadlineUsesDecisionEvidence(t *testing.T) {
	req, router, observations := placementRouteFixture()
	now := router.Now()
	eu := observations["eu-cell"]
	eu.ExpiresAt = now.Add(20 * time.Millisecond)
	observations["eu-cell"] = eu
	router.Prepare = func(ctx context.Context, _ PlacementCell, p PlacementPreparationRequest) (PlacementPreparationResult, error) {
		if !p.ExpiresAt.Equal(eu.ExpiresAt) {
			t.Fatalf("decision deadline was not carried to destination: %v", p.ExpiresAt)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 20*time.Millisecond {
			t.Fatal("preparation context outlives shortest evidence")
		}
		<-ctx.Done()
		return matchingPreparation(p, now), nil
	}
	if got, err := router.Route(context.Background(), req); !errors.Is(err, context.DeadlineExceeded) || got.Preparation.Endpoint != "" {
		t.Fatalf("late success escaped expired context: %+v, %v", got, err)
	}
}

func TestPlacementRouterEmptyPoolRequiresFreshCompleteness(t *testing.T) {
	for _, stamp := range []string{"fresh", "expired", "missing", "future", "unbounded"} {
		t.Run(stamp, func(t *testing.T) {
			req, router, observations := placementRouteFixture()
			req.Policy = &placement.Policy{SchemaVersion: placement.SchemaVersion, Groups: []placement.Group{
				{ID: "own", Match: placement.Selector{ClusterIDs: []string{"us"}}, Spillover: placement.CapacityOnly},
				{ID: "fallback", Match: placement.Selector{ClusterIDs: []string{"eu"}}},
			}}
			empty := observations["us-cell"]
			empty.Candidates = nil
			switch stamp {
			case "expired":
				empty.ExpiresAt = router.Now()
			case "missing":
				empty.ObservedAt = time.Time{}
			case "future":
				empty.ObservedAt = router.Now().Add(time.Second)
			case "unbounded":
				empty.ExpiresAt = router.Now().Add(time.Minute)
			}
			observations["us-cell"] = empty
			prepares := 0
			prepare := router.Prepare
			router.Prepare = func(ctx context.Context, cell PlacementCell, p PlacementPreparationRequest) (PlacementPreparationResult, error) {
				prepares++
				return prepare(ctx, cell, p)
			}
			got, err := router.Route(context.Background(), req)
			if stamp == "fresh" {
				if err != nil || got.Preparation.ClusterID != "eu" || prepares != 1 {
					t.Fatalf("fresh empty pool did not permit spill: %+v, %v", got, err)
				}
			} else if !errors.Is(err, ErrPlacementUnavailable) || got.Decision.Complete || prepares != 0 {
				t.Fatalf("invalid empty proof allowed spill: %+v, %v, prepares=%d", got, err, prepares)
			}
		})
	}
}

func TestPlacementRouterRechecksEmptyPoolExpiryAfterPreparation(t *testing.T) {
	for _, outcome := range []PlacementPreparationOutcome{PlacementAccepted, PlacementNodeUnavailable} {
		t.Run(string(outcome), func(t *testing.T) {
			req, router, observations := placementRouteFixture()
			now := router.Now()
			router.Now = func() time.Time { return now }
			req.Policy = &placement.Policy{SchemaVersion: placement.SchemaVersion, Groups: []placement.Group{
				{ID: "own", Match: placement.Selector{ClusterIDs: []string{"us"}}, Spillover: placement.CapacityOnly},
				{ID: "fallback", Match: placement.Selector{ClusterIDs: []string{"eu"}}},
			}}
			empty := observations["us-cell"]
			empty.Candidates, empty.ExpiresAt = nil, now.Add(time.Second)
			observations["us-cell"] = empty
			eu := observations["eu-cell"]
			second := eu.Candidates[0]
			second.NodeID = "eu-second"
			eu.Candidates = append(eu.Candidates, second)
			observations["eu-cell"] = eu
			prepares := 0
			router.Prepare = func(_ context.Context, _ PlacementCell, p PlacementPreparationRequest) (PlacementPreparationResult, error) {
				prepares++
				now = now.Add(2 * time.Second)
				result := matchingPreparation(p, now)
				result.Outcome = outcome
				return result, nil
			}
			got, err := router.Route(context.Background(), req)
			if !errors.Is(err, ErrPlacementPreparation) || prepares != 1 || got.Preparation.Endpoint != "" {
				t.Fatalf("expired completeness survived preparation: %+v, %v, prepares=%d", got, err, prepares)
			}
		})
	}
}

func TestPlacementRouterSpillsOnlyOnConfirmedCapacity(t *testing.T) {
	for _, outcome := range []PlacementPreparationOutcome{PlacementFull, PlacementNodeUnavailable, PlacementSourceUnavailable} {
		t.Run(string(outcome), func(t *testing.T) {
			req, router, _ := placementRouteFixture()
			req.Policy = &placement.Policy{SchemaVersion: placement.SchemaVersion, Groups: []placement.Group{
				{ID: "own", Match: placement.Selector{ClusterIDs: []string{"us"}}, Spillover: placement.CapacityOnly},
				{ID: "fallback", Match: placement.Selector{ClusterIDs: []string{"eu"}}},
			}}
			var attempts []string
			router.Prepare = func(_ context.Context, cell PlacementCell, p PlacementPreparationRequest) (PlacementPreparationResult, error) {
				attempts = append(attempts, cell.ID)
				result := matchingPreparation(p, router.Now())
				if cell.ID == "us-cell" {
					result.Outcome, result.Endpoint = outcome, ""
				}
				return result, nil
			}
			result, err := router.Route(context.Background(), req)
			if outcome == PlacementFull {
				if err != nil || result.Preparation.ClusterID != "eu" || len(attempts) != 2 {
					t.Fatalf("confirmed exhaustion did not spill: %+v %v %v", result, err, attempts)
				}
			} else if !errors.Is(err, ErrPlacementUnavailable) || len(attempts) != 1 {
				t.Fatalf("unavailable source/node became capacity spill: %+v %v %v", result, err, attempts)
			}
		})
	}
}

func TestPlacementRouterUnavailablePreferredCellIsNotEmpty(t *testing.T) {
	req, router, observations := placementRouteFixture()
	req.Policy = &placement.Policy{SchemaVersion: placement.SchemaVersion, Groups: []placement.Group{
		{ID: "own", Match: placement.Selector{ClusterIDs: []string{"us"}}, Spillover: placement.CapacityOnly},
		{ID: "fallback", Match: placement.Selector{ClusterIDs: []string{"eu"}}},
	}}
	router.Observe = func(_ context.Context, cell PlacementCell, _ PlacementRouteRequest) (PlacementCellObservation, error) {
		if cell.ID == "us-cell" {
			return PlacementCellObservation{}, errors.New("unreachable")
		}
		return observations[cell.ID], nil
	}
	router.Prepare = func(context.Context, PlacementCell, PlacementPreparationRequest) (PlacementPreparationResult, error) {
		t.Fatal("unreachable preferred capacity spilled")
		return PlacementPreparationResult{}, nil
	}
	result, err := router.Route(context.Background(), req)
	if !errors.Is(err, ErrPlacementUnavailable) || result.Decision.Complete {
		t.Fatalf("incomplete inventory accepted: %+v %v", result, err)
	}
}

func TestPlacementRouterDoesNotSubstituteARejectedDestination(t *testing.T) {
	req, router, observations := placementRouteFixture()
	extra := observations["us-cell"].Candidates[0]
	extra.NodeID = "us-second"
	extra.Location = &placement.Coordinates{Latitude: 39, Longitude: -74}
	us := observations["us-cell"]
	us.Candidates = append(us.Candidates, extra)
	observations["us-cell"] = us
	var attempts []string
	router.Prepare = func(_ context.Context, _ PlacementCell, p PlacementPreparationRequest) (PlacementPreparationResult, error) {
		attempts = append(attempts, p.Choice.NodeID)
		result := matchingPreparation(p, router.Now())
		if p.Choice.NodeID == "us-cell-node" {
			result.Outcome, result.Endpoint = PlacementNodeUnavailable, ""
		}
		return result, nil
	}
	result, err := router.Route(context.Background(), req)
	if err != nil || result.Preparation.NodeID != "us-second" || !slices.Equal(attempts, []string{"us-cell-node", "us-second"}) {
		t.Fatalf("exact ranked retry failed: %+v %v %v", result, err, attempts)
	}
}

func TestPlacementRouterRejectsMismatchedPreparation(t *testing.T) {
	for name, mutate := range map[string]func(*PlacementPreparationResult){
		"tenant":           func(p *PlacementPreparationResult) { p.TenantID = "other" },
		"object":           func(p *PlacementPreparationResult) { p.ObjectID = "other" },
		"source":           func(p *PlacementPreparationResult) { p.SourceGeneration = "source-2" },
		"cluster":          func(p *PlacementPreparationResult) { p.ClusterID = "eu" },
		"node":             func(p *PlacementPreparationResult) { p.NodeID = "another-edge" },
		"protocol":         func(p *PlacementPreparationResult) { p.Protocol = "webrtc" },
		"policy":           func(p *PlacementPreparationResult) { p.PolicyRevision++ },
		"parent":           func(p *PlacementPreparationResult) { p.ParentRevision++ },
		"attempt":          func(p *PlacementPreparationResult) { p.AttemptID = "another-attempt" },
		"expiry":           func(p *PlacementPreparationResult) { p.ExpiresAt = time.Time{} },
		"unbounded_expiry": func(p *PlacementPreparationResult) { p.ExpiresAt = p.ExpiresAt.Add(time.Hour) },
		"renewed_evidence": func(p *PlacementPreparationResult) { p.ExpiresAt = p.ExpiresAt.Add(time.Nanosecond) },
		"empty_endpoint":   func(p *PlacementPreparationResult) { p.Endpoint = "" },
	} {
		t.Run(name, func(t *testing.T) {
			req, router, _ := placementRouteFixture()
			router.Prepare = func(_ context.Context, _ PlacementCell, p PlacementPreparationRequest) (PlacementPreparationResult, error) {
				result := matchingPreparation(p, router.Now())
				mutate(&result)
				return result, nil
			}
			if _, err := router.Route(context.Background(), req); !errors.Is(err, ErrPlacementPreparation) {
				t.Fatalf("substituted preparation accepted: %v", err)
			}
		})
	}
}

func TestPlacementRouterAmbiguousPreparationIsNotRetriedElsewhere(t *testing.T) {
	req, router, _ := placementRouteFixture()
	ambiguous := errors.New("lost acknowledgement")
	calls := 0
	router.Prepare = func(context.Context, PlacementCell, PlacementPreparationRequest) (PlacementPreparationResult, error) {
		calls++
		return PlacementPreparationResult{}, ambiguous
	}
	if _, err := router.Route(context.Background(), req); !errors.Is(err, ambiguous) || calls != 1 {
		t.Fatalf("ambiguous preparation rerouted: %v (%d calls)", err, calls)
	}
}

func TestPlacementRouterRejectsCrossTenantAndUnownedNodeFacts(t *testing.T) {
	for _, wrongTenant := range []bool{false, true} {
		req, router, observations := placementRouteFixture()
		if wrongTenant {
			observations["us-cell"].Candidates[0].TenantID = "other"
		} else {
			observations["us-cell"].Candidates[0].ClusterID = "eu"
		}
		if _, err := router.Route(context.Background(), req); !errors.Is(err, ErrPlacementObservation) {
			t.Fatalf("unowned facts accepted: %v", err)
		}
	}
}

func TestPlacementRouterDiscoveryLeavesBudgetForHealthyDestination(t *testing.T) {
	req, router, observations := placementRouteFixture()
	router.Observe = func(ctx context.Context, cell PlacementCell, _ PlacementRouteRequest) (PlacementCellObservation, error) {
		if cell.ID == "us-cell" {
			<-ctx.Done()
			return PlacementCellObservation{}, ctx.Err()
		}
		return observations[cell.ID], nil
	}
	router.Prepare = func(ctx context.Context, _ PlacementCell, req PlacementPreparationRequest) (PlacementPreparationResult, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < time.Second {
			t.Fatal("unreachable peer consumed the preparation budget")
		}
		return matchingPreparation(req, router.Now()), nil
	}
	result, err := router.Route(context.Background(), req)
	if err != nil || result.Preparation.ClusterID != "eu" || result.Decision.Complete {
		t.Fatalf("healthy same-priority destination lost after peer timeout: %+v %v", result, err)
	}
}

func TestPlacementRouterUsesTheFullDiscoveryBudgetForEveryCell(t *testing.T) {
	req, router, observations := placementRouteFixture()
	router.Observe = func(ctx context.Context, cell PlacementCell, _ PlacementRouteRequest) (PlacementCellObservation, error) {
		select {
		case <-time.After(1100 * time.Millisecond):
			return observations[cell.ID], nil
		case <-ctx.Done():
			return PlacementCellObservation{}, ctx.Err()
		}
	}
	result, err := router.Route(context.Background(), req)
	if err != nil || result.Preparation.ClusterID != "us" || !result.Decision.Complete {
		t.Fatalf("healthy cells were cut off before the discovery deadline: %+v %v", result, err)
	}
}
