package federation

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type staticLiveSecrets struct {
	secret *mediaauthoritypb.LiveStreamSecret
	err    error
}

func (s staticLiveSecrets) OpenLiveStreamSecret(localauthority.MediaObjectSnapshot) (*mediaauthoritypb.LiveStreamSecret, error) {
	return s.secret, s.err
}

// configuredFixture turns the shared discovery fixture into a configured live
// input: the signed object becomes a pull or Mist-native source, and the sealed
// secret carries the clusters entitled to dial it.
func configuredFixture(t *testing.T, mode, sourceURI string, allowed []string) (*discoveryFixture, *ConfiguredSourcePlacementPaths, *livePathRegistry) {
	t.Helper()
	f := newDiscoveryFixture(t)
	f.pair.Object.Authority.Object = &mediaauthoritypb.MediaObjectAuthority_LiveStream{
		LiveStream: &mediaauthoritypb.LiveStreamAuthority{StreamId: "stream", IngestMode: mode},
	}
	secret := &mediaauthoritypb.LiveStreamSecret{TenantId: "tenant", AuthorityId: f.pair.Object.AuthorityID}
	if mode == "pull" {
		secret.SourceEnabled, secret.SourceUri, secret.AllowedClusterIds = true, sourceURI, allowed
	} else {
		secret.NativeSourceSpec, secret.NativeAllowedClusterIds, secret.NativePlacementCount = sourceURI, allowed, 1
	}
	reg := &livePathRegistry{}
	reader := &ConfiguredSourcePlacementPaths{
		CellID: "us-cell", Authority: f.discovery.Authority, Secrets: staticLiveSecrets{secret: secret},
		Registry: reg, Snapshot: func() *state.BalancerSnapshot { return f.snapshot }, Now: func() time.Time { return f.now },
	}
	f.discovery.Paths = &MediaPlacementPaths{Push: &LivePushPlacementPaths{CellID: "us-cell", Registry: reg, Snapshot: reader.Snapshot}, Configured: reader}
	return f, reader, reg
}

func configuredAuthority(t *testing.T, f *discoveryFixture) balancer.PlacementAuthority {
	t.Helper()
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

// generationFor resolves the configuration generation the reader will accept, so
// the query names the same source the destination is asked to serve.
func generationFor(t *testing.T, reader *ConfiguredSourcePlacementPaths, authority balancer.PlacementAuthority) string {
	t.Helper()
	generation, until, err := reader.ResolveSourceGeneration(context.Background(), authority)
	if err != nil || generation == "" || !until.Equal(authority.ExpiresAt) {
		t.Fatalf("configured generation: %q, %v, %v", generation, until, err)
	}
	return generation
}

func TestConfiguredSourceFeasibilityFollowsSignedOriginConsent(t *testing.T) {
	// "us" may dial the input, "empty" is entitled to serve but is not an
	// allowed origin and has no live copy to relay from.
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	f.snapshot.Nodes = append(f.snapshot.Nodes, state.EnhancedBalancerNodeSnapshot{NodeID: "private-node", ClusterID: "empty",
		Host: "https://private.example", IsActive: true, CapEdge: true, LastHeartbeat: f.now, OutputsObservedAt: f.now,
		Outputs: map[string]any{"HLS": "http://HOST:8080/hls/$/index.m3u8"}})
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)

	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Paths["node-00"].SourceFeasible || observation.Paths["node-00"].Presence != placement.Absent {
		t.Fatalf("allowed origin cluster was not feasible: %+v", observation.Paths["node-00"])
	}
	if observation.Paths["private-node"].SourceFeasible || observation.Paths["private-node"].Presence != placement.Absent {
		t.Fatalf("cluster outside the input's allowed set became feasible: %+v", observation.Paths["private-node"])
	}
}

func TestConfiguredSourcePrivateUpstreamRequiresClusterConsent(t *testing.T) {
	for _, consented := range []bool{true, false} {
		name := "consented"
		if !consented {
			name = "refused"
		}
		t.Run(name, func(t *testing.T) {
			f, reader, _ := configuredFixture(t, "pull", "rtsp://10.1.2.3/live", []string{"us"})
			for _, grant := range f.pair.Tenant.Authority.EffectiveClusterGrants {
				if grant.ClusterId == "us" {
					grant.AllowPrivatePullSources = consented
				}
			}
			authority := configuredAuthority(t, f)
			f.query.SourceGeneration = generationFor(t, reader, authority)
			observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if observation.Paths["node-00"].SourceFeasible != consented {
				t.Fatalf("private upstream consent=%v produced feasible=%v", consented, observation.Paths["node-00"].SourceFeasible)
			}
		})
	}
}

