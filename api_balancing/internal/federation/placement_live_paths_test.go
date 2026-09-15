package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

type livePathRegistry struct {
	entry control.StreamEntry
	err   error
}

func (r *livePathRegistry) SourceSnapshot(_ context.Context, tenant, internal string) (control.StreamEntry, bool, error) {
	return r.entry, r.entry.TenantID == tenant && r.entry.InternalName == internal, r.err
}

func livePathFixture(t *testing.T) (*discoveryFixture, *LivePushPlacementPaths, *livePathRegistry) {
	t.Helper()
	f := newDiscoveryFixture(t)
	f.pair.Tenant.Authority.EffectiveClusterGrants = append(f.pair.Tenant.Authority.EffectiveClusterGrants, &mediaauthoritypb.TenantClusterGrant{
		ClusterId: "eu-ingest", ControlCellId: "eu-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active",
		MediaConsent: &placementpb.CapacityConsent{AllowIngest: true},
	})
	r := &livePathRegistry{entry: control.StreamEntry{TenantID: "tenant", InternalName: "internal", Locations: map[string]control.Location{
		"eu-cell": {ClusterID: "eu-cell", IsLiveNow: true, AdTimestamp: f.now.Unix(), EdgeCandidates: []control.EdgeCandidate{{
			NodeID: "eu-publisher", ClusterID: "eu-ingest", IsOrigin: true, BufferState: "DRY", Playable: true, DTSCURL: "dtsc://eu.example:14200/live+internal",
			SourceGeneration: "source-generation", SourceRevision: 9007199254740993, SourceObservedAt: f.now.Unix(), DTSCObservedAt: f.now.Unix(),
		}}},
	}}}
	reader := &LivePushPlacementPaths{CellID: "us-cell", Registry: r, Snapshot: func() *state.BalancerSnapshot { return f.snapshot }, Now: func() time.Time { return f.now }}
	// Discovery dispatches by signed kind; these fixtures exercise the push
	// reader behind that dispatcher, which is how the destination assembles it.
	f.discovery.Paths = &MediaPlacementPaths{Push: reader}
	return f, reader, r
}

func TestLivePushPathsColdUSNodesUseEntitledIngestOnlyEUSource(t *testing.T) {
	f, _, _ := livePathFixture(t)
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
	if err != nil || len(authority.Cells) != 1 || len(authority.Clusters) != 2 || !authority.SourceGrants["eu-ingest"].AllowIngest {
		t.Fatalf("source grant leaked into destination census or disappeared: %+v, %v", authority, err)
	}
	response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || len(response.GetCandidates()) != 12 || !response.GetComplete() {
		t.Fatalf("cold-source discovery: %+v, %v", response, err)
	}
	for _, node := range response.Candidates {
		if !node.SourceFeasible || node.Presence != placementpb.Presence_PRESENCE_ABSENT || node.Capacity != placementpb.Capacity_CAPACITY_AVAILABLE {
			t.Fatalf("cold US node was penalized or claimed presence: %+v", node)
		}
	}
}

func TestLivePushPathsPropagateCurrentSourceReadFailure(t *testing.T) {
	f, paths, registry := livePathFixture(t)
	registry.err = errors.New("shared source state unavailable")
	if response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query); err == nil || response != nil {
		t.Fatalf("source read failure became a complete empty inventory: %v, %v", response, err)
	}
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if generation, _, err := paths.ResolveSourceGeneration(context.Background(), authority); err == nil || generation != "" {
		t.Fatalf("source lookup reused cached generation after failure: %s, %v", generation, err)
	}
}

