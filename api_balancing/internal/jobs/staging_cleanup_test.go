package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

// stubDeleter records deletes and can be made to fail.
type stubDeleter struct {
	deleted []string
	fail    bool
}

func (d *stubDeleter) Delete(_ context.Context, key string) error {
	d.deleted = append(d.deleted, key)
	if d.fail {
		return fmt.Errorf("s3 delete failed")
	}
	return nil
}

func TestStagingCleanup_DeletesAndRemovesRow(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	del := &stubDeleter{}
	j := &StagingCleanupJob{
		db:             mockDB,
		s3:             del,
		logger:         logging.NewLogger(),
		batchSize:      100,
		backoffBase:    time.Minute,
		leaseTTL:       2 * time.Minute,
		itemTimeout:    30 * time.Second,
		localBackendID: "local-backend",
	}

	// Atomic lease-claim mints a fresh token per row (UPDATE ... FOR UPDATE SKIP LOCKED ... RETURNING).
	mock.ExpectQuery("UPDATE foghorn.staging_cleanup_queue.*SET leased_until.*lease_token = gen_random_uuid.*FOR UPDATE SKIP LOCKED.*RETURNING").
		WithArgs(100, int64((2 * time.Minute).Seconds())).
		WillReturnRows(sqlmock.NewRows([]string{"object_key", "attempts", "lease_token", "backend_id"}).AddRow("tenant-1/clips/hash.mp4.staging.att-1", 0, "tok-1", "local-backend"))
	// Successful delete removes the durable row, FENCED on the lease token.
	mock.ExpectExec("DELETE FROM foghorn.staging_cleanup_queue WHERE object_key = \\$1 AND lease_token = \\$2").
		WithArgs("tenant-1/clips/hash.mp4.staging.att-1", "tok-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	j.drain()

	if len(del.deleted) != 1 || del.deleted[0] != "tenant-1/clips/hash.mp4.staging.att-1" {
		t.Fatalf("expected the staging key to be deleted, got %v", del.deleted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A queued object recorded on a DIFFERENT backend than the cell's current store (a repoint since
// enqueue) must FAIL CLOSED — never deleted from the wrong store. The worker records a repoint deferral (backoff +
// lease release) and issues NO S3 delete.
func TestStagingCleanup_RepointFailsClosed(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	del := &stubDeleter{}
	j := &StagingCleanupJob{
		db:             mockDB,
		s3:             del,
		logger:         logging.NewLogger(),
		batchSize:      100,
		backoffBase:    time.Minute,
		leaseTTL:       2 * time.Minute,
		itemTimeout:    30 * time.Second,
		localBackendID: "NEW-backend",
	}

	// The claimed row was written to OLD-backend; the cell now points at NEW-backend.
	mock.ExpectQuery("UPDATE foghorn.staging_cleanup_queue.*SET leased_until.*lease_token = gen_random_uuid.*FOR UPDATE SKIP LOCKED.*RETURNING").
		WithArgs(100, int64((2 * time.Minute).Seconds())).
		WillReturnRows(sqlmock.NewRows([]string{"object_key", "attempts", "lease_token", "backend_id"}).AddRow("k-old", 0, "tok-1", "OLD-backend"))
	// No DELETE — a backoff/lease-release UPDATE is issued instead (fail closed).
	mock.ExpectExec("UPDATE foghorn.staging_cleanup_queue\\s+SET attempts = attempts \\+ 1.*leased_until = NULL.*lease_token = NULL.*WHERE object_key = \\$1 AND lease_token = \\$4").
		WithArgs("k-old", sqlmock.AnyArg(), sqlmock.AnyArg(), "tok-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	j.drain()

	if len(del.deleted) != 0 {
		t.Fatalf("a repointed object must NOT be deleted from the current store, deleted %v", del.deleted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A queued object with a recorded non-empty backend_id but an EMPTY localBackendID (an unwired local fingerprint) must
// ALSO fail closed: a missing local identity is not proof of a match and must never license deleting from the current
// store.
func TestStagingCleanup_RecordedBackendWithoutLocalIdentityFailsClosed(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	del := &stubDeleter{}
	j := &StagingCleanupJob{
		db:             mockDB,
		s3:             del,
		logger:         logging.NewLogger(),
		batchSize:      100,
		backoffBase:    time.Minute,
		leaseTTL:       2 * time.Minute,
		itemTimeout:    30 * time.Second,
		localBackendID: "", // deliberately unwired
	}

	mock.ExpectQuery("UPDATE foghorn.staging_cleanup_queue.*SET leased_until.*lease_token = gen_random_uuid.*FOR UPDATE SKIP LOCKED.*RETURNING").
		WithArgs(100, int64((2 * time.Minute).Seconds())).
		WillReturnRows(sqlmock.NewRows([]string{"object_key", "attempts", "lease_token", "backend_id"}).AddRow("k-x", 0, "tok-1", "SOME-backend"))
	// No DELETE — a backoff/lease-release UPDATE is issued instead (fail closed).
	mock.ExpectExec("UPDATE foghorn.staging_cleanup_queue\\s+SET attempts = attempts \\+ 1.*leased_until = NULL.*lease_token = NULL.*WHERE object_key = \\$1 AND lease_token = \\$4").
		WithArgs("k-x", sqlmock.AnyArg(), sqlmock.AnyArg(), "tok-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	j.drain()

	if len(del.deleted) != 0 {
		t.Fatalf("an unmatched recorded backend must NOT be deleted from the current store, deleted %v", del.deleted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStagingCleanup_FailureBacksOffAndReleasesLease(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	del := &stubDeleter{fail: true}
	j := &StagingCleanupJob{
		db:             mockDB,
		s3:             del,
		logger:         logging.NewLogger(),
		batchSize:      100,
		backoffBase:    time.Minute,
		leaseTTL:       2 * time.Minute,
		itemTimeout:    30 * time.Second,
		localBackendID: "local-backend",
	}

	mock.ExpectQuery("UPDATE foghorn.staging_cleanup_queue.*SET leased_until.*lease_token = gen_random_uuid.*FOR UPDATE SKIP LOCKED.*RETURNING").
		WithArgs(100, int64((2 * time.Minute).Seconds())).
		WillReturnRows(sqlmock.NewRows([]string{"object_key", "attempts", "lease_token", "backend_id"}).AddRow("k1", 2, "tok-2", "local-backend"))
	// A failed delete bumps attempts, reschedules, and RELEASES the lease (token-fenced); row not removed.
	mock.ExpectExec("UPDATE foghorn.staging_cleanup_queue\\s+SET attempts = attempts \\+ 1.*leased_until = NULL.*lease_token = NULL.*WHERE object_key = \\$1 AND lease_token = \\$4").
		WithArgs("k1", int64((time.Minute * 3).Seconds()), "s3 delete failed", "tok-2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	j.drain()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
