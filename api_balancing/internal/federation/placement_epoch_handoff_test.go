package federation

import (
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

func TestLivePushPathsOwnershipEpochDominatesCellLocalRevision(t *testing.T) {
	for _, scenario := range []string{"successor in EU", "newer US withdrawal", "conflicting owner in same epoch"} {
		t.Run(scenario, func(t *testing.T) {
			f, paths, registry := livePathFixture(t)
			remote := registry.entry.Locations["eu-cell"]
			remote.EdgeCandidates[0].SourceGeneration = "eu-generation"
			remote.EdgeCandidates[0].SourceRevision = 4503599627370497
			remote.EdgeCandidates[0].SourceLeaseEpoch = 2
			remote.SourceLeaseEpoch, remote.SourceRevision, remote.SourceGeneration = 2, 4503599627370497, "eu-generation"
			remote.OwnerNodeID, remote.SourceClusterID, remote.SourceActive = "eu-publisher", "eu-ingest", true
			registry.entry.Locations["eu-cell"] = remote
			authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
			if err != nil {
				t.Fatal(err)
			}
			if generation, _, sourceErr := paths.ResolveSourceGeneration(t.Context(), authority); sourceErr != nil || generation != "eu-generation" {
				t.Fatalf("positive control rejected healthy EU source: generation=%s err=%v", generation, sourceErr)
			}
			local := control.Location{ClusterID: "us-cell", SourceGeneration: "us-generation", SourceLeaseEpoch: 1, SourceRevision: 4503599627370498}
			switch scenario {
			case "newer US withdrawal":
				local.SourceLeaseEpoch = 3
				local.SourceRevision = 1
			case "conflicting owner in same epoch":
				local.SourceLeaseEpoch = 2
				local.SourceRevision = 1
				local.SourceActive = true
				local.OwnerNodeID = "other-owner"
			}
			registry.entry.Locations["us-cell"] = local
			generation, _, err := paths.ResolveSourceGeneration(t.Context(), authority)
			if scenario == "successor in EU" {
				if err != nil || generation != "eu-generation" {
					t.Fatalf("old US-local counter vetoed globally newer EU owner: generation=%s err=%v", generation, err)
				}
				return
			}
			if err == nil || generation != "" {
				t.Fatalf("local revision overrode ownership fence: generation=%s err=%v", generation, err)
			}
		})
	}
}
