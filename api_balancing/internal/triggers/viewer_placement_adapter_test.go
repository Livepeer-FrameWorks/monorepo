package triggers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

type viewerNamedAuthorityFunc func(context.Context, string, string) (localauthority.PlacementPair, error)

func (f viewerNamedAuthorityFunc) PlacementForInternalName(ctx context.Context, tenant, internal string) (localauthority.PlacementPair, error) {
	return f(ctx, tenant, internal)
}

type viewerSourceGenerationFunc func(context.Context, balancer.PlacementAuthority) (string, time.Time, error)

func (f viewerSourceGenerationFunc) ResolveSourceGeneration(ctx context.Context, authority balancer.PlacementAuthority) (string, time.Time, error) {
	return f(ctx, authority)
}

func TestViewerPlacementAdapterTrustedInputsAndLifetime(t *testing.T) {
	for _, scenario := range []string{
		"valid", "mapped IPv4", "unknown geo unbounded", "unknown geo bounded", "distant geo", "invalid geo",
		"hostname", "address with port", "zone", "empty address", "unknown connector", "HTTP only", "publisher connector",
		"missing runtime", "authority error", "foreign tenant", "foreign name", "not ready", "source error", "empty generation",
		"expired generation", "unbounded generation", "canceled", "canceled authority", "canceled source",
		"tenant changed after source", "object changed after source",
		"source changed after decision", "source disappeared after decision", "source expiry shortened", "source canceled after decision",
	} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now()
			pair := viewerAdapterPair(now)
			connection := ViewerPlacementConnection{TenantID: "tenant", InternalName: "stream", ClusterID: "us", NodeID: "edge", Connector: "HTTP,HLS", ClientAddress: "203.0.113.1"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var observed atomic.Int32
			var reads, sources int
			sourceUntil := now.Add(10 * time.Second)
			adapter := &ViewerPlacementAdapter{GeoIP: &geoip.Reader{}, GeoCache: cache.New(cache.Options{TTL: time.Minute, MaxEntries: 16}, cache.MetricsHooks{})}
			adapter.GeoCache.Set(connection.ClientAddress, &geoip.GeoData{Latitude: 40.7, Longitude: -74}, time.Minute)
			adapter.Authority = viewerNamedAuthorityFunc(func(readCtx context.Context, tenant, internal string) (localauthority.PlacementPair, error) {
				reads++
				if tenant != connection.TenantID || internal != connection.InternalName {
					t.Fatal("authority lookup lost connection scope")
				}
				if deadline, ok := readCtx.Deadline(); !ok || time.Until(deadline) > localauthority.PlacementReadTimeout {
					t.Fatal("authority lookup is unbounded")
				}
				if scenario == "authority error" {
					return localauthority.PlacementPair{}, errors.New("authority unavailable")
				}
				if scenario == "canceled authority" {
					cancel()
				}
				return pair, nil
			})
			adapter.Source = viewerSourceGenerationFunc(func(sourceCtx context.Context, authority balancer.PlacementAuthority) (string, time.Time, error) {
				sources++
				if sources == 2 {
					switch scenario {
					case "source changed after decision":
						return "replacement-generation", sourceUntil, nil
					case "source disappeared after decision":
						return "", time.Time{}, errors.New("source withdrawn")
					case "source expiry shortened":
						sourceUntil = now.Add(5 * time.Second)
					case "source canceled after decision":
						cancel()
					}
				}
				if authority.TenantID != "tenant" || authority.ObjectID != "live_stream:stream" || authority.InternalName != "stream" || authority.Verb != placement.Serve {
					t.Fatal("source lookup did not use compiled object identity")
				}
				if deadline, ok := sourceCtx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
					t.Fatal("source lookup is unbounded")
				}
				switch scenario {
				case "source error":
					return "", time.Time{}, errors.New("source unavailable")
				case "empty generation":
					return "", sourceUntil, nil
				case "expired generation":
					return "owner-generation", now.Add(-time.Second), nil
				case "unbounded generation":
					return "owner-generation", authority.ExpiresAt.Add(time.Second), nil
				case "canceled source":
					cancel()
				case "tenant changed after source", "object changed after source":
					updated := pair
					if scenario == "tenant changed after source" {
						updated.Tenant.Version++
					} else {
						updated.Object.Version++
					}
					adapter.Gate.Authority = viewerPlacementPairReader{pair: updated}
				}
				return "owner-generation", sourceUntil, nil
			})
			adapter.Gate = &federation.PlacementPolicyGate{CellID: "us-cell", Authority: viewerPlacementPairReader{pair: pair}, Router: balancer.PlacementRouter{
				Observe: func(_ context.Context, cell balancer.PlacementCell, route balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
					observed.Add(1)
					if route.Protocol != "hls" || route.SourceGeneration != "owner-generation" || cell.ID != "us-cell" || route.ObjectID != "live_stream:stream" {
						t.Error("gate did not receive canonical protocol and owner-derived generation")
					}
					if scenario == "valid" || scenario == "mapped IPv4" {
						if route.Location == nil || *route.Location != (placement.Coordinates{Latitude: 40.7, Longitude: -74}) {
							t.Error("gate lost trusted geography")
						}
					}
					if scenario == "unknown geo unbounded" || scenario == "unknown geo bounded" {
						if route.Location != nil {
							t.Error("unknown geography was fabricated")
						}
					}
					until := now.Add(15 * time.Second)
					return balancer.PlacementCellObservation{Complete: true, ObservedAt: now, ExpiresAt: until, Candidates: []placement.Candidate{{
						TenantID: "tenant", ClusterID: "us", NodeID: "edge", OwnerTenantID: "tenant", AllowedVerbs: []placement.Verb{placement.Serve},
						ObservedAt: now, ExpiresAt: until, Capacity: placement.CapacityAvailable, BWAvailable: 100, BWLimit: 1000,
						RAMUsed: 1, RAMMax: 100, Presence: placement.Present, SourceFeasible: true, Location: &placement.Coordinates{Latitude: 40.7, Longitude: -74},
					}}}, nil
				}}}
			want := scenario == "valid" || scenario == "mapped IPv4" || scenario == "unknown geo unbounded" || scenario == "source expiry shortened"
			preReadFailure := false
			switch scenario {
			case "mapped IPv4":
				connection.ClientAddress = "::ffff:203.0.113.1"
			case "unknown geo unbounded", "unknown geo bounded":
				adapter.GeoIP = nil
				if scenario == "unknown geo unbounded" {
					pair.Tenant.Authority.MediaPlacement.Serve.Preferences.Groups[0].MaxDistanceKm = 0
				}
			case "distant geo":
				adapter.GeoCache.Set(connection.ClientAddress, &geoip.GeoData{Latitude: 52, Longitude: 4}, time.Minute)
			case "invalid geo":
				adapter.GeoCache.Set(connection.ClientAddress, &geoip.GeoData{Latitude: 91}, time.Minute)
			case "hostname", "address with port", "zone", "empty address":
				connection.ClientAddress = map[string]string{"hostname": "edge.example", "address with port": "203.0.113.1:443", "zone": "fe80::1%en0", "empty address": ""}[scenario]
				preReadFailure = true
			case "unknown connector", "HTTP only", "publisher connector":
				connection.Connector = map[string]string{"unknown connector": "other", "HTTP only": "HTTP", "publisher connector": "INPUT:HLS"}[scenario]
				preReadFailure = true
			case "missing runtime":
				adapter.Source = nil
				preReadFailure = true
			case "foreign tenant":
				connection.TenantID = "another"
			case "foreign name":
				connection.InternalName = "another"
			case "not ready":
				pair.Object.Ready = false
			case "canceled":
				cancel()
				preReadFailure = true
			}
			decision, err := adapter.AdmitViewer(ctx, connection)
			if (err == nil) != want {
				t.Fatalf("unexpected admission: %+v %v", decision, err)
			}
			if !want && decision != (federation.PlacementAdmissionDecision{}) {
				t.Fatalf("failed admission returned usable decision: %+v", decision)
			}
			if want && (decision.Protocol != "hls" || decision.SourceGeneration != "owner-generation" || !decision.ExpiresAt.Equal(sourceUntil) || observed.Load() != 1) {
				t.Fatalf("decision lost source/protocol/expiry: %+v observations=%d", decision, observed.Load())
			}
			if want && (decision.TenantAuthorityVersion != pair.Tenant.Version || decision.ObjectAuthorityVersion != pair.Object.Version) {
				t.Fatalf("viewer decision lost signed source authority: %+v", decision)
			}
			if want && sources != 2 {
				t.Fatalf("viewer admission did not recheck source after global observation: %d", sources)
			}
			if (scenario == "tenant changed after source" || scenario == "object changed after source") && observed.Load() != 0 {
				t.Fatal("stale source authority reached global placement observation")
			}
			if preReadFailure && (reads != 0 || sources != 0 || observed.Load() != 0) {
				t.Fatalf("invalid connection touched authority/source/discovery: %d/%d/%d", reads, sources, observed.Load())
			}
			if (scenario == "authority error" || scenario == "foreign tenant" || scenario == "foreign name" || scenario == "not ready" || scenario == "canceled authority") && (sources != 0 || observed.Load() != 0) {
				t.Fatal("invalid authority reached source/discovery")
			}
		})
	}
}

func viewerAdapterPair(now time.Time) localauthority.PlacementPair {
	return localauthority.PlacementPair{
		Tenant: localauthority.TenantSnapshot{Version: 1, Ready: true, ValidUntil: now.Add(20 * time.Second), Authority: &mediapb.TenantAuthority{
			SchemaVersion: 2, TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
			MediaPlacement:         &pb.PolicySet{Revision: 1, Serve: &pb.Rules{SchemaVersion: 1, Preferences: &pb.Preferences{Groups: []*pb.Group{{Id: "nearby", Match: &pb.Selector{}, MaxDistanceKm: 100}}}}},
			EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "us", ControlCellId: "us-cell", ClusterClass: "tenant_private", OwnerTenantId: "tenant", SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowServe: true}}},
		}},
		Object: localauthority.MediaObjectSnapshot{Version: 1, AuthorityID: "live_stream:stream", Ready: true, ValidUntil: now.Add(20 * time.Second), Authority: &mediapb.MediaObjectAuthority{
			SchemaVersion: 2, TenantId: "tenant", InternalName: "stream", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, MediaPlacement: &pb.PolicySet{}, PlacementTenantRevision: 1,
			Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "push"}},
		}},
	}
}
