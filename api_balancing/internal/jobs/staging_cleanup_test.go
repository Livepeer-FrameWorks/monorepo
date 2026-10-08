package jobs

import (
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

// stubDeleter records multi-object delete requests and fails the keys in fail.
type stubDeleter struct {
	requests [][]string
	fail     map[string]bool
}

func (d *stubDeleter) DeleteKeys(_ context.Context, keys []string) map[string]error {
	d.requests = append(d.requests, append([]string(nil), keys...))
	failed := make(map[string]error)
	for _, k := range keys {
		if d.fail[k] {
			failed[k] = fmt.Errorf("s3 delete failed")
		}
	}
	return failed
}

// captureArg matches any value and keeps the last one it saw.
type captureArg struct{ value driver.Value }

func (c *captureArg) Match(v driver.Value) bool {
	c.value = v
	return true
}

// sameArg matches only the value a captureArg saw.
type sameArg struct{ of *captureArg }

func (s sameArg) Match(v driver.Value) bool { return s.of.value != nil && v == s.of.value }

// textArray matches a text[] parameter holding exactly want.
type textArray []string

func (want textArray) Match(v driver.Value) bool {
	var got pq.StringArray
	if err := got.Scan(v); err != nil {
		return false
	}
	return reflect.DeepEqual([]string(got), []string(want))
}

const (
	claimSQL  = "UPDATE foghorn.staging_cleanup_queue q\\s+SET leased_until.*lease_token = \\$2::text.*FOR UPDATE SKIP LOCKED.*RETURNING"
	settleSQL = "DELETE FROM foghorn.staging_cleanup_queue\\s+WHERE object_key = ANY\\(\\$1::text\\[\\]\\) AND lease_token = \\$2::text"
	failSQL   = "UPDATE foghorn.staging_cleanup_queue q\\s+SET attempts = q.attempts \\+ 1.*leased_until = NULL, lease_token = NULL.*WHERE q.object_key = f.object_key AND q.lease_token = \\$2::text"
)

var claimCols = []string{"object_key", "attempts", "backend_id"}

func newStagingCleanupTestJob(t *testing.T, del *stubDeleter, batchSize int, localBackendID string) (*StagingCleanupJob, sqlmock.Sqlmock) {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mockDB.Close() })
	return &StagingCleanupJob{
		db:             mockDB,
		s3:             del,
		logger:         logging.NewLogger(),
		batchSize:      batchSize,
		batchesPerPass: 20,
		backoffBase:    time.Minute,
		leaseTTL:       2 * time.Minute,
		localBackendID: localBackendID,
		stopCh:         make(chan struct{}),
	}, mock
}

func expectClaim(mock sqlmock.Sqlmock, token *captureArg, bucket any, batch int, rows *sqlmock.Rows) {
	mock.ExpectQuery(claimSQL).
		WithArgs(int64((2 * time.Minute).Seconds()), token, bucket, int64(stagingCleanupClaimBuckets), batch).
		WillReturnRows(rows)
}

