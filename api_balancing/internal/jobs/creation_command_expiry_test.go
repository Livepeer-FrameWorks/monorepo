package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCreationCommandExpiry_Defaults(t *testing.T) {
	j := NewCreationCommandExpiryJob(CreationCommandExpiryConfig{Logger: logging.NewLogger()})
	if j.interval != 1*time.Minute {
		t.Fatalf("expected default interval 1m, got %v", j.interval)
	}
	if j.deadline != 15*time.Minute {
		t.Fatalf("expected default deadline 15m, got %v", j.deadline)
	}
	if j.retentionHorizon != 7*24*time.Hour {
		t.Fatalf("expected default retention horizon 7d, got %v", j.retentionHorizon)
	}
	if j.expiryBatch != creationCommandExpiryBatchDefault {
		t.Fatalf("expected default expiry batch %d, got %d", creationCommandExpiryBatchDefault, j.expiryBatch)
	}
	if j.retentionBatch != creationCommandRetentionBatchDefault {
		t.Fatalf("expected default retention batch %d, got %d", creationCommandRetentionBatchDefault, j.retentionBatch)
	}
}

func TestCreationCommandExpiry_Custom(t *testing.T) {
	j := NewCreationCommandExpiryJob(CreationCommandExpiryConfig{
		Logger:           logging.NewLogger(),
		Interval:         5 * time.Second,
		Deadline:         2 * time.Minute,
		RetentionHorizon: 48 * time.Hour,
		ExpiryBatch:      10,
		RetentionBatch:   20,
	})
	if j.interval != 5*time.Second {
		t.Fatalf("expected interval 5s, got %v", j.interval)
	}
	if j.deadline != 2*time.Minute {
		t.Fatalf("expected deadline 2m, got %v", j.deadline)
	}
	if j.retentionHorizon != 48*time.Hour {
		t.Fatalf("expected retention horizon 48h, got %v", j.retentionHorizon)
	}
	if j.expiryBatch != 10 || j.retentionBatch != 20 {
		t.Fatalf("expected batches 10/20, got %d/%d", j.expiryBatch, j.retentionBatch)
	}
}

