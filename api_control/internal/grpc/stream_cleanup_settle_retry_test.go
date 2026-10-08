package grpc

import (
	"context"
	"errors"
	"testing"

	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

type deliveredStreamCleanup struct{}

func (deliveredStreamCleanup) Dispatch(context.Context, streamCleanupOutboxRow) ([]string, error) {
	return nil, nil
}

// TestStreamCleanupSettlementReplaysFinalizeInOneLayer settles a delivered
// stream cleanup while every finalize transaction aborts with SQLSTATE 40001.
// The outbox worker replays settlement and finalize runs one transaction per
// replay, so the settlement gives up on the retryable error after
// DefaultRetryAttempts transactions instead of running that number squared.
func TestStreamCleanupSettlementReplaysFinalizeInOneLayer(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	retryable := &pq.Error{Code: "40001", Message: "restart read required"}
	for range fwdb.DefaultRetryAttempts {
		mock.ExpectBegin()
		mock.ExpectQuery("SET status = 'completed'").WillReturnError(retryable)
		mock.ExpectRollback()
	}

	logger, hook := logrustest.NewNullLogger()
	server := &CommodoreServer{db: db, logger: logger}
	worker := &outbox.Worker[streamCleanupOutboxRow]{
		Config:     streamCleanupOutboxConfig(),
		Store:      &streamCleanupOutboxStore{server: server},
		Dispatcher: deliveredStreamCleanup{},
		Logger:     logger,
	}
	worker.TryDispatch(context.Background(), streamCleanupClaimID(testTenantID, "stream-1"), 0, streamCleanupOutboxRow{})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("settlement ran fewer than %d finalize transactions: %v", fwdb.DefaultRetryAttempts, err)
	}
	entry := hook.LastEntry()
	if entry == nil {
		t.Fatal("settlement failure was not logged")
	}
	settleErr, _ := entry.Data["error"].(error)
	if !errors.Is(settleErr, retryable) {
		t.Fatalf("settlement ended with %v; want the replays exhausted on the retryable error after exactly %d finalize transactions", settleErr, fwdb.DefaultRetryAttempts)
	}
}
