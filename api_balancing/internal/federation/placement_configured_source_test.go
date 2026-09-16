package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func configuredRelayRuntimeFixture(t *testing.T, mode string) (*PlacementDestination, *MediaServePreparationRuntime, *livePathRegistry, *fakeNotifyFedClient, *placementpb.PreparePlacementRequest) {
	t.Helper()
	registry := freshRegistry(t)
	store, _, request := placementReceiptFixture(t)
	sourceURI := "rtsp://upstream.example/live"
	if mode == "mist_native" {
		sourceURI = "playlist:///srv/loop.m3u"
	}
	fixture, configured, source := configuredFixture(t, mode, sourceURI, []string{"eu-ingest"})
	fixture.pair.Object.Authority.PlaybackId = "public-playback"
	fixture.pair.Tenant.Authority.EffectiveClusterGrants = append(fixture.pair.Tenant.Authority.EffectiveClusterGrants, &mediaauthoritypb.TenantClusterGrant{
		ClusterId: "eu-ingest", ControlCellId: "eu-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active",
		MediaConsent: &placementpb.CapacityConsent{AllowIngest: true},
	})
	ingestMode, err := control.IngestModeFromWire(mode)
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
	paths := fixture.discovery.Paths.(*MediaPlacementPaths)
	paths.Push.Registry = registry
	fed := &fakeNotifyFedClient{mutateAck: func(ack *federationpb.OriginPullAck) {
		for _, candidate := range source.entry.Locations["eu-cell"].EdgeCandidates {
			if candidate.NodeID == ack.SourceNodeId {
				ack.DtscUrl = candidate.DTSCURL + "?token=test"
				return
			}
		}
	}}
	arrange := makeDepsAt(t, fed, map[string]string{"eu-cell": "peer:18009"}, store.Now)
	arrange.Registry = registry
	sharedRegistry := control.NewRedisRegistryStore(arrange.Cache.client, "cluster-local")
	if _, _, err = registry.EnableRedisSync(t.Context(), sharedRegistry, "writer", testLogger()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registry.DisableRedisSync)
	push := &LivePushPreparationRuntime{
		Authority: fixture.discovery.Authority, Paths: paths.Push, Registry: registry, Arrange: arrange, Now: store.Now,
		DestinationFence: func(context.Context, string, string) (int64, error) { return 7, nil },
	}
	serve := &MediaServePreparationRuntime{CellID: "us-cell", Authority: fixture.discovery.Authority, Paths: paths,
		Push: push, Registry: registry, Arrange: arrange, Snapshot: fixture.discovery.Snapshot, Now: store.Now}
	destination := &PlacementDestination{Discovery: fixture.discovery, Receipts: store, Now: store.Now}
	transport := PlacementTransport{LocalCellID: store.CellID, Local: destination}
	destination.Runtime = &PolicyBoundPlacementRuntime{Policy: &PlacementPolicyGate{
		CellID: store.CellID, Authority: fixture.discovery.Authority, Router: transport.Router(), Now: store.Now,
	}, Media: &PlacementMediaRuntime{Serve: serve}}
	destination.RetainPrepared = push.RetainPreparedSourceDemand
	request.Query, request.ClusterId, request.NodeId = fixture.query, "us", "node-00"
	request.ExpiresAt = fixtureExpiration(store.Now(), fixture.pair.Object.ValidUntil)
	return destination, serve, source, fed, request
}

func fixtureExpiration(now, authorityExpiry time.Time) *timestamppb.Timestamp {
	until := now.Add(15 * time.Second)
	if authorityExpiry.Before(until) {
		until = authorityExpiry
	}
	return timestamppb.New(until)
}

