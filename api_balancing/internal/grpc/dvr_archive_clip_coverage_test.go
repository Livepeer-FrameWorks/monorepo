package grpc

import (
	"context"
	"slices"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestListDVRChaptersArchivedClipBatch(t *testing.T) {
	const tenant = "10000000-0000-4000-8000-000000000001"
	const start, end int64 = 1700000000000, 1700000004000
	server, mock := newDispatchServer(t)
	serviceCtx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "service")
	req := &foghorncontrolpb.ListDVRChaptersRequest{
		TenantId:              tenant,
		Mode:                  foghorncontrolpb.ArchivedClipCoverageMode,
		RangeStartMs:          start,
		RangeEndMs:            end,
		ArchiveDvrArtifactIds: []string{"dvr-a", "dvr-b"},
	}

	if _, err := server.ListDVRChapters(t.Context(), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("lookup without service authentication = %v, want PermissionDenied", err)
	}
	oversized := &foghorncontrolpb.ListDVRChaptersRequest{
		TenantId:              tenant,
		Mode:                  req.GetMode(),
		RangeStartMs:          start,
		RangeEndMs:            end,
		ArchiveDvrArtifactIds: slices.Repeat([]string{"dvr-a"}, foghorncontrolpb.ArchivedClipCoverageBatchSize+1),
	}
	if _, err := server.ListDVRChapters(serviceCtx, oversized); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized lookup = %v, want InvalidArgument", err)
	}

	mock.ExpectQuery(`a\.tenant_id = \$1 AND a\.artifact_hash = ANY\(\$2::varchar\[\]\)`).
		WithArgs(tenant, `{"dvr-a","dvr-b"}`, start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"artifact_hash", "chapter_id", "state", "playback_artifact_hash", "actual_media_start_ms", "actual_media_end_ms",
		}).AddRow("dvr-b", "chapter-b", "reclaimed", "chapter-hash-b", start-1000, end))
	resp, err := server.ListDVRChapters(serviceCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	chapters := resp.GetChapters()
	if len(chapters) != 1 || chapters[0].GetArchiveDvrArtifactId() != "dvr-b" || chapters[0].GetPlaybackArtifactHash() != "chapter-hash-b" {
		t.Fatalf("coverage lost the chapter and owning DVR association: %v", chapters)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
