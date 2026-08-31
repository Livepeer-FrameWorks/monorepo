package signingkeyuseoutbox

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type signingKeyClient struct {
	err   error
	calls []string
}

func (c *signingKeyClient) RecordSigningKeyUse(_ context.Context, tenantID, kid string) error {
	c.calls = append(c.calls, tenantID+":"+kid)
	return c.err
}

func TestWriterPersistsObservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer := NewWriter(db)
	if err := writer.RecordSigningKeyUse(context.Background(), "", "kid"); err == nil {
		t.Fatal("incomplete observation accepted")
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.signing_key_use_outbox")).
		WithArgs("10000000-0000-0000-0000-000000000001", "kid-a").
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := writer.RecordSigningKeyUse(context.Background(), "10000000-0000-0000-0000-000000000001", "kid-a"); err != nil {
		t.Fatalf("RecordSigningKeyUse: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncRecorderCoalescesPendingTenantKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := NewAsyncRecorder(NewWriter(db), logging.NewLogger())
	tenantID := "10000000-0000-0000-0000-000000000001"
	if err := recorder.RecordSigningKeyUse(context.Background(), tenantID, "kid-a"); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordSigningKeyUse(context.Background(), tenantID, "kid-a"); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.signing_key_use_outbox")).
		WithArgs(tenantID, "kid-a").WillReturnResult(sqlmock.NewResult(1, 1))
	recorder.flush(context.Background())
	recorder.mu.Lock()
	pending := len(recorder.pending)
	recorder.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending observations = %d, want 0", pending)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRetriesCentralFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &signingKeyClient{err: errors.New("commodore down")}
	worker := NewWorker(db, client, logging.NewLogger())
	rows := sqlmock.NewRows([]string{"id", "tenant_id", "kid", "revision", "attempts"}).
		AddRow(int64(4), "tenant-a", "kid-a", int64(3), int32(1))
	mock.ExpectQuery(regexp.QuoteMeta("WITH candidates AS")).WithArgs(worker.leaseOwner, int32(32)).WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.signing_key_use_outbox")).WithArgs(int64(4), int64(3), worker.leaseOwner).WillReturnResult(sqlmock.NewResult(0, 1))
	worker.drain(context.Background())
	if len(client.calls) != 1 {
		t.Fatalf("delivery calls = %v", client.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
