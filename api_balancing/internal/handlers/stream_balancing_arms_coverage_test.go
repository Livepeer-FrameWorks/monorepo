package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	commodorecli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

// This file drives HandleGenericViewerPlayback (the /play/* and /resolve/*
// viewer-resolution entrypoint, 0% before this wave). It is the "viewer
// playback success -> manifest path + node redirect" arm of the routing tree.
// Each sub-test locks ONE routing/resolution decision: the early-exit
// validation gates and the artifact (VOD) success lane that returns a JSON
// endpoint set or a protocol-specific 307 redirect to the storage edge.
//
// Scope / reachability notes (reported honestly):
//   - The live lane (resolveLiveViewerEndpoint) and the remote_redirect arm of
//     handleStreamBalancing both depend on either a federation QueryStream RPC
//     stack (federationClient + peerManager + a FoghornFederation gRPC server)
//     or on triggerProcessor.GetClusterPeers returning peers. The latter reads
//     an UNEXPORTED triggers.streamContext out of the processor's stream cache;
//     there is no exported seam to seed it from the handlers package, so the
//     trigger-cache path to remote-edge scoring is unreachable from a
//     handlers-package unit test. The VOD lane below avoids both and exercises
//     the real success path (resolution -> artifacts row -> warm-node selection
//     -> output building -> redirect) end to end.
//   - The active-replication-pinned -> remote DTSC arm and the local-edge
//     viewer-selection / proto-redirect / localhost-fallback / 402-billing /
//     fixed-node arms of handleStreamBalancing are already covered by wave 2/3
//     (stream_balancing_coverage_test.go, balancing_commodore_coverage_test.go),
//     so they are intentionally NOT duplicated here.

// commodoreArmsFake is a Commodore InternalService double whose artifact and vod
// resolution RPCs are settable funcs; the rest fall through to
// UnimplementedInternalServiceServer (which errors, treated by ResolveContent as
// "not this kind of content"). Suffix -Arms keeps it distinct from the wave-3
// commodoreBalancingFake in the same package.
type commodoreArmsFake struct {
	commodorepb.UnimplementedInternalServiceServer

	artifactPlaybackID func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error)
	vodHash            func(context.Context, *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error)
}

func (f *commodoreArmsFake) ResolveArtifactPlaybackID(ctx context.Context, req *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
	if f.artifactPlaybackID != nil {
		return f.artifactPlaybackID(ctx, req)
	}
	return &commodorepb.ResolveArtifactPlaybackIDResponse{}, nil
}

func (f *commodoreArmsFake) ResolveVodHash(ctx context.Context, req *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
	if f.vodHash != nil {
		return f.vodHash(ctx, req)
	}
	return &commodorepb.ResolveVodHashResponse{}, nil
}

// startCommodoreFakeArms wires control.CommodoreClient (the global ResolveContent
// + ResolveArtifactPlayback read) at a real gRPC client dialing an in-process
// fake. Restored on cleanup.
func startCommodoreFakeArms(t *testing.T, fake *commodoreArmsFake) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, clientErr := commodorecli.NewGRPCClient(commodorecli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if clientErr != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("commodore client: %v", clientErr)
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

// withMockDBArms installs a sqlmock-backed *sql.DB as the handlers package-global
// `db` for the duration of the test. resolveArtifactViewerEndpoint queries
// foghorn.artifacts through this global. Restored on cleanup.
func withMockDBArms(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prev := db
	db = mockDB
	t.Cleanup(func() {
		db = prev
		_ = mockDB.Close()
	})
	return mock
}

// seedArtifactEdgeArms makes nodeID/host an ACTIVE storage edge that holds the
// artifact identified by clipHash AND advertises outputs (so GetNodeOutputs
// returns a populated map and an endpoint can be built). The node must be
// probe-verified (active) because FindNodesByArtifactHash skips inactive nodes.
func seedArtifactEdgeArms(t *testing.T, sm *state.StreamStateManager, nodeID, host, baseURL, clipHash string, outputs map[string]any) {
	t.Helper()
	lat, lon := 0.0, 0.0
	sm.SetNodeInfo(nodeID, host, true, &lat, &lon, "loc", "", outputs)
	sm.TouchNode(nodeID, true)
	sm.SetProbeVerified(nodeID, true)
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
	}{RAMMax: 100, RAMCurrent: 10, UpSpeed: 1000, DownSpeed: 2000, BWLimit: 1_000_000, CapStorage: true, CapEdge: true})
	// GetNodeOutputs reads ns.BaseURL; SetNodeInfo above sets it to host, so
	// override with the playable base URL the outputs templates resolve against.
	sm.SetNodeInfo(nodeID, baseURL, true, &lat, &lon, "loc", "", outputs)
	sm.SetNodeArtifacts(nodeID, []*ipcpb.StoredArtifact{
		{ClipHash: clipHash, FilePath: "/data/" + clipHash + ".mp4", StreamName: "vod+art"},
	})
}

