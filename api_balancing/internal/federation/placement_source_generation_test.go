package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

func TestLivePushSourceGenerationUsesCurrentOwner(t *testing.T) {
	for _, scenario := range []string{"current", "withdrawn", "newer unplayable", "duplicate", "stale", "replica", "foreign", "managed", "expired", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			f, reader, registry := livePathFixture(t)
			authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			loc := registry.entry.Locations["eu-cell"]
			switch scenario {
			case "withdrawn":
				registry.entry.Locations["us-cell"] = control.Location{SourceRevision: loc.EdgeCandidates[0].SourceRevision, SourceActive: false}
			case "newer unplayable":
				next := loc.EdgeCandidates[0]
				next.SourceRevision++
				next.NodeID, next.SourceGeneration, next.BufferState = "next", "next-generation", "DRY"
				next.Playable = false
				loc.EdgeCandidates = append(loc.EdgeCandidates, next)
			case "duplicate":
				loc.EdgeCandidates = append(loc.EdgeCandidates, loc.EdgeCandidates[0])
			case "stale":
				loc.EdgeCandidates[0].SourceObservedAt = f.now.Add(-30 * time.Second).Unix()
			case "replica":
				loc.EdgeCandidates[0].IsOrigin = false
			case "foreign":
				loc.EdgeCandidates[0].ClusterID = "foreign"
			case "managed":
				authority.IngestMode = "pull"
			case "expired":
				authority.ExpiresAt = f.now
			case "canceled":
				cancel()
			}
			registry.entry.Locations["eu-cell"] = loc
			generation, until, err := reader.ResolveSourceGeneration(ctx, authority)
			if scenario == "current" {
				if err != nil || generation != "source-generation" || !until.Equal(authority.ExpiresAt) {
					t.Fatalf("source generation/lease changed: %q %v %v", generation, until, err)
				}
			} else if err == nil || generation != "" || !until.IsZero() {
				t.Fatalf("invalid source produced generation: %q %v %v", generation, until, err)
			}
		})
	}
}
