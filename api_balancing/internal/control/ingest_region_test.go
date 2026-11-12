package control

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestPreparedIngestEndpointPreservesSelectedRegion(t *testing.T) {
	deps := &IngestDependencies{Protocol: "rtmp", Placement: ingestPlacementPreparerFunc(func(_ context.Context, req IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
		p := preparedIngestFixture(t, req)
		p.Region = "us-east"
		return p, nil
	})}
	stream := &commodorepb.ResolveStreamContextResponse{Admitted: true, IngestMode: "push", TenantId: "tenant", StreamId: "stream", InternalName: "internal"}
	got, err := ResolveIngestEndpoints(t.Context(), deps, stream, "private-key")
	if err != nil {
		t.Fatal(err)
	}
	if got.Primary.GetRegion() != "us-east" {
		t.Fatalf("selected destination's region was dropped: %+v", got.Primary)
	}
}
