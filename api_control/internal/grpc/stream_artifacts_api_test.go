package grpc

import (
	"context"
	"database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func streamArtifactReadContext(t *testing.T) context.Context {
	t.Helper()
	ctx := retentionAuthCtx(uuid.NewString())
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	return context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
}

func TestStreamArtifactsUseKeysetPagination(t *testing.T) {
	server, mock, done := newRetentionServer(t)
	defer done()
	ctx := streamArtifactReadContext(t)
	stream := uuid.NewString()
	mock.ExpectQuery("StreamMediaCatalogExists").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT kind, COUNT`).WillReturnRows(sqlmock.NewRows([]string{"kind", "count"}))
	mock.ExpectQuery(`ORDER BY created_at DESC, id DESC LIMIT`).WillReturnRows(sqlmock.NewRows([]string{"kind"}))
	response, err := server.ListStreamArtifacts(ctx, &pb.ListStreamArtifactsRequest{StreamId: stream, Kind: "dvr", Pagination: &commonpb.CursorPaginationRequest{First: 2}})
	if err != nil || response.GetPagination() == nil || len(response.GetArtifacts()) != 0 {
		t.Fatalf("stream media catalog has no keyset read API: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamArtifactsRejectMalformedCursor(t *testing.T) {
	server, _, done := newRetentionServer(t)
	defer done()
	_, err := server.ListStreamArtifacts(streamArtifactReadContext(t), &pb.ListStreamArtifactsRequest{StreamId: uuid.NewString(), Kind: "clip", Pagination: &commonpb.CursorPaginationRequest{First: 2, After: proto.String("malformed")}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed media cursor = %v, want InvalidArgument", err)
	}
}

func TestDVRRecordingLookupUsesTenantCatalog(t *testing.T) {
	server, mock, done := newRetentionServer(t)
	defer done()
	mock.ExpectQuery("GetDVRRecordingForTenant").WillReturnError(sql.ErrNoRows)
	_, err := server.GetDVRRecording(streamArtifactReadContext(t), &pb.GetDVRRecordingRequest{Id: uuid.NewString()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("catalog recording lookup = %v, want NotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamArtifactsCursorWindowAndTenantFence(t *testing.T) {
	for _, backward := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "backward"}[backward], func(t *testing.T) {
			server, mock, done := newRetentionServer(t)
			defer done()
			tenant, stream := uuid.NewString(), uuid.NewString()
			ctx := context.WithValue(streamArtifactReadContext(t), ctxkeys.KeyTenantID, tenant)
			ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
			cursorTime := time.Date(2026, 11, 18, 0, 0, 0, 123456000, time.UTC)
			cursor := pagination.EncodeCursor(cursorTime, uuid.NewString())
			decoded, _ := pagination.DecodeCursor(cursor)
			request := &commonpb.CursorPaginationRequest{First: 2, After: &cursor}
			order, comparison := "DESC", "<"
			if backward {
				request = &commonpb.CursorPaginationRequest{Last: 2, Before: &cursor}
				order, comparison = "ASC", ">"
			}
			mock.ExpectQuery("StreamMediaCatalogExists").WithArgs(tenant, stream).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			mock.ExpectQuery(`SELECT COUNT\(\*\)`).WithArgs(tenant, stream, "clip").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(8))
			mock.ExpectQuery(`SELECT kind, COUNT`).WithArgs(tenant, stream).WillReturnRows(sqlmock.NewRows([]string{"kind", "count"}).AddRow("clip", 8))
			rows := storageArtifactRows()
			for _, id := range ids {
				rows.AddRow("clip", id, "hash-"+id, "play", stream, "Stream", "Clip", "", nil, nil, "ready", nil, cursorTime, cursorTime, nil, "", "", "", "", false, nil, nil, nil, nil, nil, "", "", "", "")
			}
			mock.ExpectQuery(`AND \(created_at, id\) `+comparison+` \(\$4, \$5\) ORDER BY created_at `+order+`, id `+order+` LIMIT \$6`).WithArgs(tenant, stream, "clip", cursorTime, decoded.ID, 3).WillReturnRows(rows)
			response, err := server.ListStreamArtifacts(ctx, &pb.ListStreamArtifactsRequest{StreamId: stream, Kind: "clip", Pagination: request})
			if err != nil {
				t.Fatal(err)
			}
			wantFirst, wantLast := ids[0], ids[1]
			if backward {
				wantFirst, wantLast = wantLast, wantFirst
			}
			if len(response.Artifacts) != 2 || response.Artifacts[0].Id != wantFirst || response.Artifacts[1].Id != wantLast {
				t.Fatalf("wrong cursor window: %v", response)
			}
			if !response.Pagination.HasNextPage || !response.Pagination.HasPreviousPage || response.Pagination.TotalCount != 8 {
				t.Fatalf("wrong page metadata: %v", response.Pagination)
			}
			first, err := pagination.DecodeCursor(response.Pagination.GetStartCursor())
			if err != nil || first.ID != wantFirst || !first.Timestamp.Equal(cursorTime) {
				t.Fatalf("unstable microsecond cursor: %v %v", first, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamArtifactsRefuseOtherTenantStream(t *testing.T) {
	server, mock, done := newRetentionServer(t)
	defer done()
	mock.ExpectQuery("StreamMediaCatalogExists").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	_, err := server.ListStreamArtifacts(streamArtifactReadContext(t), &pb.ListStreamArtifactsRequest{StreamId: uuid.NewString(), Kind: "dvr"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("other tenant stream = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