// playbackCtxArms builds a gin context for HandleGenericViewerPlayback by
// setting the wildcard "path" param the route binds (c.Param("path")).
func playbackCtxArms(t *testing.T, pathParam string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/play/"+pathParam, nil)
	c.Params = gin.Params{{Key: "path", Value: pathParam}}
	return c, w
}

// Invariant: an empty view-key path is rejected at the front door with 400
// MISSING_VIEW_KEY before any resolution is attempted. Locks the entrypoint
// validation gate.
func TestGenericViewerPlayback_MissingViewKeyReturns400(t *testing.T) {
	balancingTestEnv(t)
	t.Cleanup(control.SetupTestRegistry("", nil))

	c, w := playbackCtxArms(t, "")
	HandleGenericViewerPlayback(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing view key)", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["code"] != "MISSING_VIEW_KEY" {
		t.Fatalf("code = %v, want MISSING_VIEW_KEY", body["code"])
	}
}

// Invariant: a path whose first segment parses to an empty view key (e.g. a
// bare ".hls" extension with no key) is rejected with 400 INVALID_VIEW_KEY.
// Locks the post-parse validation gate distinct from the missing-path gate.
func TestGenericViewerPlayback_InvalidViewKeyReturns400(t *testing.T) {
	balancingTestEnv(t)
	t.Cleanup(control.SetupTestRegistry("", nil))

	// parsePlaybackPath splits on "." so ".hls" yields viewKey="" protocol="hls".
	c, w := playbackCtxArms(t, ".hls")
	HandleGenericViewerPlayback(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (invalid view key)", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["code"] != "INVALID_VIEW_KEY" {
		t.Fatalf("code = %v, want INVALID_VIEW_KEY", body["code"])
	}
}

// Invariant: a view key that Commodore cannot resolve to any content yields a
// 404 VIEW_KEY_NOT_FOUND. Locks the resolution-failure decision (control.
// ResolveContent returns an error -> the viewer gets not-found, never a 5xx or
// a silent fallback).
func TestGenericViewerPlayback_UnresolvableViewKeyReturns404(t *testing.T) {
	balancingTestEnv(t)
	t.Cleanup(control.SetupTestRegistry("", nil))

	// All resolution RPCs return not-found, so ResolveContent exhausts every
	// lane and errors.
	startCommodoreFakeArms(t, &commodoreArmsFake{
		artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
		},
	})

	c, w := playbackCtxArms(t, "ghostkey")
	HandleGenericViewerPlayback(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (unresolvable view key)", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["code"] != "VIEW_KEY_NOT_FOUND" {
		t.Fatalf("code = %v, want VIEW_KEY_NOT_FOUND", body["code"])
	}
}

// expectArtifactRowArms registers the foghorn.artifacts SELECT
// resolveArtifactPlaybackWithResp runs, returning a synced VOD row whose
// authoritative cluster is empty (so AuthoritativeClusterServable passes for the
// local cluster). The 10 scanned columns are: internal_name, status,
// duration_seconds, size_bytes, created_at, format, storage_location,
// sync_status, has_thumbnails, authoritative_cluster.
func expectArtifactRowArms(mock sqlmock.Sqlmock, hash string) {
	rows := sqlmock.NewRows([]string{
		"internal_name", "status", "duration_seconds", "size_bytes",
		"created_at", "format", "storage_location", "sync_status",
		"has_thumbnails", "authoritative_cluster",
	}).AddRow(
		"art", "ready", int64(120), int64(4096),
		time.Now(), "mp4", "node", "synced",
		false, "",
	)
	mock.ExpectQuery(`FROM foghorn\.artifacts`).
		WithArgs(hash, "vod", "tenant-vod").
		WillReturnRows(rows)
}

