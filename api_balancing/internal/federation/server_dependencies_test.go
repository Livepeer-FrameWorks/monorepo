package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"

	"github.com/DATA-DOG/go-sqlmock"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func servingFederationConfig(t *testing.T) FederationServerConfig {
	t.Helper()
	cache, _ := setupTestCache(t)
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return FederationServerConfig{
		Logger:                   testLogger(),
		LB:                       balancer.NewLoadBalancer(testLogger()),
		ClusterID:                "cluster-a",
		Cache:                    cache,
		DB:                       db,
		PeerManager:              unknownPeers{},
		FedClient:                &FederationClient{},
		Placement:                &PlacementDestination{},
		ClipCreator:              noopClipCreator{},
		DVRCreator:               noopDVRCreator{},
		ArtifactHandler:          &artifactCommandSpy{},
		AdvertisedBacking:        func(context.Context, string, string) (S3Backing, bool) { return S3Backing{}, false },
		IsServedCluster:          func(string) bool { return false },
		AllowFederationMutations: true,
	}
}

// A server that accepts peer RPCs refuses to be built without any dependency a
// peer-authority check or a peer call relies on, including a nil pointer held
// in an interface field.
func TestFederationServerConfigValidateRequiresServingDependencies(t *testing.T) {
	if err := servingFederationConfig(t).Validate(); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}

	var nilPeerManager *PeerManager
	cases := map[string]func(*FederationServerConfig){
		"Logger":            func(c *FederationServerConfig) { c.Logger = nil },
		"LB":                func(c *FederationServerConfig) { c.LB = nil },
		"Cache":             func(c *FederationServerConfig) { c.Cache = nil },
		"DB":                func(c *FederationServerConfig) { c.DB = nil },
		"PeerManager":       func(c *FederationServerConfig) { c.PeerManager = nil },
		"typed nil manager": func(c *FederationServerConfig) { c.PeerManager = nilPeerManager },
		"FedClient":         func(c *FederationServerConfig) { c.FedClient = nil },
		"Placement":         func(c *FederationServerConfig) { c.Placement = nil },
		"ClipCreator":       func(c *FederationServerConfig) { c.ClipCreator = nil },
		"DVRCreator":        func(c *FederationServerConfig) { c.DVRCreator = nil },
		"ArtifactHandler":   func(c *FederationServerConfig) { c.ArtifactHandler = nil },
		"AdvertisedBacking": func(c *FederationServerConfig) { c.AdvertisedBacking = nil },
		"IsServedCluster":   func(c *FederationServerConfig) { c.IsServedCluster = nil },
		"ClusterID":         func(c *FederationServerConfig) { c.ClusterID = " " },
		"address-only peer resolver": func(c *FederationServerConfig) {
			c.PeerManager = staticPeerAddrs{}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := servingFederationConfig(t)
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("config without %s validated", name)
			}
		})
	}

	disabled := FederationServerConfig{Logger: testLogger(), ClusterID: "cluster-a"}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("a server that serves no peer RPCs needs no peer dependencies: %v", err)
	}
}

type staticPeerAddrs struct{}

func (staticPeerAddrs) GetPeerAddr(string) string { return "" }

// Each guarded path refuses when its dependency is missing instead of skipping
// the check: an unattributable lifecycle event is not applied, an advertisement
// naming a cell is not filed, and a pointer retirement reports that it could not
// run rather than answering as if there were no pointer.
func TestFederationServerMissingPeerAuthorityFailsClosed(t *testing.T) {
	var nilPeerManager *PeerManager
	for name, peers := range map[string]PeerAddrResolver{
		"no peer manager":       nil,
		"typed nil manager":     nilPeerManager,
		"address-only resolver": staticPeerAddrs{},
	} {
		noResolver := isNilDependency(peers)
		t.Run(name, func(t *testing.T) {
			cache, _ := setupTestCache(t)
			db, _, mockErr := sqlmock.New()
			if mockErr != nil {
				t.Fatalf("sqlmock.New: %v", mockErr)
			}
			t.Cleanup(func() { _ = db.Close() })
			srv := NewFederationServer(FederationServerConfig{
				Logger: testLogger(), ClusterID: "cluster-a", Cache: cache, DB: db,
				PeerManager: peers, FedClient: &FederationClient{}, AllowFederationMutations: true,
			})

			ctx := context.Background()
			srv.handleStreamLifecycle(ctx, "cluster-b", &foghornfederationpb.StreamLifecycleEvent{
				InternalName: "stream", TenantId: "tenant-a", ClusterId: "cluster-b", IsLive: true, SourceRevision: 1,
			})
			if live, err := cache.GetRemoteLiveStream(ctx, "tenant-a", "stream"); err != nil || live != nil {
				t.Fatalf("lifecycle accepted without a tenant authority: live=%+v err=%v", live, err)
			}

			prev := control.StreamRegistryInstance
			control.StreamRegistryInstance = control.NewStreamRegistry(nil, "cluster-a", time.Minute)
			t.Cleanup(func() { control.StreamRegistryInstance = prev })
			channelCell := ""
			srv.handleStreamAdvertisement(ctx, "cluster-b", &foghornfederationpb.StreamAdvertisement{
				InternalName: "stream", TenantId: "tenant-a", ControlCellId: "cell-b", IsLive: true, Timestamp: time.Now().Unix(),
			}, &channelCell)
			if _, err := control.StreamRegistryInstance.ResolveSourceByInternalName(ctx, "stream"); err == nil {
				t.Fatal("advertisement filed without a cell authority")
			}

			if !noResolver {
				return
			}
			resp, err := srv.ForwardArtifactCommand(serviceAuthContext(), &foghornfederationpb.ForwardArtifactCommandRequest{
				Command: RetireArtifactPointerCommand, ArtifactHash: "hash", TenantId: "tenant-a",
			})
			if status.Code(err) != codes.FailedPrecondition || resp != nil {
				t.Fatalf("retire without a peer resolver = %+v, %v; want FailedPrecondition", resp, err)
			}

			migrate, err := srv.MigrateArtifactMetadata(serviceAuthContext(), &foghornfederationpb.MigrateArtifactMetadataRequest{
				TenantId: "tenant-a", SourceClusterId: "cluster-b",
			})
			if status.Code(err) != codes.Internal || migrate != nil {
				t.Fatalf("migration without a peer resolver = %+v, %v; want a refusal", migrate, err)
			}
		})
	}
}
