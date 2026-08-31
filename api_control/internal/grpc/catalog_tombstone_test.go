package grpc

import (
	"context"
	"database/sql"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// A deleted clip is physically removed from the business table (the tombstone marker is the sole
// deletion record), so the ordinary hash resolver — which no longer filters any in-row flag — sees an
// absent row and returns Found=false.
func TestResolveClipHash_AbsentReadsNotFound(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectQuery(`FROM commodore.clips c[\s\S]*WHERE c.clip_hash = \$1`).
		WithArgs("clip-1").
		WillReturnError(sql.ErrNoRows)

	resp, err := s.ResolveClipHash(context.Background(), &commodorepb.ResolveClipHashRequest{ClipHash: "clip-1"})
	if err != nil {
		t.Fatalf("ResolveClipHash: %v", err)
	}
	if resp.GetFound() {
		t.Fatal("deleted (absent) clip must resolve as not found")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

// A deleted artifact's business row is gone across all three kinds, so the ordinary playback-id
// resolver returns Found=false without any in-row deletion filter.
func TestResolveArtifactPlaybackID_AbsentReadsNotFound(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectQuery(`FROM commodore.clips AS c[\s\S]*WHERE lower\(c.playback_id::text\) = lower\(\$1::text\)`).
		WithArgs("pb-1").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`FROM commodore.dvr_recordings d[\s\S]*WHERE lower\(d.playback_id::text\) = lower\(\$1::text\)`).
		WithArgs("pb-1").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`FROM commodore.vod_assets AS v[\s\S]*WHERE lower\(v.playback_id::text\) = lower\(\$1::text\)`).
		WithArgs("pb-1").WillReturnError(sql.ErrNoRows)

	resp, err := s.ResolveArtifactPlaybackID(context.Background(), &commodorepb.ResolveArtifactPlaybackIDRequest{PlaybackId: "pb-1"})
	if err != nil {
		t.Fatalf("ResolveArtifactPlaybackID: %v", err)
	}
	if resp.GetFound() {
		t.Fatal("deleted (absent) artifact must resolve as not found")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}
