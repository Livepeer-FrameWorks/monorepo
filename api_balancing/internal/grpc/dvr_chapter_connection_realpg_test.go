//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSavedDVRChapterConnection_RealPG(t *testing.T) {
	testSavedDVRChapterConnection(t, startFoghornGRPCRealPG(t))
}
func TestSavedDVRChapterConnection_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "foghorn_saved_chapter_api", "foghorn")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testSavedDVRChapterConnection(t, db)
}
func testSavedDVRChapterConnection(t *testing.T, db *sql.DB) {
	t.Helper()
	tenant := uuid.NewString()
	const hash = "saved-chapter-parent"
	if _, err := db.Exec(`INSERT INTO foghorn.artifacts(artifact_hash,artifact_type,tenant_id,status) VALUES($1,'dvr',$2,'ready')`, hash, tenant); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"chapter-a", "chapter-b", "chapter-c"} {
		mode, start := "window_sized_chapters", 1000
		if i == 1 {
			mode = "fixed_interval"
		}
		if i == 2 {
			start = 2000
		}
		if _, err := db.Exec(`INSERT INTO foghorn.dvr_chapters(chapter_id,artifact_hash,mode,start_ms,end_ms,state,playback_id) VALUES($1,$2,$3,$4,$5,'finalized',$6)`, id, hash, mode, start, start+1000, "play-"+id); err != nil {
			t.Fatal(err)
		}
	}
	server := &FoghornGRPCServer{db: db, logger: logging.NewLogger()}
	request := &pb.ListDVRChaptersRequest{DvrArtifactId: hash, TenantId: tenant, Pagination: &commonpb.CursorPaginationRequest{First: 2}}
	first, err := server.ListDVRChapters(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Chapters) != 2 || first.Chapters[0].ChapterId != "chapter-a" || first.Chapters[1].ChapterId != "chapter-b" || first.Pagination.TotalCount != 3 || !first.Pagination.HasNextPage || first.Pagination.HasPreviousPage {
		t.Fatalf("first saved chapter page: %v", first)
	}
	request.Pagination.After = first.Pagination.EndCursor
	second, err := server.ListDVRChapters(context.Background(), request)
	if err != nil || len(second.Chapters) != 1 || second.Chapters[0].ChapterId != "chapter-c" || second.Chapters[0].PlaybackId != "play-chapter-c" || second.Pagination.HasNextPage {
		t.Fatalf("second saved chapter page: %v %v", second, err)
	}
	request.Pagination = &commonpb.CursorPaginationRequest{Last: 2, Before: second.Pagination.StartCursor}
	previous, err := server.ListDVRChapters(context.Background(), request)
	if err != nil || len(previous.Chapters) != 2 || previous.Chapters[0].ChapterId != "chapter-a" || previous.Chapters[1].ChapterId != "chapter-b" {
		t.Fatalf("backward saved chapter page: %v %v", previous, err)
	}
	request.Pagination = &commonpb.CursorPaginationRequest{First: 2}
	request.RangeStartMs, request.RangeEndMs = 2000, 3000
	ranged, err := server.ListDVRChapters(context.Background(), request)
	if err != nil || len(ranged.Chapters) != 1 || ranged.Pagination.TotalCount != 1 || ranged.Chapters[0].ChapterId != "chapter-c" {
		t.Fatalf("half-open overlap range: %v %v", ranged, err)
	}
	request.TenantId = uuid.NewString()
	_, err = server.ListDVRChapters(context.Background(), request)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign chapters: %v", err)
	}
}
