package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/triggers"

	"github.com/DATA-DOG/go-sqlmock"
	commodorecli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// These tests unlock the ResolveViewerEndpoint HAPPY paths that wave 2 left at
// ~8% because every successful arm sits behind a SUCCESSFUL control.ResolveContent,
// which reads the concrete exported global control.CommodoreClient. By dialing a
// localhost commodorepb InternalService fake we drive ResolveContent to a real
// resolution and assert the type-dispatch DECISION (which sub-resolver fires) plus
// the billing/payment GATE outcome.
//
// Federation/remote-peer arms (confirmRemoteEndpoint, arrangeOriginPull,
// queryStreamFanOut, resolveRemoteArtifact) are deliberately skipped: they need a
// live federationClient + peerManager channel to a peer cluster, which is out of
// reach of an in-process fake. Reported in the structured output.

// commodoreViewerHappyFake is an in-process Commodore InternalService double whose
// resolution RPCs are settable funcs. Unset RPCs return an empty/not-found
// response (the UnimplementedInternalServiceServer embedding handles the rest with
// codes.Unimplemented, which the resolver treats as "no enrichment").
type commodoreViewerHappyFake struct {
	commodorepb.UnimplementedInternalServiceServer

	artifactPlayback func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error)
	playbackID       func(context.Context, *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error)
	artifactInternal func(context.Context, *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error)
	dvrHash          func(context.Context, *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error)
}

func (f *commodoreViewerHappyFake) ResolveArtifactPlaybackID(ctx context.Context, req *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
	if f.artifactPlayback != nil {
		return f.artifactPlayback(ctx, req)
	}
	return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
}

func (f *commodoreViewerHappyFake) ResolvePlaybackID(ctx context.Context, req *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
	if f.playbackID != nil {
		return f.playbackID(ctx, req)
	}
	return &commodorepb.ResolvePlaybackIDResponse{}, nil
}

func (f *commodoreViewerHappyFake) ResolveArtifactInternalName(ctx context.Context, req *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
	if f.artifactInternal != nil {
		return f.artifactInternal(ctx, req)
	}
	return &commodorepb.ResolveArtifactInternalNameResponse{}, nil
}

func (f *commodoreViewerHappyFake) ResolveDVRHash(ctx context.Context, req *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
	if f.dvrHash != nil {
		return f.dvrHash(ctx, req)
	}
	return &commodorepb.ResolveDVRHashResponse{}, nil
}

// startViewerHappyCommodoreFake serves fake on a localhost gRPC listener, builds a
// real *commodore.GRPCClient against it, and points control.CommodoreClient (read
// by ResolveContent + ResolveDVRArtifactDispatch) at it. Restored on cleanup.
func startViewerHappyCommodoreFake(t *testing.T, fake *commodoreViewerHappyFake) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := commodorecli.NewGRPCClient(commodorecli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("commodore client: %v", err)
	}

	prev := control.CommodoreClient
	control.CommodoreClient = client
	t.Cleanup(func() {
		control.CommodoreClient = prev
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

// billingCacheViewerHappy is a CacheInvalidator whose GetBillingStatus returns a
// settable status. nil status means "no billing info" — the gate is not tripped.
// This is the seam the payment gate inside ResolveViewerEndpoint reads.
type billingCacheViewerHappy struct {
	status *triggers.BillingStatus
}

func (b *billingCacheViewerHappy) InvalidateTenantCache(string) int                 { return 0 }
func (b *billingCacheViewerHappy) InvalidatePlaybackAuthCache(string, []string) int { return 0 }
func (b *billingCacheViewerHappy) GetBillingStatus(context.Context, string, string) *triggers.BillingStatus {
	return b.status
}
func (b *billingCacheViewerHappy) GetClusterPeers(string, string) []*clusterpeerpb.TenantClusterPeer {
	return nil
}

// newViewerHappyManager resets the package-global state manager (the source the
// balancer snapshots) and registers a fresh balancer so each test starts from a
// clean node set. Returns the manager + a balancer wired to it.
func newViewerHappyManager(t *testing.T) (*state.StreamStateManager, *balancer.LoadBalancer) {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	return sm, balancer.NewLoadBalancer(logging.NewLogger())
}

// seedLiveEdgeViewerHappy registers a healthy, probe-verified edge node carrying
// HLS outputs and marks the live stream present+active on it (the balancer's
// presence requirement for a push/live+ stream — without an active input on some
// node every candidate is rejected). Mirrors the control-package live-winner
// recipe. The stream presence is keyed by the BARE internal name.
func seedLiveEdgeViewerHappy(t *testing.T, sm *state.StreamStateManager, nodeID, baseURL, bareInternalName, tenantID string) {
	t.Helper()
	lat, lon := 52.0, 5.0
	sm.SetNodeInfo(nodeID, baseURL, true, &lat, &lon, "loc-"+nodeID, "", map[string]any{"HLS": "/hls/$/index.m3u8"})
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]state.ClassCapacity
	}{
		CPU:     10,
		RAMMax:  16_000_000_000,
		BWLimit: 1_000_000_000,
		UpSpeed: 1_000,
		CapEdge: true,
	})
	sm.TouchNode(nodeID, true)
	sm.SetProbeVerified(nodeID, true)
	sm.SetStreamInstanceInputs(bareInternalName, nodeID, 1)
	if err := sm.UpdateStreamFromBuffer("live+"+bareInternalName, bareInternalName, nodeID, tenantID, "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}
}

