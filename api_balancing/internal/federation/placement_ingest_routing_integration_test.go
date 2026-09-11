package federation

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ingestRoutingPool map[string]*foghorn.GRPCClient

func (pool ingestRoutingPool) GetOrCreate(cell, address string) (*foghorn.GRPCClient, error) {
	client := pool[cell+"@"+address]
	if client == nil {
		return nil, errors.New("unknown test cell address")
	}
	return client, nil
}

// This composes the actual router, authenticated transport, discovery, policy gate,
// connected lease reader and ingest runtime. Authority/inventory/telemetry inputs
// are fixtures; this is not a signed-delivery or publisher-media test.
func TestIngestRoutingAcrossAuthenticatedCellsIsCoordinatorIndependent(t *testing.T) {
	for _, scenario := range []string{"near-us", "near-us-west", "near-eu", "us-full", "us-unknown", "eu-owner", "owner-unknown", "official-denied",
		"selfhost-preferred", "selfhost-full", "selfhost-unknown", "selfhost-geo-hole", "selfhost-never-full"} {
		t.Run(scenario, func(t *testing.T) {
			for _, protocol := range []string{"whip", "rtmp", "srt"} {
				t.Run(protocol, func(t *testing.T) {
					routers, route, ownershipReads, _ := ingestRoutingCells(t, scenario, protocol)
					want := "us"
					if scenario == "near-eu" || scenario == "us-full" || scenario == "eu-owner" || scenario == "selfhost-preferred" {
						want = "eu"
					}
					deny := scenario == "us-unknown" || scenario == "owner-unknown" || scenario == "official-denied" || scenario == "selfhost-unknown" || scenario == "selfhost-never-full"
					// A public request's JWT must not replace service authentication.
					ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "publisher-jwt")
					var endpoint string
					for _, coordinator := range []string{"us-cell", "eu-cell"} {
						result, err := routers[coordinator].Route(ctx, route)
						if deny {
							if err == nil || result.Preparation.Endpoint != "" {
								t.Fatalf("%s admitted unknown/forbidden placement: %+v, %v", coordinator, result, err)
							}
							continue
						}
						prepared := result.Preparation
						wantNode := want + "-node"
						if scenario == "near-us-west" {
							wantNode = "us-west-node"
						}
						if err != nil || prepared.ClusterID != want || prepared.NodeID != wantNode || prepared.Ready || !mist.ValidIngestEndpointTemplate(prepared.Endpoint, protocol) {
							t.Fatalf("%s chose wrong/unprepared destination: %+v, %v", coordinator, prepared, err)
						}
						if endpoint != "" && endpoint != prepared.Endpoint {
							t.Fatalf("coordinator changed endpoint: %q != %q", endpoint, prepared.Endpoint)
						}
						endpoint = prepared.Endpoint
						if bound := mist.BindIngestEndpointTemplate(endpoint, protocol, "private-owner-key"); bound == "" || !strings.Contains(bound, "private-owner-key") {
							t.Fatal("confirmed endpoint cannot be bound by the credential-owning front door")
						}
					}
					if !deny && ownershipReads.Load() < 4 {
						t.Fatal("preparation did not recheck connected ownership")
					}
				})
			}
		})
	}
}