func TestConfiguredSourcePresenceComesFromTheNodesOwnLiveCopy(t *testing.T) {
	f, reader, _ := configuredFixture(t, "mist_native", "playlist:///srv/loop.m3u", []string{"empty"})
	f.snapshot.Nodes[0].Streams = map[string]state.BalancerStreamSummary{
		"internal": {TenantID: "tenant", Status: "live", BufferState: "DRY", Playable: true, Inputs: 1, ObservedAt: f.now},
	}
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)

	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// node-00 runs the elected input even though its cluster is not the elected
	// origin; a node already serving the stream is present regardless.
	if observation.Paths["node-00"].Presence != placement.Present {
		t.Fatalf("running node was not present: %+v", observation.Paths["node-00"])
	}
	if observation.Paths["node-01"].Presence != placement.Absent || observation.Paths["node-01"].SourceFeasible {
		t.Fatalf("idle node in a non-origin cluster became usable: %+v", observation.Paths["node-01"])
	}
}

func TestConfiguredSourceGenerationBindsTheSealedConfiguration(t *testing.T) {
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	authority := configuredAuthority(t, f)
	first := generationFor(t, reader, authority)

	replaced, replacedReader, _ := configuredFixture(t, "pull", "rtsp://elsewhere.example/live", []string{"us"})
	second := generationFor(t, replacedReader, configuredAuthority(t, replaced))
	if first == second {
		t.Fatal("a different sealed input produced the same generation")
	}

	// A query naming another generation observes no usable destination rather
	// than serving the current configuration under a stale name.
	f.query.SourceGeneration = second
	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for id, path := range observation.Paths {
		if path.Presence != placement.Absent || path.SourceFeasible {
			t.Fatalf("stale generation kept %s usable: %+v", id, path)
		}
	}
}

func TestConfiguredSourceRefusesAuthorityChangedUnderTheReader(t *testing.T) {
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)
	f.pair.Object.Version++
	if _, _, err := reader.ResolveSourceGeneration(context.Background(), authority); err == nil {
		t.Fatal("source resolution accepted a different authority version")
	}
	if _, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot); err == nil {
		t.Fatal("path observation accepted a different authority version")
	}
}

func TestConfiguredSourceHasNoPublisherAdmission(t *testing.T) {
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	f.query.Verb = placementpb.Verb_VERB_INGEST
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Ingest, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot); err == nil {
		t.Fatal("configured input accepted publisher admission")
	}
}

func artifactFixture(t *testing.T, warm []string) (*discoveryFixture, *ArtifactPlacementPaths) {
	t.Helper()
	f := newDiscoveryFixture(t)
	f.pair.Object.AuthorityID = sharedauthority.ArtifactAuthorityID("artifact-1")
	f.pair.Object.Authority.ObjectKind = mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT
	f.pair.Object.Authority.Object = &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
		ArtifactId: "artifact-1", ArtifactHash: "hash-1", ArtifactKind: mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_VOD}}
	f.query.ObjectId = f.pair.Object.AuthorityID
	reader := &ArtifactPlacementPaths{CellID: "us-cell", Authority: f.discovery.Authority, Secrets: staticLiveSecrets{},
		WarmNodes: func(hash string) []state.ArtifactNodeInfo {
			if hash != "hash-1" {
				return nil
			}
			nodes := make([]state.ArtifactNodeInfo, 0, len(warm))
			for _, id := range warm {
				nodes = append(nodes, state.ArtifactNodeInfo{NodeID: id, ClusterID: "us"})
			}
			return nodes
		},
		Snapshot: func() *state.BalancerSnapshot { return f.snapshot }, Now: func() time.Time { return f.now },
	}
	f.discovery.Paths = &MediaPlacementPaths{Push: &LivePushPlacementPaths{CellID: "us-cell", Registry: &livePathRegistry{}, Snapshot: reader.Snapshot}, Artifact: reader}
	return f, reader
}

