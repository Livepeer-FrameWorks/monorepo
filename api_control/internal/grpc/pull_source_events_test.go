package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecordPullSourceEventResolvedStampsActiveIngestCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("INSERT INTO commodore.pull_source_events").
		WithArgs("tenant-1", "stream-1", "internal-1", "resolved", "media-eu-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE commodore.streams").
		WithArgs("media-eu-1", pullClaimToken("stream-1"), "stream-1", "tenant-1", int64(activeIngestLease.Seconds())).
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	_, err = server.RecordPullSourceEvent(serviceCtx(), &commodorepb.RecordPullSourceEventRequest{
		TenantId:     "tenant-1",
		StreamId:     "stream-1",
		InternalName: "internal-1",
		EventKind:    "resolved",
		Detail:       "media-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRecordPullSourceEventNonResolvedOnlyLogsEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("INSERT INTO commodore.pull_source_events").
		WithArgs("tenant-1", "stream-1", "internal-1", "disabled", "").
		WillReturnResult(sqlmock.NewResult(1, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	_, err = server.RecordPullSourceEvent(serviceCtx(), &commodorepb.RecordPullSourceEventRequest{
		TenantId:     "tenant-1",
		StreamId:     "stream-1",
		InternalName: "internal-1",
		EventKind:    "disabled",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Pull placement is service-to-service state. The shared interceptor also
// accepts JWTs, so without this check a logged-in user could write another
// tenant's source-event history and steer a pull stream's placement.
func TestRecordPullSourceEvent_RejectsNonServiceAuth(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	_, err = server.RecordPullSourceEvent(ctx, &commodorepb.RecordPullSourceEventRequest{
		TenantId:     "tenant-1",
		StreamId:     "stream-1",
		InternalName: "internal-1",
		EventKind:    "resolved",
		Detail:       "media-eu-1",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a statement ran for an unauthorized caller: %v", err)
	}
}