func TestLivePushPathsRejectInvalidOrStaleRemoteEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*discoveryFixture, *control.Location){
		"stale_ad":  func(f *discoveryFixture, l *control.Location) { l.AdTimestamp = f.now.Add(-30 * time.Second).Unix() },
		"future_ad": func(f *discoveryFixture, l *control.Location) { l.AdTimestamp = f.now.Add(time.Second).Unix() },
		"old_buffer_fresh_ad": func(f *discoveryFixture, l *control.Location) {
			l.EdgeCandidates[0].SourceObservedAt = f.now.Add(-30 * time.Second).Unix()
		},
		"old_listener_fresh_ad": func(f *discoveryFixture, l *control.Location) {
			l.EdgeCandidates[0].DTSCObservedAt = f.now.Add(-30 * time.Second).Unix()
		},
		"unknown_buffer_clock":   func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].SourceObservedAt = 0 },
		"unknown_listener_clock": func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].DTSCObservedAt = 0 },
		"replica":                func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].IsOrigin = false },
		"unplayable":             func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].Playable = false },
		"offline":                func(_ *discoveryFixture, l *control.Location) { l.IsLiveNow = false },
		"generation":             func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].SourceGeneration = "another" },
		"revision":               func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].SourceRevision = 0 },
		"unentitled":             func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].ClusterID = "foreign" },
		"wrong_cell":             func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].ClusterID = "us" },
		"dtsc_identity":          func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].DTSCURL = "dtsc://eu/live+another" },
		"dtsc_credentials": func(_ *discoveryFixture, l *control.Location) {
			l.EdgeCandidates[0].DTSCURL = "dtsc://secret:password@eu/live+internal"
		},
		"dtsc_bad_port": func(_ *discoveryFixture, l *control.Location) {
			l.EdgeCandidates[0].DTSCURL = "dtsc://eu:99999/live+internal"
		},
		"dtsc_query": func(_ *discoveryFixture, l *control.Location) { l.EdgeCandidates[0].DTSCURL += "?secret=token" },
	} {
		t.Run(name, func(t *testing.T) {
			f, _, r := livePathFixture(t)
			loc := r.entry.Locations["eu-cell"]
			mutate(f, &loc)
			r.entry.Locations["eu-cell"] = loc
			response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
			if err != nil || len(response.GetCandidates()) != 12 {
				t.Fatalf("unavailable source lost destination inventory: %+v, %v", response, err)
			}
			for _, candidate := range response.Candidates {
				if candidate.SourceFeasible || candidate.Presence != placementpb.Presence_PRESENCE_ABSENT {
					t.Fatalf("invalid source evidence accepted: %+v", candidate)
				}
			}
		})
	}
}

func TestLivePushPathsConsentGenerationFenceAndLifetime(t *testing.T) {
	t.Run("external_source_consent", func(t *testing.T) {
		f, _, _ := livePathFixture(t)
		f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowExternalSource = false
		response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
		if err != nil || response.Candidates[0].SourceFeasible {
			t.Fatalf("destination consent ignored: %+v, %v", response, err)
		}
	})
	t.Run("newer_withdrawal", func(t *testing.T) {
		f, _, r := livePathFixture(t)
		r.entry.Locations["us-cell"] = control.Location{SourceRevision: 9007199254740994, SourceActive: false}
		response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
		if err != nil || response.Candidates[0].SourceFeasible {
			t.Fatalf("older advertisement survived newer withdrawal: %+v, %v", response, err)
		}
	})
	t.Run("equal_revision_withdrawal", func(t *testing.T) {
		f, _, r := livePathFixture(t)
		r.entry.Locations["us-cell"] = control.Location{SourceRevision: 9007199254740993, SourceActive: false}
		response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
		if err != nil || response.Candidates[0].SourceFeasible {
			t.Fatalf("equal revision resurrected withdrawn ownership: %+v, %v", response, err)
		}
	})
	t.Run("newer_dry_owner", func(t *testing.T) {
		f, _, r := livePathFixture(t)
		loc := r.entry.Locations["eu-cell"]
		newer := loc.EdgeCandidates[0]
		newer.NodeID, newer.SourceGeneration, newer.BufferState = "new-publisher", "new-generation", "DRY"
		newer.Playable = false
		newer.SourceRevision++
		loc.EdgeCandidates = append(loc.EdgeCandidates, newer)
		r.entry.Locations["eu-cell"] = loc
		response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
		if err != nil || response.Candidates[0].SourceFeasible {
			t.Fatalf("old full buffer defeated newer dry owner: %+v, %v", response, err)
		}
	})
	t.Run("duplicate_owner", func(t *testing.T) {
		f, _, r := livePathFixture(t)
		loc := r.entry.Locations["eu-cell"]
		loc.EdgeCandidates = append(loc.EdgeCandidates, loc.EdgeCandidates[0])
		loc.EdgeCandidates[1].NodeID = "another-publisher"
		r.entry.Locations["eu-cell"] = loc
		if response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query); err == nil || response != nil {
			t.Fatalf("ambiguous ownership accepted: %+v, %v", response, err)
		}
	})
	t.Run("listener_expiry", func(t *testing.T) {
		f, _, r := livePathFixture(t)
		loc := r.entry.Locations["eu-cell"]
		loc.EdgeCandidates[0].DTSCObservedAt = f.now.Add(-29 * time.Second).Unix()
		r.entry.Locations["eu-cell"] = loc
		response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
		if err != nil || !response.ExpiresAt.AsTime().Equal(f.now.Add(time.Second)) || !response.Candidates[0].ExpiresAt.AsTime().Equal(response.ExpiresAt.AsTime()) {
			t.Fatalf("source expiry was extended by advertisement: %+v, %v", response, err)
		}
	})
}

