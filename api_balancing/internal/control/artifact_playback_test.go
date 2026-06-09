package control

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
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
				"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster",
			}).AddRow("s1", "ready", int64(60), int64(9000), nil, "mp4", "s3", "synced", false, "revoked-peer"))

		_, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1"}, "pb")
		if err == nil {
			t.Fatal("artifact whose authoritative cluster is unauthorized must be refused")
		}
	})
}

// Warm-node happy path: an artifact present on an active local node resolves to
// a viewer endpoint pointing at that node, exercising the full build (warm-node
// selection → ranking → output assembly). Authoritative cluster is empty (always
// serveable), so no cross-cluster gate and no load balancer are involved.
func TestResolveArtifactPlayback_WarmNodeHappyPath(t *testing.T) {
	ctx := context.Background()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	lat, lon := 52.0, 5.0
	sm.SetNodeInfo("n1", "https://n1.example.com", true, &lat, &lon, "ams", "", map[string]any{"HLS": "x"})
	sm.TouchNode("n1", true)
	sm.SetNodeArtifacts("n1", []*ipcpb.StoredArtifact{{ClipHash: "h1"}})

	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		artifactPlaybackID: foundArtifact("h1", "vod", "t1", ""),
	})
	mockDB, mock, _ := sqlmock.New()
	t.Cleanup(func() { _ = mockDB.Close() })
	// Local row, empty authoritative cluster (always serveable), synced to S3.
	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1`).
		WithArgs("h1", "vod", "t1").
		WillReturnRows(sqlmock.NewRows([]string{
			"internal_name", "status", "duration_seconds", "size_bytes", "created_at",
			"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster",
		}).AddRow("s1", "ready", int64(60), int64(9000), nil, "mp4", "s3", "synced", false, ""))

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

func foundArtifact(hash, contentType, tenantID, originCluster string) func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
	return func(_ context.Context, _ *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
		return &commodorepb.ResolveArtifactPlaybackIDResponse{
			Found: true, ArtifactHash: hash, ContentType: contentType, TenantId: tenantID, OriginClusterId: originCluster,
		}, nil
	}
}
