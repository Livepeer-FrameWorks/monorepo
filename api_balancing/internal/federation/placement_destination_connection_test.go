package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// disconnectedRelayFixture is a US destination cell serving an EU-published
// push stream by relay, with no fence reader configured: the destination's
// control connection is read from the process registry, where no Helmsman is
// connected. That is the destination's state while an announced Helmsman
// restart holds node health.
func disconnectedRelayFixture(t *testing.T) (*PlacementDestination, *discoveryFixture, *fakeNotifyFedClient, *placementpb.PreparePlacementRequest) {
	t.Helper()
	store, _, req := placementReceiptFixture(t)
	f := newDiscoveryFixture(t)
	f.pair.Tenant.Authority.EffectiveClusterGrants = append(f.pair.Tenant.Authority.EffectiveClusterGrants, &mediaauthoritypb.TenantClusterGrant{
		ClusterId: "eu-ingest", ControlCellId: "eu-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active",
		MediaConsent: &placementpb.CapacityConsent{AllowIngest: true},
	})
	f.pair.Object.Authority.PlaybackId = "public-playback"
	f.discovery.Now = store.Now
	source := &livePathRegistry{entry: control.StreamEntry{TenantID: "tenant", InternalName: "internal", Locations: map[string]control.Location{
		"eu-cell": {ClusterID: "eu-cell", IsLiveNow: true, AdTimestamp: f.now.Unix(), EdgeCandidates: []control.EdgeCandidate{{
			NodeID: "eu-publisher", ClusterID: "eu-ingest", IsOrigin: true, BufferState: "DRY", Playable: true, DTSCURL: "dtsc://eu.example:14200/live+internal",
			SourceGeneration: "source-generation", SourceRevision: 9007199254740993, SourceObservedAt: f.now.Unix(), DTSCObservedAt: f.now.Unix(),
		}}},
	}}}
	fed := &fakeNotifyFedClient{mutateAck: func(ack *federationpb.OriginPullAck) { ack.DtscUrl = "dtsc://eu.example:14200/live+internal" }}
	arrange := makeDepsAt(t, fed, map[string]string{"eu-cell": "peer:18009"}, store.Now)
	paths := &LivePushPlacementPaths{CellID: "us-cell", Registry: source, Snapshot: func() *state.BalancerSnapshot { return f.snapshot },
		Now: func() time.Time { return f.now }, SourceCellReachable: arrange.CanArrangeFromCell}
	f.discovery.Paths = &MediaPlacementPaths{Push: paths}
	media := &LivePushPreparationRuntime{Authority: f.discovery.Authority, Paths: paths, Registry: freshRegistry(t), Arrange: arrange, Now: store.Now}
	destination := &PlacementDestination{Discovery: f.discovery, Receipts: store, Now: store.Now}
	transport := PlacementTransport{LocalCellID: store.CellID, Local: destination}
	destination.Runtime = &PolicyBoundPlacementRuntime{Policy: &PlacementPolicyGate{
		CellID: store.CellID, Authority: f.discovery.Authority, Router: transport.Router(), Now: store.Now,
	}, Media: media}
	req.Query, req.NodeId = f.query, "node-00"
	return destination, f, fed, req
}

// A relay destination without a current control connection cannot have a pull
// arranged on it, so discovery reports it unavailable instead of offering it to
// a coordinator whose preparation would then be refused.
func TestDiscoveryDoesNotOfferRelayDestinationWithoutControlConnection(t *testing.T) {
	_, f, _, _ := disconnectedRelayFixture(t)
	observed, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || len(observed.GetCandidates()) != 12 {
		t.Fatalf("discovery: %v, %v", observed, err)
	}
	for _, candidate := range observed.GetCandidates() {
		if candidate.GetCapacity() != placementpb.Capacity_CAPACITY_UNAVAILABLE {
			t.Fatalf("discovery offered disconnected relay destination %s: %v", candidate.GetNodeId(), candidate)
		}
	}
}

// A coordinator can hold an observation taken before the destination lost its
// connection. The destination answers that preparation with a typed refusal:
// the node is unavailable and nothing was reserved, so the coordinator moves to
// its next candidate without a second replica repeating the attempt.
func TestDisconnectedRelayDestinationRefusesPreparationAsNodeUnavailable(t *testing.T) {
	destination, _, fed, req := disconnectedRelayFixture(t)
	response, err := destination.PreparePlacement(context.Background(), req)
	if err != nil || response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_NODE_UNAVAILABLE ||
		response.GetEndpoint() != "" || response.GetReady() || len(fed.calls) != 0 {
		t.Fatalf("disconnected destination preparation: %v, %v, notifications=%d", response, err, len(fed.calls))
	}
}

