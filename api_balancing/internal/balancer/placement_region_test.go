package balancer

import (
	"context"
	"testing"
)

func TestPlacementRouterUsesObservedRegion(t *testing.T) {
	req, router, observations := placementRouteFixture()
	for cell, observation := range observations {
		observation.Candidates[0].Region = cell + "-region"
		observations[cell] = observation
	}
	prepare := router.Prepare
	router.Prepare = func(ctx context.Context, cell PlacementCell, req PlacementPreparationRequest) (PlacementPreparationResult, error) {
		got, err := prepare(ctx, cell, req)
		got.Region = "forged-region"
		return got, err
	}
	got, err := router.Route(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Preparation.Region != "us-cell-region" {
		t.Fatalf("region did not come from selected observation: %+v", got.Preparation)
	}
}