func ingestRoutingCells(t *testing.T, scenario, protocol string) (map[string]balancer.PlacementRouter, balancer.PlacementRouteRequest, *atomic.Int64, map[string]*IngestPlacementResolver) {
	t.Helper()
	now := time.Now().UTC()
	pair := newDiscoveryFixture(t).pair
	pair.Tenant.ValidUntil, pair.Object.ValidUntil = now.Add(time.Minute), now.Add(25*time.Second)
	pair.Tenant.Authority.EffectiveClusterGrants = nil
	for _, cluster := range []string{"us", "eu"} {
		pair.Tenant.Authority.EffectiveClusterGrants = append(pair.Tenant.Authority.EffectiveClusterGrants, &mediapb.TenantClusterGrant{
			ClusterId: cluster, ControlCellId: cluster + "-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active",
			MediaConsent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true},
		})
	}
	if scenario == "official-denied" {
		pair.Tenant.Authority.MediaPlacement.Ingest = &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
			Deny: []*placementpb.Selector{{Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL}}},
		}}
	}
	if scenario == "us-full" || scenario == "us-unknown" || strings.HasPrefix(scenario, "selfhost-") {
		preferred, fallback := "us", "eu"
		if strings.HasPrefix(scenario, "selfhost-") {
			preferred, fallback = "eu", "us"
			pair.Tenant.Authority.EffectiveClusterGrants[1].ClusterClass = "tenant_private"
			pair.Tenant.Authority.EffectiveClusterGrants[1].OwnerTenantId = "tenant"
		}
		group := &placementpb.Group{Id: "preferred", Match: &placementpb.Selector{ClusterIds: []string{preferred}}, Spillover: placementpb.Spillover_SPILLOVER_CAPACITY_ONLY}
		if scenario == "selfhost-geo-hole" {
			group.Spillover, group.GeoHoleDistanceKm, group.MinImprovementKm = placementpb.Spillover_SPILLOVER_GEO_HOLE, 2500, 100
		}
		if scenario == "selfhost-never-full" {
			group.Spillover = placementpb.Spillover_SPILLOVER_NEVER
		}
		pair.Tenant.Authority.MediaPlacement.Ingest = &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{
			group, {Id: "fallback", Match: &placementpb.Selector{ClusterIds: []string{fallback}}},
		}}}
	}
	authority, err := balancer.CompilePlacementAuthority(pair, placement.Ingest, now)
	if err != nil {
		t.Fatal(err)
	}
	reader := placementAuthorityReaderFunc(func(_ context.Context, tenant, object, internal string) (localauthority.PlacementPair, error) {
		if tenant != "tenant" || object != pair.Object.AuthorityID || internal != "internal" {
			return localauthority.PlacementPair{}, errors.New("incorrect signed identity")
		}
		return pair, nil
	})
	reads := &atomic.Int64{}
	fence := &ConnectedPlacementIngestFence{Client: placementStreamContextFunc(func(_ context.Context, stream, playback, internal, cluster string) (*commodorepb.ResolveStreamContextResponse, error) {
		reads.Add(1)
		if stream != "" || playback != "" || internal != "internal" || cluster != "" || scenario == "owner-unknown" {
			return nil, errors.New("ownership unavailable or claiming lookup")
		}
		response := &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", StreamId: "stream", InternalName: internal, IngestMode: "push"}
		if scenario == "eu-owner" {
			owner := "eu"
			response.ActiveIngestClusterId = &owner
		}
		return response, nil
	})}
	destinations := make(map[string]*PlacementDestination)
	addresses := make(map[string]string)
	pool := ingestRoutingPool{}
	for _, cluster := range []string{"us", "eu"} {
		cell := cluster + "-cell"
		latitude, longitude := 40.7, -74.0
		if cluster == "eu" {
			latitude, longitude = 52, 5
		}
		snapshot := &state.BalancerSnapshot{Nodes: []state.EnhancedBalancerNodeSnapshot{{
			NodeID: cluster + "-node", ClusterID: cluster, Host: "https://" + cluster + ".example", IsActive: true, CapIngest: true,
			CPU: 10, RAMMax: 100, RAMCurrent: 10, BWLimit: 1000, MetricsObservedAt: now, LastHeartbeat: now, OutputsObservedAt: now,
			HasCoordinates: true, GeoLatitude: latitude, GeoLongitude: longitude,
			Outputs: map[string]any{"RTMP": "rtmps://HOST:2935/play/$", "TSSRT": "srt://HOST:9889?streamid=$", "WebRTC": "https://HOST:8443/webrtc/$"},
		}}}
		if cluster == "us" {
			west := snapshot.Nodes[0]
			west.NodeID, west.Host = "us-west-node", "https://us-west.example"
			west.GeoLatitude, west.GeoLongitude = 37.8, -122.4
			snapshot.Nodes = append(snapshot.Nodes, west)
		}
		if (cluster == "us" && scenario == "us-full") || (cluster == "eu" && (scenario == "selfhost-full" || scenario == "selfhost-never-full")) {
			for i := range snapshot.Nodes {
				snapshot.Nodes[i].DownSpeed = snapshot.Nodes[i].BWLimit
			}
		}
		snapshotReader := func() *state.BalancerSnapshot { return snapshot }
		inventory := placementInventoryReaderFunc(func(_ context.Context, request *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
			if request.TenantId != "tenant" || request.ControlCellId != cell || len(request.ClusterIds) != 1 || request.ClusterIds[0] != cluster ||
				(cluster == "us" && scenario == "us-unknown") || (cluster == "eu" && scenario == "selfhost-unknown") {
				return nil, errors.New("inventory unavailable or incorrectly scoped")
			}
			inventory := &quartermasterpb.MediaPlacementInventory{TenantId: "tenant", ControlCellId: cell, ClusterIds: []string{cluster}, Complete: true, ObservedAt: timestamppb.New(now)}
			for _, node := range snapshot.Nodes {
				inventory.Nodes = append(inventory.Nodes, &quartermasterpb.MediaPlacementInventoryNode{ClusterId: cluster, NodeId: node.NodeID, AdmissionEnabled: true})
			}
			return inventory, nil
		})
		engine := miniredis.RunT(t)
		placementReceiptEngineInfo(engine, "a", "b", "0", "master")
		client := goredis.NewClient(&goredis.Options{Addr: engine.Addr(), MaxRetries: -1})
		t.Cleanup(func() { _ = client.Close() })
		epochTime := now.Add(-2*placement.PreparationClockSkew - time.Second)
		store := &PlacementReceiptStore{CellID: cell, Client: client, Now: func() time.Time { return epochTime }}
		engine.SetTime(epochTime)
		primePlacementReceiptEpoch(t, store)
		store.Now = nil
		engine.SetTime(now)
		destination := &PlacementDestination{Receipts: store, Discovery: &PlacementDiscovery{CellID: cell, Authority: reader, Inventory: inventory, Snapshot: snapshotReader,
			Paths: &MediaPlacementPaths{Push: &LivePushPlacementPaths{CellID: cell, Registry: &livePathRegistry{}, Snapshot: snapshotReader}},
		}}
		destinations[cell] = destination
		listener, listenErr := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if listenErr != nil {
			t.Fatal(listenErr)
		}
		server := grpc.NewServer(grpc.UnaryInterceptor(middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
			ServiceToken: "ingest-routing-service", MetadataPolicy: middleware.MetadataPolicyDeny,
		})))
		NewFederationServer(FederationServerConfig{ClusterID: cell, ControlCellID: cell, Placement: destination, AllowFederationMutations: true}).RegisterServices(server)
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { server.Stop(); _ = listener.Close() })
		addresses[cell] = listener.Addr().String()
		rpc, rpcErr := foghorn.NewGRPCClient(foghorn.GRPCConfig{GRPCAddr: addresses[cell], ServiceToken: "ingest-routing-service", AllowInsecure: true, Logger: newFederationTestLogger()})
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		t.Cleanup(func() { _ = rpc.Close() })
		pool[cell+"@"+addresses[cell]] = rpc
	}
	routers := make(map[string]balancer.PlacementRouter)
	resolvers := make(map[string]*IngestPlacementResolver)
	for cell, destination := range destinations {
		transport := PlacementTransport{LocalCellID: cell, Local: destination, Client: NewFederationClient(FederationClientConfig{Pool: pool}),
			CellAddress: func(id string) string { return addresses[id] }}
		routers[cell] = transport.Router()
		resolvers[cell] = &IngestPlacementResolver{Authority: reader, Fence: fence, Router: routers[cell]}
		destination.Runtime = &PolicyBoundPlacementRuntime{Policy: &PlacementPolicyGate{CellID: cell, Authority: reader, IngestFence: fence, Router: routers[cell]},
			Media: &PlacementMediaRuntime{Ingest: &LiveIngestPreparationRuntime{CellID: cell, Authority: reader, Snapshot: destination.Discovery.Snapshot}}}
	}
	location := &placement.Coordinates{Latitude: 40.7, Longitude: -74}
	if scenario == "near-eu" {
		location = &placement.Coordinates{Latitude: 52, Longitude: 5}
	}
	if scenario == "near-us-west" {
		location = &placement.Coordinates{Latitude: 37.8, Longitude: -122.4}
	}
	route := balancer.PlacementRouteRequest{TenantID: authority.TenantID, ObjectID: authority.ObjectID, InternalName: authority.InternalName,
		Verb: placement.Ingest, Protocol: protocol, Policy: authority.Policy, PolicyDigest: authority.PolicyDigest, PolicyRevision: authority.PolicyRevision,
		ParentRevision: authority.ParentRevision, Cells: authority.Cells, Location: location}
	if scenario == "eu-owner" {
		route.ActiveIngestClusterID = "eu"
	}
	return routers, route, reads, resolvers
}

