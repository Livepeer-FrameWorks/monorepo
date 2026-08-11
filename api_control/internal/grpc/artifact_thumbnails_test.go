package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
)

// artifactAssetTable is the SQL trust boundary for these handlers: the proto
// enum is the ONLY thing allowed to select a table/column name and tombstone
// kind, so an unknown/unspecified type must be rejected rather than routed.
func TestArtifactAssetTable(t *testing.T) {
	cases := []struct {
		in      commodorepb.ArtifactAssetType
		table   string
		keyCol  string
		kind    string
		wantErr bool
	}{
		{commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP, "commodore.clips", "clip_hash", "clip", false},
		{commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR, "commodore.dvr_recordings", "dvr_hash", "dvr", false},
		{commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD, "commodore.vod_assets", "vod_hash", "vod", false},
		{commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_UNSPECIFIED, "", "", "", true},
	}
	for _, c := range cases {
		table, keyCol, kind, err := artifactAssetTable(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("artifactAssetTable(%v): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("artifactAssetTable(%v): unexpected error %v", c.in, err)
		}
		if table != c.table || keyCol != c.keyCol || kind != c.kind {
			t.Errorf("artifactAssetTable(%v) = (%q,%q,%q), want (%q,%q,%q)", c.in, table, keyCol, kind, c.table, c.keyCol, c.kind)
		}
	}
}

func TestUpdateArtifactCatalogSnapshot(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	strp := func(v string) *string { return &v }

	// Deletion projection: deleted=true writes the durable tombstone MARKER (monotonic upsert) and
	// REMOVES the business row AND, for a VOD, its chapter playback mapping — all in ONE transaction
	// under the per-artifact advisory lock. The reconciler advances only on the marker's revision.
	t.Run("deleted_writes_marker_and_removes_row", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("t1:vod:vod-1").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision FROM commodore.vod_assets[\s\S]*FOR UPDATE`).
			WithArgs("t1", "vod-1").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`INSERT INTO commodore.artifact_catalog_tombstones[\s\S]*RETURNING deletion_revision`).
			WithArgs("t1", "vod", "vod-1", "media-us-1", int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"deletion_revision"}).AddRow(int64(9)))
		mock.ExpectExec(`DELETE FROM commodore.vod_assets WHERE tenant_id = \$1::uuid AND vod_hash = \$2`).
			WithArgs("t1", "vod-1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`DELETE FROM commodore.dvr_chapter_playback WHERE tenant_id = \$1::uuid AND artifact_hash = \$2`).
			WithArgs("t1", "vod-1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		resp, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 9,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("media-us-1"),
			Deleted:         true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetCurrentRevision() != 9 {
			t.Errorf("got found=%v rev=%d, want found=true rev=9", resp.GetFound(), resp.GetCurrentRevision())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A clip has no chapter mapping, so the delete is marker upsert + business-row removal. The upsert
	// claims origin authority (COALESCE) via the source cluster arg.
	t.Run("deleted_clip_no_chapter_mapping", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("t1:clip:clip-1").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision FROM commodore.clips[\s\S]*FOR UPDATE`).
			WithArgs("t1", "clip-1").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`INSERT INTO commodore.artifact_catalog_tombstones[\s\S]*RETURNING deletion_revision`).
			WithArgs("t1", "clip", "clip-1", "media-us-1", int64(9)).
			WillReturnRows(sqlmock.NewRows([]string{"deletion_revision"}).AddRow(int64(9)))
		mock.ExpectExec(`DELETE FROM commodore.clips WHERE tenant_id = \$1::uuid AND clip_hash = \$2`).
			WithArgs("t1", "clip-1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		resp, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "clip-1", SourceRevision: 9,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP,
			SourceClusterId: strp("media-us-1"),
			Deleted:         true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetCurrentRevision() != 9 {
			t.Errorf("got found=%v rev=%d, want found=true rev=9", resp.GetFound(), resp.GetCurrentRevision())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A deletion is representable even when no business row exists (delete-before-registration): the
	// marker is still upserted, the business-row DELETE affects zero rows, and the deletion is covered.
	t.Run("deleted_absent_row_still_writes_marker", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("t1:vod:vod-1").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision FROM commodore.vod_assets[\s\S]*FOR UPDATE`).
			WithArgs("t1", "vod-1").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`INSERT INTO commodore.artifact_catalog_tombstones[\s\S]*RETURNING deletion_revision`).
			WillReturnRows(sqlmock.NewRows([]string{"deletion_revision"}).AddRow(int64(9)))
		mock.ExpectExec(`DELETE FROM commodore.vod_assets`).WithArgs("t1", "vod-1").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`DELETE FROM commodore.dvr_chapter_playback`).WithArgs("t1", "vod-1").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
		resp, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 9,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("media-us-1"),
			Deleted:         true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetCurrentRevision() != 9 {
			t.Errorf("absent-row delete must still write a durable marker (found=true rev=9); got found=%v rev=%d", resp.GetFound(), resp.GetCurrentRevision())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// Atomicity: a failed business-row removal after the marker upsert rolls back the whole tx and
	// returns Internal — the marker and the row must never diverge.
	t.Run("deleted_rolls_back_when_row_delete_fails", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision FROM commodore.vod_assets[\s\S]*FOR UPDATE`).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`INSERT INTO commodore.artifact_catalog_tombstones[\s\S]*RETURNING deletion_revision`).
			WillReturnRows(sqlmock.NewRows([]string{"deletion_revision"}).AddRow(int64(9)))
		mock.ExpectExec(`DELETE FROM commodore.vod_assets`).WillReturnError(sql.ErrConnDone)
		mock.ExpectRollback()
		_, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 9,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("media-us-1"),
			Deleted:         true,
		})
		wantCode(t, err, codes.Internal)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A marker owned by a DIFFERENT origin cluster rejects the upsert (WHERE origin guard); the
	// readback surfaces PermissionDenied rather than a false coverage.
	t.Run("deleted_enforces_origin_authority", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision FROM commodore.vod_assets[\s\S]*FOR UPDATE`).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`INSERT INTO commodore.artifact_catalog_tombstones[\s\S]*RETURNING deletion_revision`).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`SELECT origin_cluster_id FROM commodore.artifact_catalog_tombstones`).
			WithArgs("t1", "vod", "vod-1").
			WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id"}).AddRow("media-us-1"))
		mock.ExpectRollback()
		_, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 9,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("rogue-cluster"),
			Deleted:         true,
		})
		wantCode(t, err, codes.PermissionDenied)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A delayed delete must NOT remove a row a re-creation has already advanced past. The live row is at
	// revision 11; this delete carries revision 9 (<= 11), so it is stale: the row is left intact and NO
	// tombstone is written. Coverage is reported at the live revision so the reconciler advances.
	t.Run("stale_delete_after_recreation_keeps_live_row", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("t1:vod:vod-1").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision FROM commodore.vod_assets[\s\S]*FOR UPDATE`).
			WithArgs("t1", "vod-1").
			WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "catalog_revision"}).AddRow("media-us-1", int64(11)))
		mock.ExpectCommit()
		resp, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 9,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("media-us-1"),
			Deleted:         true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetCurrentRevision() != 11 {
			t.Errorf("stale delete must leave the live row (found=true rev=11); got found=%v rev=%d", resp.GetFound(), resp.GetCurrentRevision())
		}
		// No tombstone INSERT and no business-row DELETE were expected — a stale delete must not touch either.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A foreign cluster must not delete a live row it does not own, even before any tombstone exists. The
	// live row's origin is media-us-1; a delete from rogue-cluster is rejected with PermissionDenied
	// before any tombstone/delete.
	t.Run("foreign_delete_against_live_row_denied", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("t1:vod:vod-1").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision FROM commodore.vod_assets[\s\S]*FOR UPDATE`).
			WithArgs("t1", "vod-1").
			WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "catalog_revision"}).AddRow("media-us-1", int64(5)))
		mock.ExpectRollback()
		_, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 9,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("rogue-cluster"),
			Deleted:         true,
		})
		wantCode(t, err, codes.PermissionDenied)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	t.Run("missing_fields", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "", SourceRevision: 5,
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("missing_source_revision_rejected", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "h1",
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	// A snapshot with no source_cluster_id is rejected — the projecting cluster must identify
	// itself so Commodore can assign/enforce origin authority.
	t.Run("missing_source_cluster_rejected", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 7,
			AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	// Applied revive path: no marker, the guarded UPDATE matches, RETURNING yields the new revision.
	t.Run("applied_returns_new_revision", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("t1:vod:vod-1").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT deletion_revision, origin_cluster_id FROM commodore.artifact_catalog_tombstones[\s\S]*FOR UPDATE`).
			WithArgs("t1", "vod", "vod-1").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`UPDATE commodore.vod_assets`).
			WillReturnRows(sqlmock.NewRows([]string{"catalog_revision", "thumbnail_serving_cluster_id"}).AddRow(int64(7), "media-official"))
		mock.ExpectCommit()
		resp, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 7,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SizeBytes:       i64(2048),
			SourceClusterId: strp("media-us-1"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetCurrentRevision() != 7 {
			t.Errorf("got found=%v rev=%d, want found=true rev=7", resp.GetFound(), resp.GetCurrentRevision())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// Guard-rejected revive: no marker, the UPDATE matches no row (stored revision already >= source),
	// so the handler reads and reports the stored revision.
	t.Run("guard_rejected_reports_stored_revision", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT deletion_revision, origin_cluster_id FROM commodore.artifact_catalog_tombstones[\s\S]*FOR UPDATE`).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`UPDATE commodore.vod_assets`).WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision, thumbnail_serving_cluster_id FROM commodore.vod_assets`).
			WithArgs("t1", "vod-1").
			WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "catalog_revision", "thumbnail_serving_cluster_id"}).AddRow("media-us-1", int64(9), nil))
		mock.ExpectCommit()
		resp, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 7,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("media-us-1"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetCurrentRevision() != 9 {
			t.Errorf("got found=%v rev=%d, want found=true rev=9", resp.GetFound(), resp.GetCurrentRevision())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// Source authority denied on revive: the UPDATE matches no row and the read-back shows a DIFFERENT
	// origin cluster → explicit PermissionDenied.
	t.Run("enforces_source_cluster_authority", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT deletion_revision, origin_cluster_id FROM commodore.artifact_catalog_tombstones[\s\S]*FOR UPDATE`).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`UPDATE commodore.vod_assets[\s\S]*origin_cluster_id IS NULL OR origin_cluster_id = \$15`).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision, thumbnail_serving_cluster_id FROM commodore.vod_assets`).
			WithArgs("t1", "vod-1").
			WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "catalog_revision", "thumbnail_serving_cluster_id"}).AddRow("media-us-1", int64(4), nil))
		mock.ExpectRollback()
		_, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 7,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("rogue-cluster"),
		})
		wantCode(t, err, codes.PermissionDenied)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// Not-registered revive: no marker, UPDATE and the readback both miss → found=false.
	t.Run("not_found_when_row_absent", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT deletion_revision, origin_cluster_id FROM commodore.artifact_catalog_tombstones[\s\S]*FOR UPDATE`).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`UPDATE commodore.vod_assets`).WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`SELECT origin_cluster_id, catalog_revision, thumbnail_serving_cluster_id FROM commodore.vod_assets`).
			WithArgs("t1", "vod-1").WillReturnError(sql.ErrNoRows)
		mock.ExpectCommit()
		resp, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 7,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("media-us-1"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetFound() {
			t.Errorf("got found=true, want false (row not registered)")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A stale-lower snapshot cannot clear a marker: the marker at deletion_revision 9 >= source 5, so
	// the revive short-circuits (no UPDATE) and reports the marker revision as covered.
	t.Run("stale_snapshot_cannot_clear_marker", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT deletion_revision, origin_cluster_id FROM commodore.artifact_catalog_tombstones[\s\S]*FOR UPDATE`).
			WithArgs("t1", "vod", "vod-1").
			WillReturnRows(sqlmock.NewRows([]string{"deletion_revision", "origin_cluster_id"}).AddRow(int64(9), "media-us-1"))
		mock.ExpectCommit()
		resp, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 5,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("media-us-1"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetCurrentRevision() != 9 {
			t.Errorf("got found=%v rev=%d, want found=true rev=9 (marker covers the stale write)", resp.GetFound(), resp.GetCurrentRevision())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A strictly-newer authoritative snapshot supersedes the marker: the marker (rev 5) is cleared,
	// then the guarded revive UPDATE applies at the newer revision.
	t.Run("strictly_newer_snapshot_clears_marker_then_revives", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT deletion_revision, origin_cluster_id FROM commodore.artifact_catalog_tombstones[\s\S]*FOR UPDATE`).
			WithArgs("t1", "vod", "vod-1").
			WillReturnRows(sqlmock.NewRows([]string{"deletion_revision", "origin_cluster_id"}).AddRow(int64(5), "media-us-1"))
		mock.ExpectExec(`DELETE FROM commodore.artifact_catalog_tombstones`).
			WithArgs("t1", "vod", "vod-1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`UPDATE commodore.vod_assets`).
			WillReturnRows(sqlmock.NewRows([]string{"catalog_revision", "thumbnail_serving_cluster_id"}).AddRow(int64(9), nil))
		mock.ExpectCommit()
		resp, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 9,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("media-us-1"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetFound() || resp.GetCurrentRevision() != 9 {
			t.Errorf("got found=%v rev=%d, want found=true rev=9", resp.GetFound(), resp.GetCurrentRevision())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// A snapshot from a FOREIGN origin cluster must NOT clear another origin's tombstone, even with a
	// numerically larger revision — revisions are cluster-local and incomparable. The revive path reads
	// the marker's origin under FOR UPDATE and fails closed with PermissionDenied BEFORE any revision
	// comparison or DELETE, so the business row (absent here) can never be resurrected. Regression: the
	// origin check ran only at the business-row UPDATE, so an absent row committed the marker clear.
	t.Run("foreign_origin_cannot_clear_tombstone_even_with_larger_revision", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("t1:vod:vod-1").WillReturnResult(sqlmock.NewResult(0, 0))
		// Marker owned by media-us-1 at revision 5; the foreign snapshot arrives from media-eu-1 at a
		// larger (incomparable) revision 100. No DELETE, no UPDATE — the origin mismatch rejects first.
		mock.ExpectQuery(`SELECT deletion_revision, origin_cluster_id FROM commodore.artifact_catalog_tombstones[\s\S]*FOR UPDATE`).
			WithArgs("t1", "vod", "vod-1").
			WillReturnRows(sqlmock.NewRows([]string{"deletion_revision", "origin_cluster_id"}).AddRow(int64(5), "media-us-1"))
		mock.ExpectRollback()
		_, err := s.UpdateArtifactCatalogSnapshot(context.Background(), &commodorepb.UpdateArtifactCatalogSnapshotRequest{
			TenantId: "t1", AssetKey: "vod-1", SourceRevision: 100,
			AssetType:       commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD,
			SourceClusterId: strp("media-eu-1"),
		})
		wantCode(t, err, codes.PermissionDenied)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}
