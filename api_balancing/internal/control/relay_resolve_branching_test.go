package control

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// fileArtifactRows builds the 10-column row fillFileArtifactResolve scans.
func fileArtifactRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"s3_url", "size_bytes", "format", "dtsh_synced", "stream_internal_name",
		"sync_status", "origin_cluster_id", "storage_cluster_id", "tenant_id", "artifact_type",
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
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, log)
		if resp.GetError() == "" {
			t.Fatal("nil DB must set an error")
		}
	})

	t.Run("no row is silent not-found", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").WillReturnError(sql.ErrNoRows)
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod"}, resp, log)
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
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod"}, resp, log)
		if resp.GetError() == "" {
			t.Fatal("db error must set an error")
		}
	})

	t.Run("synced row serves presigned media", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").
			WillReturnRows(fileArtifactRows().AddRow(
				"s3://bucket/key.mp4", int64(1234), "mp4", false, "stream1",
				"synced", "", "", "t1", "vod"))
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod"}, resp, log)
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

	t.Run("unsynced row falls through to peer relay (and 404s without a local origin)", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		// Artifact row present but sync_status is pending: must NOT serve the s3_url.
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").
			WillReturnRows(fileArtifactRows().AddRow(
				"s3://bucket/key.mp4", int64(1234), "mp4", false, "stream1",
				"pending", "", "", "t1", "vod"))
		// Peer-relay fallback queries artifact_nodes for a local origin; none here.
		mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("h").WillReturnError(sql.ErrNoRows)
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod"}, resp, log)
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
		fillCrossClusterArtifactFromCommodore(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, log)
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
		fillCrossClusterArtifactFromCommodore(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, log)
		if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING {
			t.Fatalf("hash miss must not change state, got %s", resp.GetState())
		}
	})

	t.Run("unauthorized origin cluster is refused", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			vodHash: func(_ context.Context, _ *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
				// Found, with an origin cluster but NO cluster peers -> not authorized.
				return &commodorepb.ResolveVodHashResponse{Found: true, OriginClusterId: "foreign-cluster", TenantId: "t1"}, nil
			},
		})
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillCrossClusterArtifactFromCommodore(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h"}, resp, log)
		if resp.GetState() == ipcpb.AssetState_ASSET_STATE_PLAYABLE {
			t.Fatal("unauthorized origin cluster must not be served")
		}
	})
}
