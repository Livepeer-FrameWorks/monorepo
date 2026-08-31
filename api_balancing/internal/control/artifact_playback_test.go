package control

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// ResolveArtifactPlayback resolves a clip/dvr/vod playback id to a viewer
// endpoint. This wires all three doubles (Commodore fake + sqlmock + deps) to pin
// its guards and — critically — the cross-cluster FRONT-DOOR REAUTHORIZATION: an
// adopted pointer row whose authoritative byte-cluster is no longer an authorized
// tenant peer must stop serving, even though the row still exists locally.
func TestResolveArtifactPlayback(t *testing.T) {
	ctx := context.Background()

	t.Run("nil DB errors", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		if _, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{}, "pb"); err == nil {
			t.Fatal("nil DB must error")
		}
	})

	t.Run("empty playback id errors", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		mockDB, _, _ := sqlmock.New()
		t.Cleanup(func() { _ = mockDB.Close() })
		if _, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB}, ""); err == nil {
			t.Fatal("empty playback id must error")
		}
	})

	t.Run("nil commodore client errors", func(t *testing.T) {
		prev := CommodoreClient
		CommodoreClient = nil
		t.Cleanup(func() { CommodoreClient = prev })
		mockDB, _, _ := sqlmock.New()
		t.Cleanup(func() { _ = mockDB.Close() })
		if _, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB}, "pb"); err == nil {
			t.Fatal("nil commodore client must error")
		}
	})

	t.Run("artifact not found in commodore errors", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
			},
		})
		mockDB, _, _ := sqlmock.New()
		t.Cleanup(func() { _ = mockDB.Close() })
		if _, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1"}, "pb"); err == nil {
			t.Fatal("unresolved artifact must error")
		}
	})

	t.Run("no artifact row and no federation -> not found", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: foundArtifact("h1", "vod", "t1", ""),
		})
		mockDB, mock, _ := sqlmock.New()
		t.Cleanup(func() { _ = mockDB.Close() })
		mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = \$2`).
			WithArgs("h1", "vod", "t1").
			WillReturnError(sql.ErrNoRows)
		if _, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1"}, "pb"); err == nil {
			t.Fatal("missing local row with no federation must be not-found")
		}
	})

	t.Run("unauthorized authoritative cluster is refused (front-door reauth)", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			artifactPlaybackID: foundArtifact("h1", "vod", "t1", ""), // no cluster peers
		})
		mockDB, mock, _ := sqlmock.New()
		t.Cleanup(func() { _ = mockDB.Close() })
		// Row exists but its authoritative byte-cluster is a foreign cluster the
		// tenant no longer peers with -> must refuse to serve.
		mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1`).
			WithArgs("h1", "vod", "t1").
			WillReturnRows(sqlmock.NewRows([]string{
				"internal_name", "status", "duration_seconds", "size_bytes", "created_at",
				"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster", "thumbnail_serving_cluster",
			}).AddRow("s1", "ready", int64(60), int64(9000), nil, "mp4", "s3", "synced", false, "revoked-peer", ""))

		_, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1"}, "pb")
		if err == nil {
			t.Fatal("artifact whose authoritative cluster is unauthorized must be refused")
		}
	})
}