func TestArtifactPathsSeparateWarmCopiesFromStorageCapableEdges(t *testing.T) {
	f, reader := artifactFixture(t, []string{"node-00"})
	f.snapshot.Nodes[1].CapStorage = true
	authority := configuredAuthority(t, f)
	generation, until, err := reader.ResolveSourceGeneration(context.Background(), authority)
	if err != nil || generation == "" || !until.Equal(authority.ExpiresAt) {
		t.Fatalf("artifact generation: %q, %v, %v", generation, until, err)
	}
	f.query.SourceGeneration = generation

	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Paths["node-00"].Presence != placement.Present {
		t.Fatalf("warm copy was not present: %+v", observation.Paths["node-00"])
	}
	if observation.Paths["node-01"].Presence != placement.Absent || !observation.Paths["node-01"].SourceFeasible {
		t.Fatalf("storage-capable edge could not materialize the artifact: %+v", observation.Paths["node-01"])
	}
	if observation.Paths["node-02"].SourceFeasible {
		t.Fatalf("edge without storage offered to fetch the artifact: %+v", observation.Paths["node-02"])
	}
}

func TestArtifactPathsRefuseStaleNodesAndPublisherAdmission(t *testing.T) {
	f, reader := artifactFixture(t, []string{"node-00"})
	f.snapshot.Nodes[0].LastHeartbeat = f.now.Add(-time.Minute)
	f.snapshot.Nodes[1].CapStorage, f.snapshot.Nodes[1].IsActive = true, false
	authority := configuredAuthority(t, f)
	generation, _, err := reader.ResolveSourceGeneration(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	f.query.SourceGeneration = generation
	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Paths["node-00"].Presence != placement.Absent || observation.Paths["node-01"].SourceFeasible {
		t.Fatalf("stale or inactive destinations stayed usable: %+v %+v", observation.Paths["node-00"], observation.Paths["node-01"])
	}
	f.query.Verb = placementpb.Verb_VERB_INGEST
	ingest, err := balancer.CompilePlacementAuthority(f.pair, placement.Ingest, f.now)
	if err == nil {
		if _, observeErr := reader.ObservePlacementPaths(context.Background(), ingest, f.query, f.snapshot); observeErr == nil {
			t.Fatal("stored media accepted publisher admission")
		}
	}
}

func TestMediaPlacementPathsDispatchOnSignedKind(t *testing.T) {
	push := &LivePushPlacementPaths{CellID: "us-cell"}
	configured := &ConfiguredSourcePlacementPaths{CellID: "us-cell"}
	artifact := &ArtifactPlacementPaths{CellID: "us-cell"}
	paths := &MediaPlacementPaths{Push: push, Configured: configured, Artifact: artifact}
	live := mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM
	stored := mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT

	for _, tc := range []struct {
		name  string
		kind  mediaauthoritypb.MediaObjectKind
		mode  string
		want  MediaSourcePathReader
		fails bool
	}{
		{name: "push", kind: live, mode: "push", want: push},
		{name: "pull", kind: live, mode: "pull", want: configured},
		{name: "native", kind: live, mode: "mist_native", want: configured},
		{name: "artifact", kind: stored, want: artifact},
		{name: "unknown-mode", kind: live, mode: "webrtc-ingest", fails: true},
		{name: "unknown-kind", kind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_UNSPECIFIED, fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := paths.reader(balancer.PlacementAuthority{ObjectKind: tc.kind, IngestMode: tc.mode})
			if tc.fails {
				if err == nil {
					t.Fatal("unsupported object kind or ingest mode was dispatched")
				}
				return
			}
			if err != nil || reader != tc.want {
				t.Fatalf("dispatch: %T, %v", reader, err)
			}
		})
	}

	// A kind whose reader is absent must refuse rather than borrow another
	// kind's evidence.
	if _, err := (&MediaPlacementPaths{Push: push}).reader(balancer.PlacementAuthority{ObjectKind: stored}); err == nil {
		t.Fatal("missing artifact reader silently fell through")
	}
	if cell, err := paths.CellID(); err != nil || cell != "us-cell" {
		t.Fatalf("cell identity: %q, %v", cell, err)
	}
	if _, err := (&MediaPlacementPaths{Push: push, Artifact: &ArtifactPlacementPaths{CellID: "other"}}).CellID(); err == nil {
		t.Fatal("mixed-cell readers were accepted")
	}
}

