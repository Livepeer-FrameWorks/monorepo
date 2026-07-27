package control

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// fileArtifactRows builds the 12-column row fillFileArtifactResolve scans.
func fileArtifactRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"s3_url", "size_bytes", "format", "dtsh_synced", "stream_internal_name",
		"sync_status", "origin_cluster_id", "storage_cluster_id", "tenant_id", "artifact_type",
		"active_dtsh_key", "durable_backend_local",
	})
}

// TestFillFileArtifactResolve pins the synced-vs-fallback decision tree: only a
// row whose bytes are durably synced to S3 may serve a presigned media URL; a
// stale/pending sync must fall through to peer-relay rather than hand back the
// (possibly stale) upload. Missing rows are a silent 404, not an error.
func TestFillFileArtifactResolve(t *testing.T) {
	ctx := context.Background()
	log := logging.NewLogger()

	t.Run("nil DB errors", func(t *testing.T) {
		prev := db
		db = nil
		t.Cleanup(func() { db = prev })
		resp := &ipcpb.RelayResolveResponse{}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, "node-1", log)
		if resp.GetError() == "" {
			t.Fatal("nil DB must set an error")
		}
	})

	t.Run("no row is silent not-found", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").WillReturnError(sql.ErrNoRows)
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod"}, resp, "node-1", log)
		if resp.GetError() != "" {
			t.Fatalf("missing row must not error, got %q", resp.GetError())
		}
		if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING {
			t.Fatalf("state must stay source-missing, got %s", resp.GetState())
		}
	})

	t.Run("db error sets error", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").WillReturnError(sql.ErrConnDone)
		resp := &ipcpb.RelayResolveResponse{}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod"}, resp, "node-1", log)
		if resp.GetError() == "" {
			t.Fatal("db error must set an error")
		}
	})

	t.Run("synced row serves presigned media", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		// The requesting node is a platform/shared edge: no tenant, on a cluster THIS Foghorn operates → entitled.
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(func() { state.ResetDefaultManagerForTests() })
		sm.SetNodeInfo("node-1", "n", true, nil, nil, "", "", nil)
		sm.SetNodeConnectionInfo(context.Background(), "node-1", "n", "", "platform-eu", nil)
		AddPlatformSharedCluster("platform-eu")
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").
			WillReturnRows(fileArtifactRows().AddRow(
				"s3://bucket/key.mp4", int64(1234), "mp4", false, "stream1",
				"synced", "", "", "t1", "vod", "", true))
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod"}, resp, "node-1", log)
		if resp.GetState() != ipcpb.AssetState_ASSET_STATE_PLAYABLE {
			t.Fatalf("synced row must be playable, got %s err=%q", resp.GetState(), resp.GetError())
		}
		if resp.GetMediaPresignedUrl() == "" {
			t.Fatal("synced row must mint a media presigned URL")
		}
		if resp.GetExpectedSizeBytes() != 1234 || resp.GetContentType() != "video/mp4" {
			t.Fatalf("size/content-type not populated: size=%d ct=%q", resp.GetExpectedSizeBytes(), resp.GetContentType())
		}
	})

	t.Run("dedicated node is denied another tenant's artifact (no presigned URL leak)", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		// The requesting node is DEDICATED to tenant-a; the artifact belongs to tenant-b.
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(func() { state.ResetDefaultManagerForTests() })
		sm.SetNodeInfo("byoc-a-node", "n", true, nil, nil, "", "", nil)
		sm.SetNodeConnectionInfo(context.Background(), "byoc-a-node", "n", "tenant-a", "", nil)
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").
			WillReturnRows(fileArtifactRows().AddRow(
				"s3://bucket/key.mp4", int64(1234), "mp4", false, "stream1",
				"synced", "", "", "tenant-b", "vod", "", true))
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod"}, resp, "byoc-a-node", log)
		if resp.GetMediaPresignedUrl() != "" {
			t.Fatal("a tenant-a node must NOT receive a presigned URL for a tenant-b artifact")
		}
		if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING {
			t.Fatalf("cross-tenant request must stay source-missing, got %s", resp.GetState())
		}
	})

	t.Run("unsynced row falls through to peer relay (and 404s without a local origin)", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		// Artifact row present but sync_status is pending: must NOT serve the s3_url.
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").
			WillReturnRows(fileArtifactRows().AddRow(
				"s3://bucket/key.mp4", int64(1234), "mp4", false, "stream1",
				"pending", "", "", "t1", "vod", "", false))
		// Peer-relay fallback queries artifact_nodes for a local origin; none here.
		mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("h").WillReturnError(sql.ErrNoRows)
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod"}, resp, "node-1", log)
		if resp.GetMediaPresignedUrl() != "" {
			t.Fatal("pending sync must not serve a presigned media URL")
		}
		if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING {
			t.Fatalf("no local origin must stay source-missing, got %s", resp.GetState())
		}
	})
}