func TestConfiguredRelayPreparationBindsAndAdmitsExactSource(t *testing.T) {
	for _, mode := range []string{"pull", "mist_native"} {
		t.Run(mode, func(t *testing.T) {
			destination, runtime, _, fed, request := configuredRelayRuntimeFixture(t, mode)
			response, err := destination.PreparePlacement(t.Context(), request)
			if err != nil || response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED || len(fed.calls) != 1 {
				t.Fatalf("configured relay preparation: %+v, %v, notifications=%d", response, err, len(fed.calls))
			}
			pull, found, err := runtime.Registry.CurrentInboundPull(t.Context(), request.Query.InternalName, request.NodeId)
			if err != nil || !found || !pull.PlacementRequired || pull.SourceClusterID != "eu-cell" || pull.SourceMediaClusterID != "eu-ingest" || pull.SourceNodeID != "eu-origin" {
				t.Fatalf("configured relay was not bound to its exact source: %+v, %v", pull, err)
			}
			resolved, err := runtime.ResolvePreparedSource(t.Context(), destination.Receipts, PlacementSourceIdentity{
				TenantID: "tenant", ObjectID: request.Query.ObjectId, InternalName: request.Query.InternalName,
				ClusterID: request.ClusterId, NodeID: request.NodeId, DestinationFence: 7,
			})
			if err != nil || resolved.AttemptID != pull.AttemptID || resolved.DTSCURL != pull.DTSCURL || resolved.DTSCURL == "" {
				t.Fatalf("configured relay source admission: %+v, %v", resolved, err)
			}
			reader := control.NewStreamRegistry(nil, "cluster-local", time.Minute)
			if _, _, err = reader.EnableRedisSync(t.Context(), control.NewRedisRegistryStore(runtime.Arrange.Cache.client, "cluster-local"), "reader", testLogger()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reader.DisableRedisSync)
			replica := *runtime
			replica.Registry = reader
			replicaArrange := *runtime.Arrange
			replicaArrange.Registry = reader
			replica.Arrange = &replicaArrange
			replicaPush := *runtime.Push
			replicaPush.Registry, replicaPush.Arrange = reader, &replicaArrange
			replica.Push = &replicaPush
			resolved, err = replica.ResolvePreparedSource(t.Context(), destination.Receipts, PlacementSourceIdentity{
				TenantID: "tenant", ObjectID: request.Query.ObjectId, InternalName: request.Query.InternalName,
				ClusterID: request.ClusterId, NodeID: request.NodeId, DestinationFence: 7,
			})
			if err != nil || resolved.AttemptID != pull.AttemptID || resolved.DTSCURL != pull.DTSCURL {
				t.Fatalf("another Foghorn replica could not admit the configured pull: %+v, %v", resolved, err)
			}
		})
	}
}

func TestConfiguredRelayCanMoveOriginWithoutAConfigurationChange(t *testing.T) {
	destination, runtime, source, fed, request := configuredRelayRuntimeFixture(t, "mist_native")
	if _, err := destination.PreparePlacement(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	first, found, err := runtime.Registry.CurrentInboundPull(t.Context(), request.Query.InternalName, request.NodeId)
	if err != nil || !found {
		t.Fatalf("first configured pull: %+v, %v", first, err)
	}
	location := source.entry.Locations["eu-cell"]
	location.EdgeCandidates = []control.EdgeCandidate{{
		NodeID: "eu-replacement", ClusterID: "eu-ingest", IsOrigin: true, BufferState: "DRY", Playable: true,
		DTSCURL: "dtsc://eu-replacement.example:14200/internal", DTSCObservedAt: time.Unix(1800000000, 0).Unix(), SourceObservedAt: time.Unix(1800000000, 0).Unix(),
	}}
	source.entry.Locations["eu-cell"] = location
	resolved, err := destination.ResolveOrReauthorizeMediaSource(t.Context(), runtime, PlacementSourceIdentity{
		TenantID: "tenant", ObjectID: request.Query.ObjectId, InternalName: request.Query.InternalName,
		ClusterID: request.ClusterId, NodeID: request.NodeId, DestinationFence: 7,
	})
	if err != nil {
		t.Fatalf("configured origin failover: %+v, %v", resolved, err)
	}
	replacement, found, err := runtime.Registry.CurrentInboundPull(t.Context(), request.Query.InternalName, request.NodeId)
	if err != nil || !found || replacement.AttemptID == first.AttemptID || replacement.SourceNodeID != "eu-replacement" || len(fed.calls) != 2 {
		t.Fatalf("configured origin was not atomically replaced: %+v, %v, notifications=%d", replacement, err, len(fed.calls))
	}
}

func TestConfiguredLocalOriginStillBindsNoFederationPull(t *testing.T) {
	fixture, reader, _ := configuredFixture(t, "pull", "rtsp://upstream.example/live", []string{"us"})
	fixture.pair.Object.Authority.PlaybackId = "public-playback"
	fixture.query.SourceGeneration = generationFor(t, reader, configuredAuthority(t, fixture))
	runtime, request := mediaServeFixture(t, fixture)
	prepared, err := runtime.Reconcile(t.Context(), request, PlacementReceipt{}, nil)
	if err != nil || prepared.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED {
		t.Fatalf("local configured source preparation: %+v, %v", prepared, err)
	}
}
