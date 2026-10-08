package orchestrator

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// Production v0.3.11: Helmsman reported a successful self-update, the new
// Helmsman never reconnected, and the node went stale. Stale nodes are not
// eligible for the rollout, so nothing expired the update and nothing was
// recorded. The reconciler must record the failure with its reason.
func TestReconcileTargetFailsHelmsmanSelfUpdateWithoutReconnect(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	control.Init(logging.NewLogger(), nil, nil)
	mock := installMockDBOrch(t)
	t.Cleanup(func() {
		state.ResetDefaultManagerForTests()
		control.Init(logging.NewLogger(), nil, nil)
	})
	seedNativeNodeOrch(sm, "edge-1", "cluster-a")
	sm.MarkNodeDisconnected("edge-1")

	expired := time.Now().Add(-time.Minute).UTC()
	mock.ExpectQuery(`COALESCE\(target_release`).WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).
			AddRow("stable:v1.2.3", "warming", expired, time.Now().Add(-3*time.Minute), `{"helmsman":"v0.4.5"}`))
	mock.ExpectExec(`INSERT INTO foghorn\.node_update_state`).
		WithArgs("edge-1", "stable:v1.2.3", "failed", "helmsman did not reconnect after self-update", sqlmock.AnyArg(), nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := reconcileTarget(context.Background(), startQMFakeOrch(t, fenceRecoveryRelease(t)), &quartermasterpb.ClusterReleaseTarget{
		ClusterId: "cluster-a", Channel: "stable", TargetVersion: "v1.2.3", RolloutPlanJson: `{"batch_size":1}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("stale node's expired self-update was not recorded as failed: %v", err)
	}
}

// A disconnected node whose reconnect deadline has not passed yet keeps its
// phase: the new Helmsman may still be starting.
func TestReconcileTargetWaitsForHelmsmanReconnectBeforeDeadline(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	control.Init(logging.NewLogger(), nil, nil)
	mock := installMockDBOrch(t)
	t.Cleanup(func() {
		state.ResetDefaultManagerForTests()
		control.Init(logging.NewLogger(), nil, nil)
	})
	seedNativeNodeOrch(sm, "edge-1", "cluster-a")
	sm.MarkNodeDisconnected("edge-1")

	mock.ExpectQuery(`COALESCE\(target_release`).WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).
			AddRow("stable:v1.2.3", "warming", time.Now().Add(time.Minute), time.Now(), `{"helmsman":"v0.4.5"}`))

	if err := reconcileTarget(context.Background(), startQMFakeOrch(t, fenceRecoveryRelease(t)), &quartermasterpb.ClusterReleaseTarget{
		ClusterId: "cluster-a", Channel: "stable", TargetVersion: "v1.2.3", RolloutPlanJson: `{"batch_size":1}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
