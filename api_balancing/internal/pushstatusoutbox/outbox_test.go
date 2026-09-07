package pushstatusoutbox

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEnqueueCarriesMistEventFence(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs("10000000-0000-0000-0000-000000000001", "20000000-0000-0000-0000-000000000002", "pushing", ReasonConnected, sql.NullString{}, int64(1234)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := Enqueue(context.Background(), db,
		"10000000-0000-0000-0000-000000000001", "20000000-0000-0000-0000-000000000002",
		"pushing", ReasonConnected, nil, 1234); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type statusClient struct {
	err   error
	calls []string
}

func (c *statusClient) UpdatePushTargetStatus(_ context.Context, targetID, tenantID, status, reasonCode string, _ *string) error {
	c.calls = append(c.calls, targetID+":"+tenantID+":"+status+":"+reasonCode)
	return c.err
}

func TestWorkerRetriesPushStatusAndKeepsExactRevision(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &statusClient{err: errors.New("offline")}
	worker := NewWorker(db, client, logging.NewLogger())
	rows := sqlmock.NewRows([]string{"id", "target_id", "tenant_id", "status", "reason_code", "last_error", "revision", "attempts"}).
		AddRow(int64(5), "target-1", "tenant-1", "failed", ReasonProcessError, "push failed", int64(3), int32(1))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.push_target_status_outbox")).WithArgs(int64(5), int64(3), worker.leaseOwner).WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if len(client.calls) != 1 {
		t.Fatalf("calls = %v", client.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerSettlesDeliveredPushStatusByRevision(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &statusClient{}
	worker := NewWorker(db, client, logging.NewLogger())
	rows := sqlmock.NewRows([]string{"id", "target_id", "tenant_id", "status", "reason_code", "last_error", "revision", "attempts"}).
		AddRow(int64(5), "target-1", "tenant-1", "pushing", ReasonConnected, nil, int64(4), int32(0))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM foghorn.push_target_status_outbox")).WithArgs(int64(5), int64(4), worker.leaseOwner).WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if len(client.calls) != 1 {
		t.Fatalf("calls = %v", client.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerTreatsDeletedTargetAsTerminalTombstone(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &statusClient{err: status.Error(codes.NotFound, "target deleted")}
	outcomes := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_push_status_outbox_outcomes_total"}, []string{"outcome"})
	worker := NewWorker(db, client, logging.NewLogger(), &Metrics{Outcomes: outcomes})
	rows := sqlmock.NewRows([]string{"id", "target_id", "tenant_id", "status", "reason_code", "last_error", "revision", "attempts"}).
		AddRow(int64(8), "target-deleted", "tenant-1", "idle", ReasonStopped, nil, int64(2), int32(4))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM foghorn.push_target_status_outbox")).WithArgs(int64(8), int64(2), worker.leaseOwner).WillReturnResult(sqlmock.NewResult(0, 1))

	worker.drain(context.Background())

	if got := testutil.ToFloat64(outcomes.WithLabelValues("terminal_not_found")); got != 1 {
		t.Fatalf("terminal_not_found metric = %v, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