// disconnectedConfiguredRelayFixture is a US destination cell serving a pull
// input that runs on an EU origin, which US nodes may only relay. No fence
// reader is configured, so no destination has a control connection.
func disconnectedConfiguredRelayFixture(t *testing.T) (*PlacementDestination, *discoveryFixture, *fakeNotifyFedClient, *placementpb.PreparePlacementRequest) {
	t.Helper()
	store, _, request := placementReceiptFixture(t)
	fixture, configured, source := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"eu-ingest"})
	fixture.pair.Object.Authority.PlaybackId = "public-playback"
	fixture.pair.Tenant.Authority.EffectiveClusterGrants = append(fixture.pair.Tenant.Authority.EffectiveClusterGrants, &mediaauthoritypb.TenantClusterGrant{
		ClusterId: "eu-ingest", ControlCellId: "eu-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active",
		MediaConsent: &placementpb.CapacityConsent{AllowIngest: true},
	})
	ingestMode, err := control.IngestModeFromWire("pull")
	if err != nil {
		t.Fatal(err)
	}
	source.entry = control.StreamEntry{TenantID: "tenant", InternalName: "internal", Locations: map[string]control.Location{
		"eu-cell": {ClusterID: "eu-cell", IsLiveNow: true, AdTimestamp: fixture.now.Unix(), EdgeCandidates: []control.EdgeCandidate{{
			NodeID: "eu-origin", ClusterID: "eu-ingest", IsOrigin: true, BufferState: "DRY", Playable: true,
			DTSCURL:        "dtsc://eu.example:14200/" + control.RuntimeNameFor(ingestMode, "internal"),
			DTSCObservedAt: fixture.now.Unix(), SourceObservedAt: fixture.now.Unix(),
		}}},
	}}
	fixture.query.SourceGeneration = generationFor(t, configured, configuredAuthority(t, fixture))
	fixture.discovery.Now = store.Now
	configured.Now = store.Now
	registry := freshRegistry(t)
	paths := fixture.discovery.Paths.(*MediaPlacementPaths)
	paths.Push.Registry = registry
	fed := &fakeNotifyFedClient{}
	arrange := makeDepsAt(t, fed, map[string]string{"eu-cell": "peer:18009"}, store.Now)
	arrange.Registry = registry
	push := &LivePushPreparationRuntime{Authority: fixture.discovery.Authority, Paths: paths.Push, Registry: registry, Arrange: arrange, Now: store.Now}
	serve := &MediaServePreparationRuntime{CellID: "us-cell", Authority: fixture.discovery.Authority, Paths: paths,
		Push: push, Registry: registry, Arrange: arrange, Snapshot: fixture.discovery.Snapshot, Now: store.Now}
	destination := &PlacementDestination{Discovery: fixture.discovery, Receipts: store, Now: store.Now}
	transport := PlacementTransport{LocalCellID: store.CellID, Local: destination}
	destination.Runtime = &PolicyBoundPlacementRuntime{Policy: &PlacementPolicyGate{
		CellID: store.CellID, Authority: fixture.discovery.Authority, Router: transport.Router(), Now: store.Now,
	}, Media: &PlacementMediaRuntime{Serve: serve}}
	request.Query, request.ClusterId, request.NodeId = fixture.query, "us", "node-00"
	request.ExpiresAt = fixtureExpiration(store.Now(), fixture.pair.Object.ValidUntil)
	return destination, fixture, fed, request
}

// Configured-input relays need the destination's control connection like push
// relays do: discovery reports a disconnected relay destination unavailable.
func TestDiscoveryDoesNotOfferConfiguredRelayDestinationWithoutControlConnection(t *testing.T) {
	_, f, _, _ := disconnectedConfiguredRelayFixture(t)
	observed, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || len(observed.GetCandidates()) == 0 {
		t.Fatalf("discovery: %v, %v", observed, err)
	}
	for _, candidate := range observed.GetCandidates() {
		if candidate.GetClusterId() == "us" && candidate.GetCapacity() != placementpb.Capacity_CAPACITY_UNAVAILABLE {
			t.Fatalf("discovery offered disconnected configured relay destination %s: %v", candidate.GetNodeId(), candidate)
		}
	}
}

// A configured relay preparation on a disconnected destination is a typed
// refusal with nothing arranged, not an Unavailable error.
func TestDisconnectedConfiguredRelayRefusesPreparationAsNodeUnavailable(t *testing.T) {
	destination, _, fed, req := disconnectedConfiguredRelayFixture(t)
	response, err := destination.PreparePlacement(context.Background(), req)
	if err != nil || response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_NODE_UNAVAILABLE ||
		response.GetEndpoint() != "" || response.GetReady() || len(fed.calls) != 0 {
		t.Fatalf("disconnected configured relay preparation: %v, %v, notifications=%d", response, err, len(fed.calls))
	}
}