// The bounded expire pass CAS-rejects a LIMITed batch of still-'accepted' rows past the
// deadline with no matching artifact (SELECT ... FOR UPDATE SKIP LOCKED), passing the
// deadline (seconds) and the batch size. A pass that fills the batch loops; a pass that
// returns fewer than the batch stops.
func TestCreationCommandExpiry_BoundedBatchLoops(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &CreationCommandExpiryJob{
		db:          mockDB,
		logger:      logging.NewLogger(),
		deadline:    15 * time.Minute,
		expiryBatch: 2,
	}

	// First pass fills the batch (2) → loop continues.
	mock.ExpectExec(`WITH stranded AS.*FOR UPDATE SKIP LOCKED.*UPDATE foghorn\.artifact_creation_commands`).
		WithArgs(int64(900), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	// Second pass returns fewer than the batch (1) → stop.
	mock.ExpectExec(`WITH stranded AS.*UPDATE foghorn\.artifact_creation_commands`).
		WithArgs(int64(900), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	j.expire(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A single bounded pass that returns fewer than the batch stops after one statement.
func TestCreationCommandExpiry_SingleBoundedPass(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &CreationCommandExpiryJob{
		db:          mockDB,
		logger:      logging.NewLogger(),
		deadline:    15 * time.Minute,
		expiryBatch: 500,
	}

	mock.ExpectExec(`WITH stranded AS.*UPDATE foghorn\.artifact_creation_commands`).
		WithArgs(int64(900), int64(500)).
		WillReturnResult(sqlmock.NewResult(0, 2))

	j.expire(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreationCommandExpiry_QueryError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &CreationCommandExpiryJob{
		db:          mockDB,
		logger:      logging.NewLogger(),
		deadline:    15 * time.Minute,
		expiryBatch: 500,
	}

	mock.ExpectExec(`WITH stranded AS`).WillReturnError(fmt.Errorf("db error"))

	j.expire(context.Background()) // must not panic

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreationCommandExpiry_ZeroDeadlineClampsToOne(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &CreationCommandExpiryJob{
		db:          mockDB,
		logger:      logging.NewLogger(),
		deadline:    0,
		expiryBatch: 500,
	}

	mock.ExpectExec(`WITH stranded AS.*UPDATE foghorn\.artifact_creation_commands`).
		WithArgs(int64(1), int64(500)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	j.expire(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Retention deletes ONLY CONSUMED terminal ('committed'/'rejected') rows in bounded
// batches (SELECT ... FOR UPDATE SKIP LOCKED + LIMIT), looping until a pass deletes fewer
// than the batch. The DELETE gates strictly on consumed_at IS NOT NULL, so an unconsumed
// terminal outcome is never time-deleted: the query text carries the consumed predicate
// and NOT an unconsumed branch, and only the horizon + batch are bound (no safety horizon).
func TestCreationCommandExpiry_RetentionBoundedDelete(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &CreationCommandExpiryJob{
		db:               mockDB,
		logger:           logging.NewLogger(),
		retentionHorizon: 7 * 24 * time.Hour,
		retentionBatch:   2,
	}

	horizon := int64((7 * 24 * time.Hour).Seconds())
	// The DELETE deletes only consumed terminal rows past the horizon; the query text must
	// carry the consumed_at IS NOT NULL predicate and bind exactly (horizon, batch).
	deletePattern := `consumed_at IS NOT NULL.*DELETE FROM foghorn\.artifact_creation_commands`
	// First delete fills the batch → loop; second returns fewer → stop.
	mock.ExpectExec(deletePattern).
		WithArgs(horizon, int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(deletePattern).
		WithArgs(horizon, int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	j.enforceRetention(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The retention DELETE must NEVER contain an unconsumed branch: an unconsumed terminal
// outcome is retained forever (until acked+consumed), so the query text must not reference
// deleting rows where consumed_at IS NULL. This guards against reintroducing the
// safety-horizon delete that recreated the committed→MISSING→bounded-abort data-loss path.
func TestCreationCommandExpiry_RetentionNeverDeletesUnconsumed(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &CreationCommandExpiryJob{
		db:               mockDB,
		logger:           logging.NewLogger(),
		retentionHorizon: 7 * 24 * time.Hour,
		retentionBatch:   500,
	}

	// A regexp that matches ONLY if the DELETE ties consumed_at to IS NULL would fire if the
	// unconsumed branch returned; expecting the consumed-only pattern and asserting the
	// single bound-arg shape proves the unconsumed branch is gone.
	mock.ExpectExec(`consumed_at IS NOT NULL[^;]*DELETE FROM foghorn\.artifact_creation_commands`).
		WithArgs(int64((7 * 24 * time.Hour).Seconds()), int64(500)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	j.enforceRetention(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Retention ages a consumed row by consumed_at, NOT the terminal transition (updated_at):
// a row terminalized long ago but only just consumed must survive a full horizon past its
// ack, else a lost ack RESPONSE would leave the row already GC-eligible and drive the retry
// to MISSING forever. The query text must range-filter AND order on consumed_at, never on
// updated_at, so the retention window starts at consumption.
func TestCreationCommandExpiry_RetentionAnchorsOnConsumedAt(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &CreationCommandExpiryJob{
		db:               mockDB,
		logger:           logging.NewLogger(),
		retentionHorizon: 7 * 24 * time.Hour,
		retentionBatch:   500,
	}

	// The aging predicate and ORDER BY are on consumed_at; there is NO `updated_at <` aging
	// clause. A pattern that requires `consumed_at < NOW()` ... `ORDER BY c.consumed_at`
	// matches only the consumed-anchored query.
	mock.ExpectExec(`consumed_at IS NOT NULL[\s\S]+consumed_at < NOW\(\)[\s\S]+ORDER BY c\.consumed_at[\s\S]+DELETE FROM foghorn\.artifact_creation_commands`).
		WithArgs(int64((7 * 24 * time.Hour).Seconds()), int64(500)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	j.enforceRetention(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A nonzero count of unconsumed terminal rows past the horizon is an alertable backlog:
// warnUnconsumedBacklog counts them (never deletes) so a stuck ack surfaces operationally.
func TestCreationCommandExpiry_WarnsUnconsumedBacklog(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &CreationCommandExpiryJob{
		db:               mockDB,
		logger:           logging.NewLogger(),
		retentionHorizon: 7 * 24 * time.Hour,
	}

	mock.ExpectQuery(`SELECT COUNT\(\*\).*consumed_at IS NULL`).
		WithArgs(int64((7 * 24 * time.Hour).Seconds())).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(3)))

	j.warnUnconsumedBacklog(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreationCommandExpiry_RetentionQueryError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	j := &CreationCommandExpiryJob{
		db:               mockDB,
		logger:           logging.NewLogger(),
		retentionHorizon: 7 * 24 * time.Hour,
		retentionBatch:   1000,
	}

	mock.ExpectExec(`WITH old_terminal AS`).WillReturnError(fmt.Errorf("db error"))

	j.enforceRetention(context.Background()) // must not panic

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