// Warm-node happy path: an artifact present on an active local node resolves to
// a viewer endpoint pointing at that node, exercising the full build (warm-node
// selection → ranking → output assembly). The authoritative cluster is an
// explicitly platform-shared local cluster, so the front-door gate is exercised
// without requiring a tenant-specific peer grant.
func TestResolveArtifactPlayback_WarmNodeHappyPath(t *testing.T) {
	ctx := context.Background()
	previousLocalCluster := GetLocalClusterID()
	t.Cleanup(func() { SetLocalClusterID(previousLocalCluster) })
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	lat, lon := 52.0, 5.0
	sm.SetNodeInfo("n1", "https://n1.example.com", true, &lat, &lon, "ams", "", map[string]any{"HLS": "x"})
	sm.TouchNode("n1", true)
	sm.SetNodeArtifacts("n1", []*ipcpb.StoredArtifact{{ClipHash: "h1"}}, state.ArtifactReportOrder{Fence: 1, Seq: 1})
	SetLocalClusterID("c1")
	AddPlatformSharedCluster("c1")

	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		artifactPlaybackID: foundArtifact("h1", "vod", "t1", "c1"),
	})
	mockDB, mock, _ := sqlmock.New()
	t.Cleanup(func() { _ = mockDB.Close() })
	// Local row on an authorized platform-shared cluster, synced to S3.
	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1`).
		WithArgs("h1", "vod", "t1").
		WillReturnRows(sqlmock.NewRows([]string{
			"internal_name", "status", "duration_seconds", "size_bytes", "created_at",
			"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster", "thumbnail_serving_cluster",
		}).AddRow("s1", "ready", int64(60), int64(9000), nil, "mp4", "s3", "synced", false, "c1", ""))

	resp, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1", GeoLat: 52, GeoLon: 5}, "pb")
	if err != nil {
		t.Fatalf("warm-node resolution failed: %v", err)
	}
	if resp.GetPrimary() == nil || resp.GetPrimary().GetNodeId() != "n1" {
		t.Fatalf("expected primary endpoint on n1, got %+v", resp.GetPrimary())
	}
	if resp.GetPrimary().GetUrl() == "" {
		t.Fatal("primary endpoint url must be populated")
	}
}

func TestResolveArtifactPlaybackWithIdentity_DoesNotCallControlPlane(t *testing.T) {
	previousLocalCluster := GetLocalClusterID()
	t.Cleanup(func() { SetLocalClusterID(previousLocalCluster) })
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	lat, lon := 52.0, 5.0
	sm.SetNodeInfo("n1", "https://n1.example.com", true, &lat, &lon, "ams", "", map[string]any{"HLS": "x"})
	sm.TouchNode("n1", true)
	sm.SetNodeArtifacts("n1", []*ipcpb.StoredArtifact{{ClipHash: "h-local"}}, state.ArtifactReportOrder{Fence: 1, Seq: 1})
	SetLocalClusterID("c1")
	AddPlatformSharedCluster("c1")

	startFakeCommodoreServer(t, &fakeCommodoreInternal{})
	mockDB, mock, _ := sqlmock.New()
	t.Cleanup(func() { _ = mockDB.Close() })
	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1`).
		WithArgs("h-local", "vod", "t1").
		WillReturnRows(sqlmock.NewRows([]string{
			"internal_name", "status", "duration_seconds", "size_bytes", "created_at",
			"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster", "thumbnail_serving_cluster",
		}).AddRow("asset-local", "ready", int64(60), int64(9000), nil, "mp4", "s3", "synced", false, "c1", ""))

	var centralCalls int
	ctx := ctxkeys.WithMediaRequestRPCObserver(context.Background(), "viewer_test", func(_, _, _ string) {
		centralCalls++
	})
	identity := &commodorepb.ResolveArtifactPlaybackIDResponse{
		Found: true, ArtifactHash: "h-local", InternalName: "asset-local", TenantId: "t1",
		ContentType: "vod", OriginClusterId: "c1",
	}
	resp, err := ResolveArtifactPlaybackWithIdentity(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1", AllowPlatformSharedPlayback: true}, "pb-local", identity)
	if err != nil {
		t.Fatalf("local-authority artifact resolution failed: %v", err)
	}
	if resp.GetPrimary().GetNodeId() != "n1" {
		t.Fatalf("primary node = %q, want n1", resp.GetPrimary().GetNodeId())
	}
	if centralCalls != 0 {
		t.Fatalf("local-authority artifact resolution made %d central RPCs", centralCalls)
	}
	if _, err := CommodoreClient.ResolveArtifactPlaybackID(ctx, "probe"); err != nil {
		t.Fatalf("observer probe: %v", err)
	}
	if centralCalls != 1 {
		t.Fatalf("central RPC observer count = %d, want 1 after probe", centralCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestResolveArtifactPlayback_CordonBlocksDBFallback is the cordon regression: a node holds the artifact
// but its ArtifactInventoryReady cordon is DOWN (incomplete scan), so the in-memory FindNodesByArtifactHash
// excludes it. There is deliberately NO fallback to foghorn.artifact_nodes — the durable projection has no
// readiness gate — so a NON-cold artifact (not synced, not S3, origin==local => no federation) must resolve
// to a "storage node unknown" error, NOT route through a retained DB row. The test mocks ONLY the metadata
// query; if the removed artifact_nodes fallback ran, sqlmock would flag its unexpected query.
func TestResolveArtifactPlayback_CordonBlocksDBFallback(t *testing.T) {
	ctx := context.Background()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	lat, lon := 52.0, 5.0
	sm.SetNodeInfo("n1", "https://n1.example.com", true, &lat, &lon, "ams", "", map[string]any{"HLS": "x"})
	sm.TouchNode("n1", true)
	// Node reported the artifact (ready), then a later incomplete scan cordons it — inventory retained,
	// ArtifactInventoryReady=false. FindNodesByArtifactHash must now exclude it.
	sm.SetNodeArtifacts("n1", []*ipcpb.StoredArtifact{{ClipHash: "h1"}}, state.ArtifactReportOrder{Fence: 1, Seq: 1})
	if err := sm.CordonNodeArtifactsIncomplete("n1", state.ArtifactReportOrder{Fence: 1, Seq: 2}); err != nil {
		t.Fatalf("cordon failed: %v", err)
	}
	if nodes := sm.FindNodesByArtifactHash("h1"); len(nodes) != 0 {
		t.Fatalf("cordoned node must be excluded from in-memory routing, got %d", len(nodes))
	}

	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		artifactPlaybackID: foundArtifact("h1", "vod", "t1", ""), // origin empty => local, no federation
	})
	mockDB, mock, _ := sqlmock.New()
	t.Cleanup(func() { _ = mockDB.Close() })
	// Artifact is NOT synced and NOT on S3 (cold path cannot serve), authoritative cluster empty
	// (serveable). Only this metadata query is expected — no artifact_nodes fallback.
	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1`).
		WithArgs("h1", "vod", "t1").
		WillReturnRows(sqlmock.NewRows([]string{
			"internal_name", "status", "duration_seconds", "size_bytes", "created_at",
			"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster", "thumbnail_serving_cluster",
		}).AddRow("s1", "ready", int64(60), int64(9000), nil, "mp4", "local", "pending", false, "", ""))

	_, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1", GeoLat: 52, GeoLon: 5}, "pb")
	if err == nil {
		t.Fatal("cordoned inventory + non-cold artifact must NOT route through the DB projection; expected an error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("only the metadata query may run (no artifact_nodes fallback): %v", err)
	}
}

func foundArtifact(hash, contentType, tenantID, originCluster string) func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
	return func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
		return &commodorepb.ResolveArtifactPlaybackIDResponse{
			Found: true, ArtifactHash: hash, ContentType: contentType, TenantId: tenantID, OriginClusterId: originCluster,
		}, nil
	}
}
