package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
)

func TestSavedDVRChapterConnectionReadsOnlyMaterializedRows(t *testing.T) {
	server, mock := withControlDBRpcMore(t)
	mock.ExpectQuery("GetDVRArtifactTenantID").WithArgs("recording").WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant"))
	mock.ExpectQuery("CountSavedDVRChapters").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("ListSavedDVRChapters").WillReturnRows(sqlmock.NewRows([]string{"chapter_id"}))
	response, err := server.ListDVRChapters(context.Background(), &pb.ListDVRChaptersRequest{DvrArtifactId: "recording", TenantId: "tenant", Pagination: &commonpb.CursorPaginationRequest{First: 2}})
	if err != nil || response.GetPagination() == nil {
		t.Fatalf("saved chapters have no keyset catalog: %v %v", response, err)
	}
	if len(response.Chapters) != 0 || response.Pagination.TotalCount != 0 {
		t.Fatalf("saved chapter read invented future chapters: %v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