// TestFillPeerRelayFromLocalOrigin pins the local-origin peer-relay grant path:
// a fresh, complete origin row yields a peer URL + capability grant pointing at
// that node's Caddy origin; missing rows, blank base URLs, and missing extension
// all fail closed (return false).
func TestFillPeerRelayFromLocalOrigin(t *testing.T) {
	ctx := context.Background()
	log := logging.NewLogger()

	t.Run("nil DB returns false", func(t *testing.T) {
		prev := db
		db = nil
		t.Cleanup(func() { db = prev })
		resp := &ipcpb.RelayResolveResponse{}
		if fillPeerRelayFromLocalOrigin(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, sql.NullInt64{}, sql.NullString{}, sql.NullString{}, log) {
			t.Fatal("nil DB must return false")
		}
	})

	t.Run("no origin row returns false", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("h").WillReturnError(sql.ErrNoRows)
		resp := &ipcpb.RelayResolveResponse{}
		req := &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod", Ext: ".mp4"}
		if fillPeerRelayFromLocalOrigin(ctx, req, resp, sql.NullInt64{}, sql.NullString{}, sql.NullString{}, log) {
			t.Fatal("no origin row must return false")
		}
	})

	t.Run("origin with blank base url returns false", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("h").
			WillReturnRows(sqlmock.NewRows([]string{"node_id", "base_url"}).AddRow("node1", ""))
		resp := &ipcpb.RelayResolveResponse{}
		req := &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod", Ext: ".mp4"}
		if fillPeerRelayFromLocalOrigin(ctx, req, resp, sql.NullInt64{}, sql.NullString{}, sql.NullString{}, log) {
			t.Fatal("blank base url must return false")
		}
	})

	t.Run("fresh complete origin yields peer relay grant", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("h").
			WillReturnRows(sqlmock.NewRows([]string{"node_id", "base_url"}).
				AddRow("node1", "https://edge.example.com/view"))
		resp := &ipcpb.RelayResolveResponse{}
		req := &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod", Ext: ".mp4"}
		ok := fillPeerRelayFromLocalOrigin(ctx, req, resp, sql.NullInt64{Int64: 42, Valid: true}, sql.NullString{}, sql.NullString{}, log)
		if !ok {
			t.Fatal("fresh complete origin must return true")
		}
		if resp.GetState() != ipcpb.AssetState_ASSET_STATE_PLAYABLE {
			t.Fatalf("state = %s, want playable", resp.GetState())
		}
		// Caddy origin: scheme://host only, /view path dropped, relay path appended.
		want := "https://edge.example.com/internal/artifact/vod/h.mp4"
		if resp.GetPeerRelayUrl() != want {
			t.Fatalf("peer relay url = %q, want %q", resp.GetPeerRelayUrl(), want)
		}
		if resp.GetPeerRelayGrantId() == "" {
			t.Fatal("peer relay grant id must be minted")
		}
		if resp.GetExpectedSizeBytes() != 42 {
			t.Fatalf("expected size = %d, want 42", resp.GetExpectedSizeBytes())
		}
	})
}

// TestFillCrossClusterArtifactFromCommodore pins the processing-input federation
// gate: it resolves the source upload by hash and — critically — refuses to
// federate to an origin cluster that is not an authorized tenant peer. A nil
// client or unknown hash returns silently (→ 404 at the relay).
func TestFillCrossClusterArtifactFromCommodore(t *testing.T) {
	ctx := context.Background()
	log := logging.NewLogger()

	t.Run("nil commodore returns silently", func(t *testing.T) {
		prev := CommodoreClient
		CommodoreClient = nil
		t.Cleanup(func() { CommodoreClient = prev })
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillCrossClusterArtifactFromCommodore(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, "node-1", log)
		if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING {
			t.Fatalf("nil client must not change state, got %s", resp.GetState())
		}
	})

	t.Run("hash miss returns silently", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			vodHash: func(_ context.Context, _ *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
				return &commodorepb.ResolveVodHashResponse{Found: false}, nil
			},
		})
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillCrossClusterArtifactFromCommodore(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, "node-1", log)
		if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING {
			t.Fatalf("hash miss must not change state, got %s", resp.GetState())
		}
	})

	t.Run("unauthorized origin cluster is refused", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(func() { state.ResetDefaultManagerForTests() })
		// Platform/shared edge on a served cluster: passes tenant binding so the ORIGIN-cluster gate is exercised.
		sm.SetNodeInfo("node-1", "n", true, nil, nil, "", "", nil)
		sm.SetNodeConnectionInfo(context.Background(), "node-1", "n", "", "platform-eu", nil)
		AddPlatformSharedCluster("platform-eu")
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			vodHash: func(_ context.Context, _ *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
				// Found, with an origin cluster but NO cluster peers -> not authorized.
				return &commodorepb.ResolveVodHashResponse{Found: true, OriginClusterId: "foreign-cluster", TenantId: "t1"}, nil
			},
		})
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillCrossClusterArtifactFromCommodore(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, "node-1", log)
		if resp.GetState() == ipcpb.AssetState_ASSET_STATE_PLAYABLE {
			t.Fatal("unauthorized origin cluster must not be served")
		}
	})

	t.Run("node dedicated to another tenant is denied before federation", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(func() { state.ResetDefaultManagerForTests() })
		sm.SetNodeInfo("byoc-a-node", "n", true, nil, nil, "", "", nil)
		sm.SetNodeConnectionInfo(context.Background(), "byoc-a-node", "n", "tenant-a", "", nil)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			vodHash: func(_ context.Context, _ *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
				// The node-tenant binding is checked BEFORE the origin/peer gate, so this is denied regardless.
				return &commodorepb.ResolveVodHashResponse{Found: true, OriginClusterId: "c1", TenantId: "tenant-b"}, nil
			},
		})
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillCrossClusterArtifactFromCommodore(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, "byoc-a-node", log)
		if resp.GetState() == ipcpb.AssetState_ASSET_STATE_PLAYABLE {
			t.Fatal("a tenant-a node must NOT reach a tenant-b federated URL")
		}
	})
}