func TestLivePushPathsLocalPublisherUsesLiveTenantEvidenceNotProjectionAge(t *testing.T) {
	f, reader, r := livePathFixture(t)
	r.entry.IngestMode = control.IngestPush
	r.entry.Locations = map[string]control.Location{"us-cell": {SourceActive: true, OwnerNodeID: "node-00", SourceGeneration: "source-generation", SourceRevision: 9, UpdatedAt: f.now.Add(-24 * time.Hour)}}
	f.snapshot.Nodes[0].Outputs["DTSC"] = "dtsc://HOST:14200/$"
	f.snapshot.Nodes[0].Streams = map[string]state.BalancerStreamSummary{"internal": {
		TenantID: "tenant", Status: "live", BufferState: "FULL", Playable: true, Inputs: 1, ObservedAt: f.now,
	}}
	f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowExternalSource = false
	response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || response.Candidates[0].Presence != placementpb.Presence_PRESENCE_PRESENT || !response.Candidates[1].SourceFeasible {
		t.Fatalf("old ownership projection hid a live publisher: %+v, %v", response, err)
	}
	delete(f.snapshot.Nodes[0].Outputs, "DTSC")
	response, err = f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || response.Candidates[0].Presence != placementpb.Presence_PRESENCE_PRESENT || response.Candidates[1].SourceFeasible {
		t.Fatalf("missing DTSC erased local presence or invented an upstream path: %+v, %v", response, err)
	}
	f.snapshot.Nodes[0].Outputs["DTSC"] = "dtsc://HOST:14200/$"
	for name, mutate := range map[string]func(*state.BalancerStreamSummary){
		"other_tenant":  func(s *state.BalancerStreamSummary) { s.TenantID = "other" },
		"replica":       func(s *state.BalancerStreamSummary) { s.Replicated = true },
		"not_publisher": func(s *state.BalancerStreamSummary) { s.Inputs = 0 },
		"old_buffer":    func(s *state.BalancerStreamSummary) { s.ObservedAt = f.now.Add(-30 * time.Second) },
		"unplayable":    func(s *state.BalancerStreamSummary) { s.Playable = false },
	} {
		t.Run(name, func(t *testing.T) {
			old := f.snapshot.Nodes[0].Streams["internal"]
			changed := old
			mutate(&changed)
			f.snapshot.Nodes[0].Streams["internal"] = changed
			defer func() { f.snapshot.Nodes[0].Streams["internal"] = old }()
			response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
			if err != nil || response.Candidates[0].Presence != placementpb.Presence_PRESENCE_ABSENT || response.Candidates[1].SourceFeasible {
				t.Fatalf("invalid local publisher admitted: %+v, %v", response, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.ObservePlacementPaths(ctx, balancer.PlacementAuthority{}, nil, f.snapshot); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reader performed work: %v", err)
	}
}

func TestLivePushPathsNewIngestNeedsNoSourceAndCannotHandleOtherObjectKinds(t *testing.T) {
	f, reader, _ := livePathFixture(t)
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Ingest, f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.query.Verb, f.query.SourceGeneration = placementpb.Verb_VERB_INGEST, ""
	f.query.PolicyDigest = authority.PolicyDigest
	reader.Snapshot = func() *state.BalancerSnapshot { t.Fatal("new publisher discovery read upstream media"); return nil }
	got, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil || len(got.Paths) != 12 {
		t.Fatalf("new publisher required an existing source: %+v, %v", got, err)
	}
	for _, mode := range []string{"pull", "mist_native", ""} {
		authority.IngestMode = mode
		if _, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot); err == nil {
			t.Fatalf("invented push semantics for %q", mode)
		}
	}
	authority.IngestMode = "push"
	authority.ObjectKind = mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT
	if _, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot); err == nil {
		t.Fatal("artifact acquired publisher semantics")
	}
}
