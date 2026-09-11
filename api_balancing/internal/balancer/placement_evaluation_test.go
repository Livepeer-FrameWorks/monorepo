package balancer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

func TestPlacementEvaluationDoesNotPrepareAndMatchesRouting(t *testing.T) {
	req, router, _ := placementRouteFixture()
	routed, err := router.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	router.Prepare = func(context.Context, PlacementCell, PlacementPreparationRequest) (PlacementPreparationResult, error) {
		t.Fatal("read-only evaluation dispatched preparation")
		return PlacementPreparationResult{}, nil
	}
	evaluated, err := router.Evaluate(context.Background(), req)
	if err != nil || !reflect.DeepEqual(evaluated.Decision, routed.Decision) || !evaluated.ExpiresAt.Equal(routed.Preparation.ExpiresAt) {
		t.Fatalf("evaluation diverged from routing: %v, %v", evaluated, err)
	}
	router.Prepare = nil
	if _, err = router.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("read-only evaluation required a mutator: %v", err)
	}
}

func TestPlacementEvaluationPreservesCrossCellSpillEvidence(t *testing.T) {
	for _, preferred := range []string{"available", "empty", "unreachable", "expired"} {
		t.Run(preferred, func(t *testing.T) {
			req, router, observations := placementRouteFixture()
			req.Policy = &placement.Policy{SchemaVersion: placement.SchemaVersion, Groups: []placement.Group{
				{ID: "own", Match: placement.Selector{ClusterIDs: []string{"eu"}}, Spillover: placement.CapacityOnly},
				{ID: "fallback", Match: placement.Selector{ClusterIDs: []string{"us"}}},
			}}
			eu := observations["eu-cell"]
			if preferred != "available" {
				eu.Candidates = nil
			}
			if preferred == "expired" {
				eu.ExpiresAt = router.Now()
			}
			observations["eu-cell"] = eu
			observe := router.Observe
			router.Observe = func(ctx context.Context, cell PlacementCell, req PlacementRouteRequest) (PlacementCellObservation, error) {
				if cell.ID == "eu-cell" && preferred == "unreachable" {
					return PlacementCellObservation{}, errors.New("peer unavailable")
				}
				return observe(ctx, cell, req)
			}
			result, err := router.Evaluate(context.Background(), req)
			switch preferred {
			case "available":
				if err != nil || result.Decision.Choices[0].ClusterID != "eu" {
					t.Fatalf("healthy preferred cell bypassed: %v, %v", result, err)
				}
			case "empty":
				if err != nil || result.Decision.Choices[0].ClusterID != "us" || !result.Decision.Complete {
					t.Fatalf("verified empty pool did not spill: %v, %v", result, err)
				}
			default:
				if !errors.Is(err, ErrPlacementUnavailable) || result.Decision.Complete {
					t.Fatalf("unknown preferred pool authorized fallback: %v, %v", result, err)
				}
			}
		})
	}
}

func TestPlacementEvaluationBoundsEveryReturnedChoice(t *testing.T) {
	req, router, observations := placementRouteFixture()
	eu := observations["eu-cell"]
	eu.Candidates[0].ExpiresAt = router.Now().Add(time.Second)
	observations["eu-cell"] = eu
	result, err := router.Evaluate(context.Background(), req)
	if err != nil || len(result.Decision.Choices) != 2 || !result.ExpiresAt.Equal(eu.Candidates[0].ExpiresAt) {
		t.Fatalf("read-only decision outlives a returned choice: %v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = router.Evaluate(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled evaluation returned a decision: %v", err)
	}
}
