package managedplacementoutbox

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

type placementClient struct {
	recordErr error
	clearErr  error
	recorded  []string
	cleared   []string
}

func (c *placementClient) RecordStreamActiveCluster(_ context.Context, streamID, tenantID, clusterID string) (*commodorepb.RecordStreamActiveClusterResponse, error) {
	c.recorded = append(c.recorded, streamID+":"+tenantID+":"+clusterID)
	return &commodorepb.RecordStreamActiveClusterResponse{Updated: true}, c.recordErr
}

func (c *placementClient) ClearStreamActiveCluster(_ context.Context, streamID, clusterID, tenantID string) (*commodorepb.ClearStreamActiveClusterResponse, error) {
	c.cleared = append(c.cleared, streamID+":"+tenantID+":"+clusterID)
	return &commodorepb.ClearStreamActiveClusterResponse{Cleared: true}, c.clearErr
}

func TestWriterRejectsIncompleteAndPersistsDesiredState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer := NewWriter(db)
	if err := writer.Enqueue(context.Background(), "", "tenant", "cluster", true); err == nil {
		t.Fatal("incomplete placement accepted")
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.managed_stream_placement_outbox")).
		WithArgs("10000000-0000-0000-0000-000000000001", "20000000-0000-0000-0000-000000000001", "30000000-0000-0000-0000-000000000001", true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := writer.Enqueue(context.Background(), "10000000-0000-0000-0000-000000000001", "20000000-0000-0000-0000-000000000001", "30000000-0000-0000-0000-000000000001", true); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRetriesFailureAndRevisionGuardsSettlement(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &placementClient{recordErr: errors.New("commodore down")}
	worker := NewWorker(db, client, logging.NewLogger())
	rows := sqlmock.NewRows([]string{"id", "stream_id", "tenant_id", "cluster_id", "desired_active", "revision", "attempts"}).
		AddRow(int64(9), "stream-1", "tenant-1", "cluster-1", true, int64(4), int32(2))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.managed_stream_placement_outbox")).WithArgs(int64(9), int64(4), worker.leaseOwner).WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if len(client.recorded) != 1 || len(client.cleared) != 0 {
		t.Fatalf("delivery calls = record %v clear %v", client.recorded, client.cleared)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerDeliversClearThenDeletesExactRevision(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &placementClient{}
	worker := NewWorker(db, client, logging.NewLogger())
	rows := sqlmock.NewRows([]string{"id", "stream_id", "tenant_id", "cluster_id", "desired_active", "revision", "attempts"}).
		AddRow(int64(8), "stream-1", "tenant-1", "cluster-1", false, int64(7), int32(0))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM foghorn.managed_stream_placement_outbox")).WithArgs(int64(8), int64(7), worker.leaseOwner).WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if len(client.cleared) != 1 || len(client.recorded) != 0 {
		t.Fatalf("delivery calls = record %v clear %v", client.recorded, client.cleared)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