// Invariant: a VOD whose artifact resolves to a hash present on a healthy local
// storage edge, requested WITHOUT a protocol, returns 200 with a JSON viewer
// endpoint whose Primary points at that edge. This is the viewer-playback
// success arm (the "any/manifest" JSON shape). Asserts the routing OUTCOME: the
// selected node id and a populated outputs map (the manifest surface).
func TestGenericViewerPlayback_VodJSONSuccessReturnsEndpoint(t *testing.T) {
	sm := balancingTestEnv(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	mock := withMockDBArms(t)

	const hash = "vodhashaaaaaaaaaaaaaaaaaaaaaaaa1"
	seedArtifactEdgeArms(t, sm, "store-json", "store-json.example", "https://store-json.example/", hash,
		map[string]any{"HLS": "https://store-json.example/hls/$/index.m3u8"})

	startCommodoreFakeArms(t, &commodoreArmsFake{
		artifactPlaybackID: func(_ context.Context, req *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			return &commodorepb.ResolveArtifactPlaybackIDResponse{
				Found:        true,
				ArtifactHash: hash,
				InternalName: "art",
				TenantId:     "tenant-vod",
				StreamId:     "stream-vod",
				ContentType:  "vod",
			}, nil
		},
		vodHash: func(_ context.Context, _ *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
			return &commodorepb.ResolveVodHashResponse{Found: true, TenantId: "tenant-vod", Title: "My VOD"}, nil
		},
	})
	expectArtifactRowArms(mock, hash)

	c, w := playbackCtxArms(t, "vodkey")
	HandleGenericViewerPlayback(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (VOD JSON success); body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Primary struct {
			NodeId  string         `json:"nodeId"`
			Outputs map[string]any `json:"outputs"`
		} `json:"primary"`
		Metadata struct {
			ContentType string `json:"contentType"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal endpoint JSON %q: %v", w.Body.String(), err)
	}
	if resp.Primary.NodeId != "store-json" {
		t.Fatalf("primary node = %q, want store-json", resp.Primary.NodeId)
	}
	if len(resp.Primary.Outputs) == 0 {
		t.Fatalf("primary outputs empty, want a populated manifest/output map")
	}
	if resp.Metadata.ContentType != "vod" {
		t.Fatalf("content type = %q, want vod", resp.Metadata.ContentType)
	}
}

// Invariant: the SAME VOD requested WITH an explicit protocol (hls) is answered
// with a 307 redirect to the storage edge's HLS output URL, not a JSON body.
// Locks the protocol-redirect form of the viewer-playback success arm: the
// resolved Primary.Outputs[HLS] URL becomes the Location.
func TestGenericViewerPlayback_VodProtocolRedirectsToEdge(t *testing.T) {
	sm := balancingTestEnv(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	mock := withMockDBArms(t)

	const hash = "vodhashbbbbbbbbbbbbbbbbbbbbbbbb2"
	seedArtifactEdgeArms(t, sm, "store-hls", "store-hls.example", "https://store-hls.example/", hash,
		map[string]any{"HLS": "https://store-hls.example/hls/$/index.m3u8"})

	startCommodoreFakeArms(t, &commodoreArmsFake{
		artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
			return &commodorepb.ResolveArtifactPlaybackIDResponse{
				Found:        true,
				ArtifactHash: hash,
				InternalName: "art2",
				TenantId:     "tenant-vod",
				StreamId:     "stream-vod",
				ContentType:  "vod",
			}, nil
		},
		vodHash: func(_ context.Context, _ *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
			return &commodorepb.ResolveVodHashResponse{Found: true, TenantId: "tenant-vod"}, nil
		},
	})
	expectArtifactRowArms(mock, hash)

	// "vodkey/hls" -> parsePlaybackPath: viewKey=vodkey, protocol=hls.
	c, w := playbackCtxArms(t, "vodkey2/hls")
	HandleGenericViewerPlayback(c)

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307 (VOD protocol redirect); body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatalf("Location header empty, want HLS edge URL")
	}
	// The redirect must target the storage edge's resolved HLS output, not the
	// localhost fallback or a foreign host.
	if want := "https://store-hls.example/hls/"; loc[:len(want)] != want {
		t.Fatalf("Location = %q, want prefix %q (storage edge HLS output)", loc, want)
	}
}
