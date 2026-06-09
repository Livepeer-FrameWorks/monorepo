package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
)

// artifactAssetTable is the SQL trust boundary for these handlers: the proto
// enum is the ONLY thing allowed to select a table/column name, so an
// unknown/unspecified type must be rejected rather than silently routed.
func TestArtifactAssetTable(t *testing.T) {
	cases := []struct {
		in      commodorepb.ArtifactAssetType
		table   string
		keyCol  string
		wantErr bool
	}{
		{commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP, "commodore.clips", "clip_hash", false},
		{commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, "commodore.dvr_recordings", "dvr_hash", false},
		{commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD, "commodore.vod_assets", "vod_hash", false},
		{commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_UNSPECIFIED, "", "", true},
	}
	for _, c := range cases {
		table, keyCol, err := artifactAssetTable(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("artifactAssetTable(%v): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("artifactAssetTable(%v): unexpected error %v", c.in, err)
		}
		if table != c.table || keyCol != c.keyCol {
			t.Errorf("artifactAssetTable(%v) = (%q,%q), want (%q,%q)", c.in, table, keyCol, c.table, c.keyCol)
		}
	}
}

func TestMarkArtifactThumbnailsReady(t *testing.T) {
	t.Run("missing_fields", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.MarkArtifactThumbnailsReady(context.Background(), &commodorepb.MarkArtifactThumbnailsReadyRequest{
			TenantId: "t1", AssetKey: "", StorageClusterId: "c1",
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP,
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("unsupported_asset_type", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.MarkArtifactThumbnailsReady(context.Background(), &commodorepb.MarkArtifactThumbnailsReadyRequest{
			TenantId: "t1", AssetKey: "h1", StorageClusterId: "c1",
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_UNSPECIFIED,
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	// Each asset type must route to its own table; verify the per-type table
	// name actually reaches the query.
	for _, tc := range []struct {
		name      string
		assetType commodorepb.ArtifactAssetType
		table     string
	}{
		{"clip", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP, "UPDATE commodore.clips"},
		{"dvr", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, "UPDATE commodore.dvr_recordings"},
		{"vod", commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD, "UPDATE commodore.vod_assets"},
	} {
		t.Run("routes_"+tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			mock.ExpectExec(tc.table).
				WithArgs("c1", "t1", "h1").
				WillReturnResult(sqlmock.NewResult(0, 1))
			resp, err := s.MarkArtifactThumbnailsReady(context.Background(), &commodorepb.MarkArtifactThumbnailsReadyRequest{
				TenantId: "t1", AssetKey: "h1", StorageClusterId: "c1", AssetType: tc.assetType,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !resp.GetUpdated() {
				t.Errorf("Updated = false, want true (RowsAffected=1)")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet: %v", err)
			}
		})
	}

	// Idempotent re-mark: the WHERE clause filters out no-op writes, so
	// RowsAffected=0 must surface as Updated=false (not an error).
	t.Run("idempotent_noop_reports_not_updated", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectExec("UPDATE commodore.clips").
			WithArgs("c1", "t1", "h1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		resp, err := s.MarkArtifactThumbnailsReady(context.Background(), &commodorepb.MarkArtifactThumbnailsReadyRequest{
			TenantId: "t1", AssetKey: "h1", StorageClusterId: "c1",
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetUpdated() {
			t.Errorf("Updated = true, want false (RowsAffected=0)")
		}
	})
}

func TestUpdateArtifactStorageCluster(t *testing.T) {
	t.Run("missing_fields", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.UpdateArtifactStorageCluster(context.Background(), &commodorepb.UpdateArtifactStorageClusterRequest{
			TenantId: "", AssetKey: "h1",
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("happy_updates_cluster_only", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectExec("UPDATE commodore.vod_assets").
			WithArgs(sqlmock.AnyArg(), "t1", "h1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		resp, err := s.UpdateArtifactStorageCluster(context.Background(), &commodorepb.UpdateArtifactStorageClusterRequest{
			TenantId: "t1", AssetKey: "h1", StorageClusterId: "c2",
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetUpdated() {
			t.Errorf("Updated = false, want true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}

func TestUpdateArtifactSize(t *testing.T) {
	t.Run("negative_size_rejected", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.UpdateArtifactSize(context.Background(), &commodorepb.UpdateArtifactSizeRequest{
			TenantId: "t1", AssetKey: "h1", SizeBytes: -1,
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR,
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("happy_writes_size", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectExec("UPDATE commodore.dvr_recordings").
			WithArgs(int64(2048), "t1", "h1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		resp, err := s.UpdateArtifactSize(context.Background(), &commodorepb.UpdateArtifactSizeRequest{
			TenantId: "t1", AssetKey: "h1", SizeBytes: 2048,
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetUpdated() {
			t.Errorf("Updated = false, want true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}