func TestConfiguredSourceRelayRequiresExternalSourceConsent(t *testing.T) {
	// The input may only be dialed from a cluster in another cell, so local
	// destinations can serve it only by relaying that cell's live copy.
	f, reader, registry := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"eu-ingest"})
	f.pair.Tenant.Authority.EffectiveClusterGrants = append(f.pair.Tenant.Authority.EffectiveClusterGrants, &mediaauthoritypb.TenantClusterGrant{
		ClusterId: "eu-ingest", ControlCellId: "eu-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active",
		MediaConsent: &placementpb.CapacityConsent{AllowIngest: true},
	})
	registry.entry = control.StreamEntry{TenantID: "tenant", InternalName: "internal", Locations: map[string]control.Location{
		"eu-cell": {ClusterID: "eu-cell", IsLiveNow: true, AdTimestamp: f.now.Unix(), EdgeCandidates: []control.EdgeCandidate{{
			NodeID: "eu-origin", ClusterID: "eu-ingest", IsOrigin: true, BufferState: "FULL", Playable: true,
			DTSCURL: "dtsc://eu.example:14200/pull+internal", DTSCObservedAt: f.now.Unix(), SourceObservedAt: f.now.Unix(),
		}}},
	}}
	f.snapshot.Nodes = append(f.snapshot.Nodes, state.EnhancedBalancerNodeSnapshot{NodeID: "private-node", ClusterID: "empty",
		Host: "https://private.example", IsActive: true, CapEdge: true, LastHeartbeat: f.now, OutputsObservedAt: f.now,
		Outputs: map[string]any{"HLS": "http://HOST:8080/hls/$/index.m3u8"}})
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)

	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// "us" consents to an external source, so it may relay the remote origin.
	if !observation.Paths["node-00"].SourceFeasible {
		t.Fatalf("consenting destination could not relay the remote origin: %+v", observation.Paths["node-00"])
	}
	// "empty" has no external-source consent and cannot dial the input itself.
	if observation.Paths["private-node"].SourceFeasible {
		t.Fatalf("destination without external-source consent relayed anyway: %+v", observation.Paths["private-node"])
	}
}

// mediaServeFixture builds the serving preparation dispatcher over a fixture's
// discovery, as ConfigureLivePlacementDestination assembles it at startup.
func mediaServeFixture(t *testing.T, f *discoveryFixture) (*MediaServePreparationRuntime, *placementpb.PreparePlacementRequest) {
	t.Helper()
	paths := f.discovery.Paths.(*MediaPlacementPaths)
	runtime := &MediaServePreparationRuntime{CellID: "us-cell", Authority: f.discovery.Authority, Paths: paths,
		Push:     &LivePushPreparationRuntime{Authority: f.discovery.Authority, Paths: paths.Push, Now: func() time.Time { return f.now }},
		Snapshot: func() *state.BalancerSnapshot { return f.snapshot }, Now: func() time.Time { return f.now }}
	attempt, err := placement.NewPreparationAttemptID(f.now)
	if err != nil {
		t.Fatal(err)
	}
	request := &placementpb.PreparePlacementRequest{Query: f.query, ClusterId: "us", NodeId: "node-00",
		AttemptId: attempt, ExpiresAt: timestamppb.New(f.now.Add(20 * time.Second))}
	return runtime, request
}

func TestMediaServePreparationAcceptsConfiguredSourceWithoutPullBinding(t *testing.T) {
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	f.pair.Object.Authority.PlaybackId = "public-playback"
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)
	runtime, request := mediaServeFixture(t, f)

	prepared, err := runtime.Reconcile(context.Background(), request, PlacementReceipt{}, nil)
	if err != nil || prepared.GetNodeId() != "node-00" || prepared.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED {
		t.Fatalf("configured preparation: %v, %v", prepared, err)
	}
	if !strings.Contains(prepared.GetEndpoint(), "/public-playback/") {
		t.Fatalf("prepared endpoint did not address the signed playback identity: %q", prepared.GetEndpoint())
	}
	if prepared.GetExpiresAt().AsTime().After(f.pair.Object.ValidUntil) {
		t.Fatalf("preparation outlived its signed authority: %v", prepared.GetExpiresAt().AsTime())
	}
	// A configured input is materialized by the destination's own source
	// resolution, so a receipt carrying an arranged pull belongs elsewhere.
	if _, err := runtime.Reconcile(context.Background(), request, PlacementReceipt{Pull: &PlacementPullBinding{AttemptID: request.AttemptId}}, nil); err == nil {
		t.Fatal("configured preparation accepted a physical pull binding")
	}
	if err := runtime.Revalidate(context.Background(), request, PlacementReceipt{Response: prepared}); err != nil {
		t.Fatalf("revalidating an unchanged configured preparation: %v", err)
	}
}

