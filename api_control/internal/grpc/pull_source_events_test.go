package grpc

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestRecordPullSourceEventResolvedStampsActiveIngestCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO commodore.pull_source_events
		            (tenant_id, stream_id, internal_name, event_kind, detail)
		VALUES      ($1::uuid, NULLIF($2, '')::uuid, $3, $4, NULLIF($5, ''))
	`)).
		WithArgs("tenant-1", "stream-1", "internal-1", "resolved", "media-eu-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE commodore.streams").
		WithArgs("media-eu-1", "stream-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	_, err = server.RecordPullSourceEvent(context.Background(), &pb.RecordPullSourceEventRequest{
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
	_, err = server.RecordPullSourceEvent(context.Background(), &pb.RecordPullSourceEventRequest{
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
