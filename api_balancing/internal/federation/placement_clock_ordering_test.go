package federation

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// advancingClock returns an earlier instant to the first caller and a later one
// to every caller after it. It stands in for real time passing while a reader
// fetches evidence: whatever the registry and inventory return was observed
// after the first reading, not before it.
func advancingClock(first, rest time.Time) func() time.Time {
	var calls atomic.Int64
	return func() time.Time {
		if calls.Add(1) == 1 {
			return first
		}
		return rest
	}
}

// Source evidence is judged against a clock read taken after that evidence was
// fetched. A reading taken before the fetch is necessarily older than a
// heartbeat or advertisement that lands during it, and evidence stamped after
// its reference instant is refused as impossible, so an earlier reading would
// report a maximally live publisher as absent whenever one interleaves. This is
// the same defect that made every configured and stored placement refuse, fixed
// there and not here, so it is pinned on both readers.
func TestLivePushSourceSurvivesEvidenceObservedDuringTheCall(t *testing.T) {
	f, reader, _ := livePathFixture(t)
	// Evidence sits between the two readings: after the first, before the second.
	reader.Now = advancingClock(f.now.Add(-2*time.Second), f.now.Add(2*time.Second))

	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
	if err != nil {
		t.Fatal(err)
	}
	generation, expires, err := reader.ResolveSourceGeneration(context.Background(), authority)
	if err != nil {
		t.Fatalf("publisher reporting during the call was read as absent: %v", err)
	}
	if generation != "source-generation" {
		t.Fatalf("source generation = %q, want source-generation", generation)
	}
	if !expires.After(f.now) {
		t.Fatalf("source expiry %s is not in the future relative to %s", expires, f.now)
	}
}

// The same ordering rule on the destination-discovery entry point: a publisher
// whose advertisement lands mid-call still makes entitled destinations feasible.
func TestLivePushDiscoverySurvivesEvidenceObservedDuringTheCall(t *testing.T) {
	f, reader, _ := livePathFixture(t)
	reader.Now = advancingClock(f.now.Add(-2*time.Second), f.now.Add(2*time.Second))

	response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil {
		t.Fatalf("discovery failed on evidence observed during the call: %v", err)
	}
	feasible := 0
	for _, node := range response.GetCandidates() {
		if node.GetSourceFeasible() {
			feasible++
		}
	}
	if feasible == 0 {
		t.Fatal("no destination was source-feasible: the live publisher was read as absent")
	}
}

// The configured/pull reader gathers its own inventory and federated
// advertisements after the caller's clock read, so it carries the same rule.
// The input is pinned away from the destination cluster, so the only route to
// feasibility is the relay source that reader.liveSource has to find.
func TestConfiguredSourceSurvivesEvidenceObservedDuringTheCall(t *testing.T) {
	f, reader, registry := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"empty"})
	f.pair.Tenant.Authority.EffectiveClusterGrants = append(f.pair.Tenant.Authority.EffectiveClusterGrants,
		&mediaauthoritypb.TenantClusterGrant{ClusterId: "eu-ingest", ControlCellId: "eu-cell", ClusterClass: "platform_official",
			OwnerTenantId: "platform", SubscriptionStatus: "active",
			MediaConsent: &placementpb.CapacityConsent{AllowIngest: true}})
	registry.entry = control.StreamEntry{TenantID: "tenant", InternalName: "internal", Locations: map[string]control.Location{
		"eu-cell": {ClusterID: "eu-cell", IsLiveNow: true, AdTimestamp: f.now.Unix(), EdgeCandidates: []control.EdgeCandidate{{
			NodeID: "eu-origin", ClusterID: "eu-ingest", IsOrigin: true, BufferState: "FULL", Playable: true,
			DTSCURL: "dtsc://eu.example:14200/pull+internal", DTSCObservedAt: f.now.Unix(),
		}}},
	}}

	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)
	// Only now does the clock start reporting the passage of time, so the
	// advertisement above lands between the reader's two readings.
	reader.Now = advancingClock(f.now.Add(-2*time.Second), f.now.Add(2*time.Second))

	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatalf("configured source observation failed on mid-call evidence: %v", err)
	}
	if !observation.Paths["node-00"].SourceFeasible {
		t.Fatal("relay source advertised during the call was read as absent, stranding the destination")
	}
}
