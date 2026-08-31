package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
)

// A mint that lands after a concurrent delete wrote the chapter's tombstone marker writes nothing: the
// marker check inside the transaction (under the per-artifact advisory lock the delete projection also
// takes) sees the marker and the tx is rolled back with FailedPrecondition — no mapping or vod_asset
// upsert is issued, so the deleted asset is never resurrected. The advisory lock closes the absent-row
// TOCTOU a FOR UPDATE on the maybe-absent business row could not.
func TestMintChapterPlaybackID_TombstonedChapterRefused(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("t1:vod:chap-hash").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT deletion_revision FROM commodore\.artifact_catalog_tombstones .* FOR UPDATE`).
		WithArgs("t1", "chap-hash").
		WillReturnRows(sqlmock.NewRows([]string{"deletion_revision"}).AddRow(int64(9)))
	// No INSERTs must follow: a tombstoned chapter cannot be resurrected.
	mock.ExpectRollback()

	_, err := s.MintChapterPlaybackID(context.Background(), &commodorepb.MintChapterPlaybackIDRequest{
		ChapterId: "c1", TenantId: "t1", ArtifactHash: "chap-hash", UserId: "u1", DvrHash: "dvr-1",
	})
	wantCode(t, err, codes.FailedPrecondition)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

// A fresh chapter (no tombstone marker) proceeds inside the transaction: the marker check misses, then
// the playback-id mint and vod_asset registration run and the tx commits.
func TestMintChapterPlaybackID_FreshChapterRegisters(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("t1:vod:chap-hash").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT deletion_revision FROM commodore\.artifact_catalog_tombstones .* FOR UPDATE`).
		WithArgs("t1", "chap-hash").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO commodore.dvr_chapter_playback`).
		WillReturnRows(sqlmock.NewRows([]string{"playback_id"}).AddRow("pb-chap"))
	mock.ExpectExec(`INSERT INTO commodore.vod_assets`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := s.MintChapterPlaybackID(context.Background(), &commodorepb.MintChapterPlaybackIDRequest{
		ChapterId: "c1", TenantId: "t1", ArtifactHash: "chap-hash", UserId: "u1", DvrHash: "dvr-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetPlaybackId() != "pb-chap" {
		t.Errorf("got playback_id=%q, want pb-chap", resp.GetPlaybackId())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestMintChapterPlaybackID_CrossTenantChapterCollisionRefused(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("tenant-b:vod:chap-hash-b").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT deletion_revision FROM commodore\.artifact_catalog_tombstones .* FOR UPDATE`).
		WithArgs("tenant-b", "chap-hash-b").
		WillReturnError(sql.ErrNoRows)
	// The tenant-qualified ON CONFLICT branch returns no row when chapter_id is
	// already owned by another tenant. The transaction must stop before the VOD
	// projection and roll back both the attempted mapping change and lock scope.
	mock.ExpectQuery(`INSERT INTO commodore.dvr_chapter_playback`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := s.MintChapterPlaybackID(context.Background(), &commodorepb.MintChapterPlaybackIDRequest{
		ChapterId: "shared-chapter-id", TenantId: "tenant-b", ArtifactHash: "chap-hash-b", UserId: "user-b", DvrHash: "dvr-b",
	})
	wantCode(t, err, codes.FailedPrecondition)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}