// seedStorageArtifactViewerHappy registers an active, probe-verified storage node
// that holds the artifact identified by clipHash and advertises HLS outputs, so
// ResolveArtifactPlayback finds it via FindNodesByArtifactHash and emits a viewer
// endpoint to it.
func seedStorageArtifactViewerHappy(t *testing.T, sm *state.StreamStateManager, nodeID, baseURL, clipHash string) {
	t.Helper()
	lat, lon := 52.0, 5.0
	sm.SetNodeInfo(nodeID, baseURL, true, &lat, &lon, "loc-"+nodeID, "", map[string]any{"HLS": "/hls/$/index.m3u8"})
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]state.ClassCapacity
	}{
		CPU:        10,
		RAMMax:     16_000_000_000,
		BWLimit:    1_000_000_000,
		UpSpeed:    1_000,
		CapEdge:    true,
		CapStorage: true,
	})
	sm.TouchNode(nodeID, true)
	sm.SetProbeVerified(nodeID, true)
	sm.SetNodeArtifacts(nodeID, []*ipcpb.StoredArtifact{
		{ClipHash: clipHash, FilePath: "/data/" + clipHash + ".mp4", StreamName: "vod+art"},
	})
}

// Invariant: a content_id that Commodore resolves to a LIVE stream dispatches to
// resolveLiveViewerEndpoint, and the present+active local edge is returned as the
// primary viewer endpoint with live metadata. This locks the live arm of the
// ResolveViewerEndpoint type switch — the decision that wave 2 could not reach
// without a successful ResolveContent.
func TestResolveViewerEndpoint_LiveDispatchesToLiveWinner(t *testing.T) {
	t.Cleanup(control.SetupTestRegistry("", nil))
	sm, lb := newViewerHappyManager(t)
	seedLiveEdgeViewerHappy(t, sm, "edge-live-1", "https://edge1.example.com", "demo_stream", "tenant-live")

	startViewerHappyCommodoreFake(t, &commodoreViewerHappyFake{
		// Artifact path must MISS so ResolveContent falls through to the live path.
		artifactPlayback: func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
		},
		playbackID: func(_ context.Context, req *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
			if req.GetPlaybackId() != "live-pid" {
				t.Errorf("ResolvePlaybackID got %q, want live-pid", req.GetPlaybackId())
			}
			return &commodorepb.ResolvePlaybackIDResponse{
				InternalName: "live+demo_stream",
				TenantId:     "tenant-live",
				StreamId:     "stream-77",
			}, nil
		},
	})

	s := &FoghornGRPCServer{logger: logrus.New(), lb: lb, originPulling: map[string]struct{}{}}
	resp, err := s.ResolveViewerEndpoint(context.Background(), &sharedpb.ViewerEndpointRequest{ContentId: "live-pid"})
	if err != nil {
		t.Fatalf("live resolve failed: %v", err)
	}
	if resp.GetPrimary() == nil || resp.GetPrimary().GetNodeId() != "edge-live-1" {
		t.Fatalf("primary should be the seeded live edge, got %+v", resp.GetPrimary())
	}
	md := resp.GetMetadata()
	if md == nil || md.GetContentType() != "live" || !md.GetIsLive() {
		t.Fatalf("live metadata expected, got %+v", md)
	}
	if md.GetTenantId() != "tenant-live" {
		t.Fatalf("metadata tenant = %q, want tenant-live", md.GetTenantId())
	}
}

