package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func diagnoseCtx(authType, userID string, operator bool) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, authType)
	if userID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userID)
		ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-op")
	}
	if operator {
		ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	}
	return ctx
}

func TestDiagnoseDVRRefusesNonOperatorsBeforeReadingTheDatabase(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"service token", diagnoseCtx("service", "", false)},
		{"service token claiming operator", diagnoseCtx("service", "user-op", true)},
		{"tenant JWT", diagnoseCtx("jwt", "user-1", false)},
		{"API token", diagnoseCtx("api_token", "user-op", true)},
		{"unauthenticated", context.Background()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			server := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
			_, err = server.DiagnoseDVR(tc.ctx, &foghornpb.DiagnoseDVRRequest{DvrHash: "dvr-1"})
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("got %v, want PermissionDenied", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDiagnoseDVRRequiresHash(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	_, err := server.DiagnoseDVR(diagnoseCtx("jwt", "user-op", true), &foghornpb.DiagnoseDVRRequest{DvrHash: "  "})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument", err)
	}
}

func TestDiagnoseDVRNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs("dvr-missing").WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}))
	server := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	_, err = server.DiagnoseDVR(diagnoseCtx("jwt", "user-op", true), &foghornpb.DiagnoseDVRRequest{DvrHash: "dvr-missing"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestDiagnoseDVROperatorReadsEveryPart(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("FROM foghorn.artifacts").WithArgs("dvr-1").WillReturnRows(sqlmock.NewRows(make([]string, 29)).AddRow(
		"dvr-1", "tenant-b", "live+s", "dvr-int", "recording", "",
		"cluster-eu", "", false,
		"local", "failed", "timeout",
		now, "node-1", int32(3),
		false, "", int32(0),
		now, nil, int64(0), int64(1024),
		now.Add(24*time.Hour), nil,
		"fixed_interval", int32(3600), false,
		"gen-1", "pending",
	))
	mock.ExpectQuery("FROM foghorn.dvr_segments").WithArgs("dvr-1").WillReturnRows(
		sqlmock.NewRows([]string{"c", "a", "b", "d", "s"}).AddRow(int64(3), int64(0), int64(9000), int64(8000), int64(300)))
	mock.ExpectQuery("GROUP BY status").WithArgs("dvr-1").WillReturnRows(
		sqlmock.NewRows([]string{"status", "count"}).AddRow("uploaded", int64(2)).AddRow("lost_local", int64(1)))
	mock.ExpectQuery("prior_end").WithArgs("dvr-1", int32(dvrDiagnosisGapLimit)).WillReturnRows(
		sqlmock.NewRows([]string{"s", "e", "q", "t"}).AddRow(int64(4000), int64(5000), int64(3), int64(1)))
	chapterCols := make([]string, 18)
	mock.ExpectQuery("FROM foghorn.dvr_chapters").WithArgs("dvr-1", int32(1000)).WillReturnRows(
		sqlmock.NewRows(chapterCols).AddRow(
			"ch-1", "fixed_interval", "failed_permanent", int64(0), int64(3600000), false, int32(4),
			"source missing", "", now, nil, nil, now, int32(0), false, "", nil, int64(3600000)))
	mock.ExpectQuery("state IN \\('closed', 'finalizing'\\)").WithArgs("dvr-1", int32(1000)).WillReturnRows(
		sqlmock.NewRows(chapterCols).AddRow(
			"ch-2", "fixed_interval", "closed", int64(3600000), int64(7200000), false, int32(0),
			"", "", nil, nil, nil, now, int32(0), false, "", nil, nil))

	server := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	resp, err := server.DiagnoseDVR(diagnoseCtx("jwt", "user-op", true), &foghornpb.DiagnoseDVRRequest{DvrHash: "dvr-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	rec := resp.GetRecording()
	if rec.GetTenantId() != "tenant-b" || rec.GetSyncStatus() != "failed" || rec.GetStartDispatchState() != "pending" || rec.GetRetentionUntil() == nil || rec.GetEndedAt() != nil {
		t.Fatalf("recording = %+v", rec)
	}
	seg := resp.GetSegments()
	if seg.GetCount() != 3 || seg.GetGapCount() != 1 || len(seg.GetGaps()) != 1 || seg.GetGaps()[0].GetStartMs() != 4000 || len(seg.GetByStatus()) != 2 {
		t.Fatalf("segments = %+v", seg)
	}
	ch := resp.GetChapters()
	if len(ch) != 1 || ch[0].GetFinalizeJobId() != "chapter-finalize-v2-4-ch-1" || ch[0].GetLastFailureReason() != "source missing" ||
		ch[0].ActualMediaStartMs != nil || ch[0].GetActualMediaEndMs() != 3600000 {
		t.Fatalf("chapters = %+v", ch)
	}
	pending := resp.GetPendingFinalize()
	if len(pending) != 1 || pending[0].GetChapterId() != "ch-2" || pending[0].GetFinalizeJobId() != "" || pending[0].GetQueuedAt() == nil {
		t.Fatalf("pending = %+v", pending)
	}
}
