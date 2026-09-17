package federation

import (
	"context"
	"testing"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// Dial feasibility for a configured input is decided per node: the cluster pin
// and consent admit every node of "us", but the stream's ingest policy names
// only node-00, so no other node is offered as a place to start the input.
func TestConfiguredSourceDialFeasibilityFollowsNodePlacement(t *testing.T) {
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	f.pair.Tenant.Authority.SchemaVersion = sharedauthority.NodePlacementSchemaVersion
	f.pair.Object.Authority.SchemaVersion = sharedauthority.NodePlacementSchemaVersion
	f.pair.Tenant.Authority.MediaPlacement.Ingest = &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
		Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"us"}, NodeIds: []string{"node-00"}}}},
	}}
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)

	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Paths["node-00"].SourceFeasible {
		t.Fatalf("policy-listed node was not feasible: %+v", observation.Paths["node-00"])
	}
	if observation.Paths["node-01"].SourceFeasible {
		t.Fatalf("node outside the ingest policy may dial the input: %+v", observation.Paths["node-01"])
	}
}
