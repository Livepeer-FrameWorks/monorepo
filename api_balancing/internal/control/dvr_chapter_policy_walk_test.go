package control

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// chapterOriginIDForArtifact is the foundation of the chapter→parent
// DVR policy walk. Tests pin: non-chapter artifacts pass through as
// empty (caller stops walking), chapter-origin artifacts return their
// origin_id so the caller can fetch the chapter and look up the parent
// DVR's policy.

func TestChapterOriginIDForArtifact_NonChapterReturnsEmpty(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectQuery(`SELECT origin_type, origin_id\s+FROM foghorn.artifacts\s+WHERE artifact_hash = \$1`).
		WithArgs("vod-1").
		WillReturnRows(sqlmock.NewRows([]string{"origin_type", "origin_id"}).AddRow("upload", "ignore"))

	got := chapterOriginIDForArtifact(context.Background(), "vod-1")
	if got != "" {
		t.Fatalf("expected empty for non-chapter origin, got %q", got)
	}
}

func TestChapterOriginIDForArtifact_ChapterReturnsOriginID(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectQuery(`SELECT origin_type, origin_id\s+FROM foghorn.artifacts\s+WHERE artifact_hash = \$1`).
		WithArgs("chap-art").
		WillReturnRows(sqlmock.NewRows([]string{"origin_type", "origin_id"}).AddRow("dvr_chapter", "chap-1"))

	got := chapterOriginIDForArtifact(context.Background(), "chap-art")
	if got != "chap-1" {
		t.Fatalf("expected origin_id=chap-1, got %q", got)
	}
}

func TestChapterOriginIDForArtifact_MissingArtifactReturnsEmpty(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectQuery(`SELECT origin_type, origin_id`).
		WithArgs("missing").
		WillReturnRows(sqlmock.NewRows([]string{"origin_type", "origin_id"}))

	got := chapterOriginIDForArtifact(context.Background(), "missing")
	if got != "" {
		t.Fatalf("expected empty for missing artifact, got %q", got)
	}
}