func TestPreparedIngestFrontDoorUsesAuthenticatedCells(t *testing.T) {
	for _, scenario := range []string{"near-us", "selfhost-preferred", "eu-owner", "selfhost-unknown"} {
		t.Run(scenario, func(t *testing.T) {
			_, route, _, resolvers := ingestRoutingCells(t, scenario, "whip")
			for _, cell := range []string{"us-cell", "eu-cell"} {
				response, err := control.ResolveIngestEndpoints(t.Context(), &control.IngestDependencies{
					GeoLat: route.Location.Latitude, GeoLon: route.Location.Longitude, Placement: resolvers[cell],
				}, &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "push"}, "owner-key")
				if scenario == "selfhost-unknown" {
					if err == nil || response != nil {
						t.Fatalf("unknown preferred pool emitted endpoints: %v, %v", response, err)
					}
					continue
				}
				want := "us"
				if scenario == "selfhost-preferred" || scenario == "eu-owner" {
					want = "eu"
				}
				if err != nil || response.Primary.ClusterId != want || response.Primary.NodeId != want+"-node" || response.Primary.BaseUrl != "https://"+want+".example" || len(response.Fallbacks) != 0 {
					t.Fatalf("front door changed exact destination: %v, %v", response, err)
				}
				for _, endpoint := range []string{response.Primary.GetWhipUrl(), response.Primary.GetRtmpUrl(), response.Primary.GetSrtUrl()} {
					if !strings.Contains(endpoint, "owner-key") {
						t.Fatalf("unbound or absent confirmed protocol: %s", endpoint)
					}
				}
			}
		})
	}
}

func TestIngestPlacementResolverRechecksAuthorityAfterPreparation(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		_, route, _, resolvers := ingestRoutingCells(t, "near-us", "rtmp")
		resolver := resolvers["us-cell"]
		original := resolver.Authority
		reads := 0
		resolver.Authority = placementAuthorityReaderFunc(func(ctx context.Context, tenant, object, internal string) (localauthority.PlacementPair, error) {
			pair, err := original.Placement(ctx, tenant, object, internal)
			reads++
			if reads == 2 {
				if unavailable {
					return localauthority.PlacementPair{}, errors.New("authority unavailable after preparation")
				}
				pair.Tenant.Version++
			}
			return pair, err
		})
		response, err := resolver.PrepareIngest(t.Context(), control.IngestPlacementRequest{TenantID: "tenant", StreamID: "stream", InternalName: "internal", Protocol: "rtmp", Location: route.Location})
		if err == nil || response.Endpoint != "" || reads != 2 {
			t.Fatalf("stale authority escaped after preparation: %+v, %v, reads=%d", response, err, reads)
		}
	}
}
