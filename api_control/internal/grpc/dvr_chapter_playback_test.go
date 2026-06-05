package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
)

// MintChapterPlaybackID is idempotent on chapter_id — the SQL uses
// ON CONFLICT DO UPDATE … RETURNING playback_id, so retries return
// the existing public key even when the caller passes a freshly-
// generated one.
func TestMintChapterPlaybackID_IdempotentOnChapterID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`INSERT INTO commodore\.dvr_chapter_playback`).
		WithArgs("chap-1", "tenant-1", sqlmock.AnyArg(), "artifact-aaa").
		WillReturnRows(sqlmock.NewRows([]string{"playback_id"}).AddRow("pb_existing_chapter"))
	mock.ExpectExec(`INSERT INTO commodore\.vod_assets`).
		WithArgs(sqlmock.AnyArg(), "tenant-1", "user-1", "stream-1", "artifact-aaa", "artifact-aaa", "pb_existing_chapter",
			"DVR chapter", "", "chapter.mkv", "video/x-matroska", "cluster-1", "", "chap-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	resp, err := server.MintChapterPlaybackID(context.Background(), &commodorepb.MintChapterPlaybackIDRequest{
		ChapterId:       "chap-1",
		TenantId:        "tenant-1",
		ArtifactHash:    "artifact-aaa",
		UserId:          "user-1",
		Filename:        "chapter.mkv",
		OriginClusterId: "cluster-1",
		StreamId:        "stream-1",
	})
	if err != nil {
		t.Fatalf("MintChapterPlaybackID: %v", err)
	}
	if resp.GetPlaybackId() != "pb_existing_chapter" {
		t.Fatalf("expected existing playback id, got %q", resp.GetPlaybackId())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMintChapterPlaybackID_RejectsMissingArgs(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	server := &CommodoreServer{db: db, logger: logrus.New()}

	cases := []*commodorepb.MintChapterPlaybackIDRequest{
		{TenantId: "t", ArtifactHash: "a"},
		{ChapterId: "c", ArtifactHash: "a"},
		{ChapterId: "c", TenantId: "t"},
		{ChapterId: "c", TenantId: "t", ArtifactHash: "a"},
	}
	for _, req := range cases {
		if _, err := server.MintChapterPlaybackID(context.Background(), req); err == nil {
			t.Fatalf("expected error for %+v", req)
		}
	}
}

// Roundtrip — mint stores (chapter_id, tenant, artifact_hash, playback_id),
// resolve returns that same tuple back. The lookup is case-insensitive on
// playback_id since it's CITEXT.
func TestResolveChapterPlaybackID_Roundtrip(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT chapter_id, tenant_id::text, artifact_hash`).
		WithArgs("pb_existing_chapter").
		WillReturnRows(sqlmock.NewRows([]string{"chapter_id", "tenant_id", "artifact_hash"}).
			AddRow("chap-1", "tenant-1", "artifact-aaa"))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	resp, err := server.ResolveChapterPlaybackID(context.Background(), &commodorepb.ResolveChapterPlaybackIDRequest{
		PlaybackId: "pb_existing_chapter",
	})
	if err != nil {
		t.Fatalf("ResolveChapterPlaybackID: %v", err)
	}
	if !resp.GetFound() {
		t.Fatal("expected Found=true")
	}
	if resp.GetChapterId() != "chap-1" || resp.GetTenantId() != "tenant-1" || resp.GetArtifactHash() != "artifact-aaa" {
		t.Fatalf("unexpected: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestResolveChapterPlaybackID_NotFoundReturnsFoundFalse(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT chapter_id, tenant_id::text, artifact_hash`).
		WithArgs("pb_missing").
		WillReturnRows(sqlmock.NewRows([]string{"chapter_id", "tenant_id", "artifact_hash"}))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	resp, err := server.ResolveChapterPlaybackID(context.Background(), &commodorepb.ResolveChapterPlaybackIDRequest{
		PlaybackId: "pb_missing",
	})
	if err != nil {
		t.Fatalf("ResolveChapterPlaybackID unexpected error: %v", err)
	}
	if resp.GetFound() {
		t.Fatal("expected Found=false for unknown playback id")
	}
}