// A short claim deletes its objects in one request and drops their rows in one token-fenced statement, and ends the
// pass: fewer than a batch of rows were due.
func TestStagingCleanup_DeletesBatchAndSettlesInOneStatement(t *testing.T) {
	del := &stubDeleter{}
	j, mock := newStagingCleanupTestJob(t, del, 100, "local-backend")
	token := &captureArg{}
	expectClaim(mock, token, int64(-1), 100, sqlmock.NewRows(claimCols).
		AddRow("tenant-1/clips/hash.mp4.staging.att-1", 0, "local-backend").
		AddRow("tenant-1/clips/hash.mp4.staging.att-2", 0, "local-backend"))
	mock.ExpectExec(settleSQL).
		WithArgs(textArray{"tenant-1/clips/hash.mp4.staging.att-1", "tenant-1/clips/hash.mp4.staging.att-2"}, sameArg{token}).
		WillReturnResult(sqlmock.NewResult(0, 2))

	j.drain()

	if len(del.requests) != 1 || len(del.requests[0]) != 2 {
		t.Fatalf("expected one request carrying both keys, got %v", del.requests)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A queued object recorded on a DIFFERENT backend than the cell's current store (a repoint since enqueue) must FAIL
// CLOSED — never deleted from the wrong store. The worker records a repoint deferral (backoff + lease release) and
// sends NO S3 request.
func TestStagingCleanup_RepointFailsClosed(t *testing.T) {
	del := &stubDeleter{}
	j, mock := newStagingCleanupTestJob(t, del, 100, "NEW-backend")
	token := &captureArg{}
	expectClaim(mock, token, int64(-1), 100, sqlmock.NewRows(claimCols).AddRow("k-old", 0, "OLD-backend"))
	mock.ExpectExec(failSQL).
		WithArgs(int64(60), sameArg{token}, textArray{"k-old"},
			textArray{"storage backend repointed (recorded OLD-backend != current NEW-backend); refusing to delete from the wrong store"}).
		WillReturnResult(sqlmock.NewResult(0, 1))

	j.drain()

	if len(del.requests) != 0 {
		t.Fatalf("a repointed object must NOT be deleted from the current store, requests %v", del.requests)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A queued object with a recorded non-empty backend_id but an EMPTY localBackendID (an unwired local fingerprint) must
// ALSO fail closed: a missing local identity is not proof of a match and must never license deleting from the current
// store.
func TestStagingCleanup_RecordedBackendWithoutLocalIdentityFailsClosed(t *testing.T) {
	del := &stubDeleter{}
	j, mock := newStagingCleanupTestJob(t, del, 100, "")
	token := &captureArg{}
	expectClaim(mock, token, int64(-1), 100, sqlmock.NewRows(claimCols).AddRow("k-x", 0, "SOME-backend"))
	mock.ExpectExec(failSQL).
		WithArgs(int64(60), sameArg{token}, textArray{"k-x"}, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	j.drain()

	if len(del.requests) != 0 {
		t.Fatalf("an unmatched recorded backend must NOT be deleted from the current store, requests %v", del.requests)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Keys S3 failed to delete are released with a backoff and their error, under the same token; the rest of the batch is
// removed.
func TestStagingCleanup_FailedKeysBackOffAndReleaseLease(t *testing.T) {
	del := &stubDeleter{fail: map[string]bool{"k1": true}}
	j, mock := newStagingCleanupTestJob(t, del, 100, "local-backend")
	token := &captureArg{}
	expectClaim(mock, token, int64(-1), 100, sqlmock.NewRows(claimCols).
		AddRow("k1", 2, "local-backend").AddRow("k2", 0, "local-backend"))
	mock.ExpectExec(settleSQL).WithArgs(textArray{"k2"}, sameArg{token}).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(failSQL).
		WithArgs(int64(60), sameArg{token}, textArray{"k1"}, textArray{"s3 delete failed"}).
		WillReturnResult(sqlmock.NewResult(0, 1))

	j.drain()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A full first claim means a backlog: the pass moves to one bucket, stays on it while claims come back full, and walks
// to the next bucket when one comes back short.
func TestStagingCleanup_BacklogDrainsBucketByBucket(t *testing.T) {
	del := &stubDeleter{}
	j, mock := newStagingCleanupTestJob(t, del, 2, "local-backend")
	full := func(a, b string) *sqlmock.Rows {
		return sqlmock.NewRows(claimCols).AddRow(a, 0, "local-backend").AddRow(b, 0, "local-backend")
	}
	settled := sqlmock.NewResult(0, 2)
	tokens := []*captureArg{{}, {}, {}, {}, {}}
	buckets := []*captureArg{{}, {}, {}, {}, {}}

	expectClaim(mock, tokens[0], buckets[0], 2, full("k1", "k2"))
	mock.ExpectExec(settleSQL).WithArgs(textArray{"k1", "k2"}, sameArg{tokens[0]}).WillReturnResult(settled)
	expectClaim(mock, tokens[1], buckets[1], 2, full("k3", "k4"))
	mock.ExpectExec(settleSQL).WithArgs(textArray{"k3", "k4"}, sameArg{tokens[1]}).WillReturnResult(settled)
	expectClaim(mock, tokens[2], buckets[2], 2, sqlmock.NewRows(claimCols).AddRow("k5", 0, "local-backend"))
	mock.ExpectExec(settleSQL).WithArgs(textArray{"k5"}, sameArg{tokens[2]}).WillReturnResult(sqlmock.NewResult(0, 1))
	expectClaim(mock, tokens[3], buckets[3], 2, full("k6", "k7"))
	mock.ExpectExec(settleSQL).WithArgs(textArray{"k6", "k7"}, sameArg{tokens[3]}).WillReturnResult(settled)
	// Every remaining bucket comes back empty; the pass ends once all sixteen were visited.
	for range stagingCleanupClaimBuckets - 1 {
		expectClaim(mock, &captureArg{}, sqlmock.AnyArg(), 2, sqlmock.NewRows(claimCols))
	}

	j.drain()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	first := buckets[1].value.(int64)
	if buckets[0].value != int64(-1) || first < 0 || first >= stagingCleanupClaimBuckets {
		t.Fatalf("claims started at bucket %v then %v, want -1 then a bucket in [0,%d)", buckets[0].value, first, stagingCleanupClaimBuckets)
	}
	if buckets[2].value != first || buckets[3].value != (first+1)%stagingCleanupClaimBuckets {
		t.Fatalf("bucket sequence %v %v %v, want %d %d %d", buckets[1].value, buckets[2].value, buckets[3].value, first, first, (first+1)%stagingCleanupClaimBuckets)
	}
	seen := map[any]bool{}
	for _, tok := range tokens[:4] {
		if seen[tok.value] {
			t.Fatalf("lease token %v reused across claims", tok.value)
		}
		seen[tok.value] = true
	}
}

// A pass processes at most batchesPerPass batches however large the backlog is; the next pass continues it.
func TestStagingCleanup_PassIsBounded(t *testing.T) {
	del := &stubDeleter{}
	j, mock := newStagingCleanupTestJob(t, del, 1, "local-backend")
	j.batchesPerPass = 3
	for i := range 3 {
		expectClaim(mock, &captureArg{}, sqlmock.AnyArg(), 1, sqlmock.NewRows(claimCols).AddRow(fmt.Sprintf("k%d", i), 0, "local-backend"))
		mock.ExpectExec(settleSQL).WillReturnResult(sqlmock.NewResult(0, 1))
	}

	j.drain()

	if len(del.requests) != 3 {
		t.Fatalf("pass sent %d requests, want 3", len(del.requests))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Stop interrupts the pause between batches, so a pass never outlives Stop by more than the batch in flight.
func TestStagingCleanup_StopInterruptsPause(t *testing.T) {
	del := &stubDeleter{}
	j, mock := newStagingCleanupTestJob(t, del, 1, "local-backend")
	j.batchPause = time.Hour
	expectClaim(mock, &captureArg{}, sqlmock.AnyArg(), 1, sqlmock.NewRows(claimCols).AddRow("k1", 0, "local-backend"))
	mock.ExpectExec(settleSQL).WillReturnResult(sqlmock.NewResult(0, 1))
	close(j.stopCh)

	done := make(chan struct{})
	go func() {
		j.drain()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain kept pausing after Stop")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// jitterDuration stays inside its range and actually varies, so replicas started together drift apart.
func TestJitterDurationStaysInRangeAndVaries(t *testing.T) {
	const d = time.Minute
	seen := map[time.Duration]bool{}
	for range 1000 {
		got := jitterDuration(d, 0.8, 1.2)
		if got < 48*time.Second || got >= 72*time.Second {
			t.Fatalf("jitterDuration(1m, 0.8, 1.2) = %s, outside [48s, 72s)", got)
		}
		seen[got] = true
	}
	if len(seen) < 100 {
		t.Fatalf("jitterDuration produced only %d distinct values in 1000 draws", len(seen))
	}
	if got := jitterDuration(d, 0, 1); got < 0 || got >= d {
		t.Fatalf("initial delay %s outside [0, 1m)", got)
	}
}

// NewStagingCleanupJob never sizes a batch past one multi-object delete request.
func TestNewStagingCleanupJobCapsBatchAtOneRequest(t *testing.T) {
	if got := NewStagingCleanupJob(StagingCleanupConfig{BatchSize: 5000}).batchSize; got != 1000 {
		t.Fatalf("batch size %d, want 1000", got)
	}
	if got := NewStagingCleanupJob(StagingCleanupConfig{}).batchSize; got != stagingCleanupDefaultBatch {
		t.Fatalf("default batch size %d, want %d", got, stagingCleanupDefaultBatch)
	}
}