func TestMediaServePreparationDoesNotOriginateOutsideNodePolicy(t *testing.T) {
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	f.pair.Tenant.Authority.SchemaVersion = sharedauthority.NodePlacementSchemaVersion
	f.pair.Object.Authority.SchemaVersion = sharedauthority.NodePlacementSchemaVersion
	f.pair.Tenant.Authority.MediaPlacement.Ingest = &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
		Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"us"}, NodeIds: []string{"node-00"}}}},
	}}
	f.snapshot.Nodes[0].Streams = map[string]state.BalancerStreamSummary{
		"internal": {TenantID: "tenant", Status: "live", Playable: true, Inputs: 1, ObservedAt: f.now},
	}
	f.snapshot.Nodes[0].Outputs["DTSC"] = "dtsc://HOST:14200/$"
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)
	runtime, request := mediaServeFixture(t, f)
	request.NodeId = "node-01"

	if prepared, err := runtime.Reconcile(context.Background(), request, PlacementReceipt{}, nil); err == nil {
		t.Fatalf("node outside the ingest policy originated the configured input: %+v", prepared)
	}
}

func TestMediaServePreparationRefusesDestinationWithoutASourcePath(t *testing.T) {
	// The input may only be dialed from another cluster and nothing is live, so
	// no destination in this cell has a source path.
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"eu-ingest"})
	f.pair.Tenant.Authority.EffectiveClusterGrants = append(f.pair.Tenant.Authority.EffectiveClusterGrants, &mediaauthoritypb.TenantClusterGrant{
		ClusterId: "eu-ingest", ControlCellId: "eu-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active",
		MediaConsent: &placementpb.CapacityConsent{AllowIngest: true},
	})
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)
	runtime, request := mediaServeFixture(t, f)

	if prepared, err := runtime.Reconcile(context.Background(), request, PlacementReceipt{}, nil); err == nil {
		t.Fatalf("preparation accepted a destination with no source path: %v", prepared)
	}
}

func TestMediaServePreparationAcceptsStoredMediaOnAWarmCopy(t *testing.T) {
	f, reader := artifactFixture(t, []string{"node-00"})
	f.pair.Object.Authority.PlaybackId = "public-playback"
	authority := configuredAuthority(t, f)
	generation, _, err := reader.ResolveSourceGeneration(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	f.query.SourceGeneration = generation
	runtime, request := mediaServeFixture(t, f)

	prepared, prepareErr := runtime.Reconcile(context.Background(), request, PlacementReceipt{}, nil)
	if prepareErr != nil || prepared.GetNodeId() != "node-00" || !strings.Contains(prepared.GetEndpoint(), "/public-playback/") {
		t.Fatalf("stored media preparation: %v, %v", prepared, prepareErr)
	}
}

func TestConfiguredSourceUnpinnedPublicInputMayRunOnAnyEntitledCluster(t *testing.T) {
	// A public pull source without a cluster pin is legitimate: the control
	// plane only requires a pin for private or multicast upstreams.
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", nil)
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)
	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Paths["node-00"].SourceFeasible {
		t.Fatalf("unpinned public input was not dialable by an entitled cluster: %+v", observation.Paths["node-00"])
	}
}

func TestConfiguredSourceUnpinnedPrivateInputStillNeedsClusterConsent(t *testing.T) {
	f, reader, _ := configuredFixture(t, "pull", "rtsp://10.1.2.3/live", nil)
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)
	observation, err := reader.ObservePlacementPaths(context.Background(), authority, f.query, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Paths["node-00"].SourceFeasible {
		t.Fatalf("private upstream reached a cluster without private-source consent: %+v", observation.Paths["node-00"])
	}
}

func TestConfiguredNativeSourceRequiresItsElection(t *testing.T) {
	f, reader, _ := configuredFixture(t, "mist_native", "playlist:///srv/loop.m3u", nil)
	authority := configuredAuthority(t, f)
	if _, _, err := reader.ResolveSourceGeneration(context.Background(), authority); err == nil {
		t.Fatal("a Mist-native input without an elected cluster was accepted")
	}
}

func TestMediaServePreparationAcceptsEvidenceObservedDuringTheCall(t *testing.T) {
	// A real clock advances between selecting the node and observing its source,
	// so the reader's observation is always stamped after the selection reading.
	// Preparation must not read that as evidence from the future.
	f, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	f.pair.Object.Authority.PlaybackId = "public-playback"
	authority := configuredAuthority(t, f)
	f.query.SourceGeneration = generationFor(t, reader, authority)
	runtime, request := mediaServeFixture(t, f)

	ticking := f.now
	advance := func() time.Time {
		ticking = ticking.Add(time.Millisecond)
		return ticking
	}
	runtime.Now = advance
	reader.Now = advance

	prepared, err := runtime.Reconcile(context.Background(), request, PlacementReceipt{}, nil)
	if err != nil || prepared.GetNodeId() != "node-00" {
		t.Fatalf("preparation rejected evidence observed during its own call: %v, %v", prepared, err)
	}
}