// Invariant: a content_id Commodore resolves to a VOD artifact dispatches to
// resolveArtifactViewerEndpoint and routes to the warm storage node holding the
// artifact's bytes. This locks the clip/vod arm of the type switch and the
// FindNodesByArtifactHash -> viewer-endpoint selection. The foghorn.artifacts
// lifecycle row is served from sqlmock filtered by (hash, type, tenant).
func TestResolveViewerEndpoint_VodDispatchesToStorageNode(t *testing.T) {
	t.Cleanup(control.SetupTestRegistry("", nil))
	sm, lb := newViewerHappyManager(t)
	seedStorageArtifactViewerHappy(t, sm, "store-1", "https://store1.example.com", "vodhash1")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Tenant isolation: the artifacts lifecycle read MUST be scoped by tenant_id;
	// assert the resolved tenant is the third bound arg. storage_cluster_id is
	// returned empty so AuthoritativeClusterServable treats it as local-serveable.
	mock.ExpectQuery(`SELECT COALESCE\(internal_name`).
		WithArgs("vodhash1", "vod", "tenant-vod").
		WillReturnRows(sqlmock.NewRows([]string{
			"internal_name", "status", "duration_seconds", "size_bytes", "created_at",
			"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster",
		}).AddRow("art", "ready", int64(120), int64(1000), time.Now(), "mp4", "node", "synced", false, ""))

	startViewerHappyCommodoreFake(t, &commodoreViewerHappyFake{
		artifactPlayback: func(_ context.Context, req *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			if req.GetPlaybackId() != "vod-pid" {
				t.Errorf("ResolveArtifactPlaybackID got %q, want vod-pid", req.GetPlaybackId())
			}
			return &commodorepb.ResolveArtifactPlaybackIDResponse{
				Found:        true,
				ArtifactHash: "vodhash1",
				InternalName: "art",
				TenantId:     "tenant-vod",
				ContentType:  "vod",
			}, nil
		},
	})

	s := &FoghornGRPCServer{logger: logrus.New(), lb: lb, db: db, originPulling: map[string]struct{}{}}
	resp, err := s.ResolveViewerEndpoint(context.Background(), &sharedpb.ViewerEndpointRequest{ContentId: "vod-pid"})
	if err != nil {
		t.Fatalf("vod resolve failed: %v", err)
	}
	if resp.GetPrimary() == nil || resp.GetPrimary().GetNodeId() != "store-1" {
		t.Fatalf("primary should be the warm storage node, got %+v", resp.GetPrimary())
	}
	md := resp.GetMetadata()
	if md == nil || md.GetContentType() != "vod" {
		t.Fatalf("vod metadata expected, got %+v", md)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// Invariant: a content_id Commodore resolves to a DVR whose dispatch is ACTIVE
// (status=recording with a recording-origin node) routes through the live-style
// edge selector, then has its metadata RELABELED to the DVR identity
// (ContentType=dvr, Status=recording, IsLive still true). This locks the DVR
// active-dispatch arm + the overrideActiveDVRMetadata rewrite — both gated on a
// successful ResolveContent + an active dispatch from control.db.
func TestResolveViewerEndpoint_ActiveDVRRelabelsLiveWinner(t *testing.T) {
	t.Cleanup(control.SetupTestRegistry("", nil))
	sm, lb := newViewerHappyManager(t)
	// The active DVR routes via dvr+<name>; ResolveLivePlayback drops the dvr+
	// prefix and (cold-start) selects any eligible edge, so seed a present edge
	// under the bare name to give it a winner.
	seedLiveEdgeViewerHappy(t, sm, "edge-dvr-1", "https://edgedvr.example.com", "dvrstream", "tenant-dvr")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// control.db owns ResolveDVRArtifactDispatch's status + recording-origin reads.
	prevDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(prevDB) })

	mock.ExpectQuery(`SELECT status\s+FROM foghorn.artifacts`).
		WithArgs("dvrhash1").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
	mock.ExpectQuery(`SELECT node_id, COALESCE\(is_orphaned`).
		WithArgs("dvrhash1").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "is_orphaned"}).AddRow("edge-dvr-1", false))

	startViewerHappyCommodoreFake(t, &commodoreViewerHappyFake{
		// ResolveContent: artifact playback resolves the public id to a DVR artifact.
		artifactPlayback: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			return &commodorepb.ResolveArtifactPlaybackIDResponse{
				Found:        true,
				ArtifactHash: "dvrhash1",
				InternalName: "dvrstream",
				TenantId:     "tenant-dvr",
				ContentType:  "dvr",
			}, nil
		},
		// ResolveDVRArtifactDispatch: internal-name -> dvr artifact, then hash -> dvr.
		artifactInternal: func(_ context.Context, req *commodorepb.ResolveArtifactInternalNameRequest) (*commodorepb.ResolveArtifactInternalNameResponse, error) {
			if req.GetInternalName() != "dvrstream" {
				t.Errorf("ResolveArtifactInternalName got %q, want dvrstream", req.GetInternalName())
			}
			return &commodorepb.ResolveArtifactInternalNameResponse{
				Found:        true,
				ArtifactHash: "dvrhash1",
				InternalName: "dvrstream",
				TenantId:     "tenant-dvr",
				ContentType:  "dvr",
			}, nil
		},
		dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
			return &commodorepb.ResolveDVRHashResponse{
				Found:        true,
				InternalName: "dvrstream",
				TenantId:     "tenant-dvr",
				StreamId:     "stream-dvr",
			}, nil
		},
	})

	s := &FoghornGRPCServer{logger: logrus.New(), lb: lb, db: db, originPulling: map[string]struct{}{}}
	resp, err := s.ResolveViewerEndpoint(context.Background(), &sharedpb.ViewerEndpointRequest{ContentId: "dvr-pid"})
	if err != nil {
		t.Fatalf("active DVR resolve failed: %v", err)
	}
	if resp.GetPrimary() == nil || resp.GetPrimary().GetNodeId() != "edge-dvr-1" {
		t.Fatalf("active DVR should route through the live edge selector, got %+v", resp.GetPrimary())
	}
	md := resp.GetMetadata()
	if md == nil || md.GetContentType() != "dvr" {
		t.Fatalf("active DVR metadata must be relabeled to dvr, got %+v", md)
	}
	if md.GetStatus() != "recording" || md.GetDvrStatus() != "recording" {
		t.Fatalf("relabel must carry recording status, got status=%q dvr_status=%q", md.GetStatus(), md.GetDvrStatus())
	}
	if !md.GetIsLive() {
		t.Fatal("an active DVR surface must stay IsLive=true after relabel")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// Invariant: a viewer whose content owner is a SUSPENDED prepaid tenant, with no
// x402 payment presented, is BLOCKED at ResolveViewerEndpoint with
// FailedPrecondition before any edge selection — playback is gated on the owner's
// billing state, not node availability. This is the hard-block arm of the payment
// gate, reached only when ResolveContent yields a non-empty TenantId.
func TestResolveViewerEndpoint_SuspendedTenantBlocked(t *testing.T) {
	t.Cleanup(control.SetupTestRegistry("", nil))
	sm, lb := newViewerHappyManager(t)
	// Seed a healthy winner so a passing gate would otherwise succeed — proving
	// the gate, not a routing failure, is what rejects.
	seedLiveEdgeViewerHappy(t, sm, "edge-susp-1", "https://s.example.com", "suspstream", "tenant-susp")

	startViewerHappyCommodoreFake(t, &commodoreViewerHappyFake{
		artifactPlayback: func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
		},
		playbackID: func(context.Context, *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
			return &commodorepb.ResolvePlaybackIDResponse{
				InternalName: "live+suspstream",
				TenantId:     "tenant-susp",
			}, nil
		},
	})

	s := &FoghornGRPCServer{
		logger:           logrus.New(),
		lb:               lb,
		originPulling:    map[string]struct{}{},
		cacheInvalidator: &billingCacheViewerHappy{status: &triggers.BillingStatus{TenantID: "tenant-susp", BillingModel: "prepaid", IsSuspended: true}},
	}
	_, err := s.ResolveViewerEndpoint(context.Background(), &sharedpb.ViewerEndpointRequest{ContentId: "live-pid"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("suspended owner: want FailedPrecondition (payment gate), got %v", err)
	}
}

// Invariant: a prepaid owner whose balance is NEGATIVE (soft block, not yet
// hard-suspended), with no x402 payment, is likewise blocked with a payment-
// required FailedPrecondition. Locks that the is_balance_negative warning state on
// a prepaid owner gates new viewer playback.
func TestResolveViewerEndpoint_PrepaidNegativeBalanceBlocked(t *testing.T) {
	t.Cleanup(control.SetupTestRegistry("", nil))
	sm, lb := newViewerHappyManager(t)
	seedLiveEdgeViewerHappy(t, sm, "edge-neg-1", "https://n.example.com", "negstream", "tenant-neg")

	startViewerHappyCommodoreFake(t, &commodoreViewerHappyFake{
		artifactPlayback: func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
		},
		playbackID: func(context.Context, *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
			return &commodorepb.ResolvePlaybackIDResponse{
				InternalName: "live+negstream",
				TenantId:     "tenant-neg",
			}, nil
		},
	})

	s := &FoghornGRPCServer{
		logger:           logrus.New(),
		lb:               lb,
		originPulling:    map[string]struct{}{},
		cacheInvalidator: &billingCacheViewerHappy{status: &triggers.BillingStatus{TenantID: "tenant-neg", BillingModel: "prepaid", IsBalanceNegative: true}},
	}
	_, err := s.ResolveViewerEndpoint(context.Background(), &sharedpb.ViewerEndpointRequest{ContentId: "live-pid"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("prepaid negative balance: want FailedPrecondition (payment gate), got %v", err)
	}
}

// Invariant: a prepaid owner in GOOD standing (not suspended, balance positive)
// does NOT trip the payment gate — resolution proceeds to live edge selection and
// returns the healthy winner. Negative control proving the gate keys on the
// billing FLAGS, not merely on TenantId being populated.
func TestResolveViewerEndpoint_HealthyPrepaidProceeds(t *testing.T) {
	t.Cleanup(control.SetupTestRegistry("", nil))
	sm, lb := newViewerHappyManager(t)
	seedLiveEdgeViewerHappy(t, sm, "edge-ok-1", "https://ok.example.com", "okstream", "tenant-ok")

	startViewerHappyCommodoreFake(t, &commodoreViewerHappyFake{
		artifactPlayback: func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
		},
		playbackID: func(context.Context, *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
			return &commodorepb.ResolvePlaybackIDResponse{
				InternalName: "live+okstream",
				TenantId:     "tenant-ok",
			}, nil
		},
	})

	s := &FoghornGRPCServer{
		logger:           logrus.New(),
		lb:               lb,
		originPulling:    map[string]struct{}{},
		cacheInvalidator: &billingCacheViewerHappy{status: &triggers.BillingStatus{TenantID: "tenant-ok", BillingModel: "prepaid"}},
	}
	resp, err := s.ResolveViewerEndpoint(context.Background(), &sharedpb.ViewerEndpointRequest{ContentId: "live-pid"})
	if err != nil {
		t.Fatalf("healthy prepaid must not be gated, got %v", err)
	}
	if resp.GetPrimary() == nil || resp.GetPrimary().GetNodeId() != "edge-ok-1" {
		t.Fatalf("healthy prepaid should resolve to the live edge, got %+v", resp.GetPrimary())
	}
}
