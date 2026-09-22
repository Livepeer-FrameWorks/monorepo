package control

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// A serialization failure on the skip write replays the whole requeue
// transaction; the poison-skip counter is emitted once, after the commit, not
// once per attempt.
func TestRequeuePushTargetActivationsReplaysSerializationFailureAndCountsOnce(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_restream_reconcile_total"}, []string{"operation", "outcome"})
	prevMetrics := controlMetrics
	SetMetrics(&ControlMetrics{RestreamReconcile: counter})
	t.Cleanup(func() { SetMetrics(prevMetrics) })

	const (
		nodeID   = "node-1"
		fence    = int64(7)
		effectID = int64(42)
	)
	listColumns := []string{"id", "tenant_id", "stream_internal_name", "source_generation", "target_revision", "push_targets"}
	// An empty payload decodes to revision 0, which mismatches the durable revision and takes the poison-skip path.
	expectAttempt := func() *sqlmock.ExpectedExec {
		mock.ExpectBegin()
		mock.ExpectQuery(`-- name: ListActivePushTargetActivationsForNodeRequeue`).
			WithArgs(nodeID, fence).
			WillReturnRows(sqlmock.NewRows(listColumns).AddRow(effectID, "tenant-1", "stream-1", "00000000-0000-0000-0000-000000000001", int64(5), []byte{}))
		return mock.ExpectExec(`-- name: SkipReconnectPushTargetActivationByID`).
			WithArgs(fence, sqlmock.AnyArg(), effectID)
	}
	expectAttempt().WillReturnError(&pq.Error{Code: "40001", Message: "restart transaction"})
	mock.ExpectRollback()
	expectAttempt().WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	requeued, examined, err := requeueActivePushTargetActivationsForNodeBatch(context.Background(), nodeID, fence, "instance-1")
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if requeued != 0 || examined != 1 {
		t.Fatalf("requeued=%d examined=%d, want 0 and 1", requeued, examined)
	}
	if got := testutil.ToFloat64(counter.WithLabelValues("reconnect", "poison_skipped")); got != 1 {
		t.Fatalf("poison_skipped counter = %v, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
