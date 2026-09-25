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

func fenceRecoveryRelease(t *testing.T) *clusterReleaseFakeOrch {
	t.Helper()
	return &clusterReleaseFakeOrch{
		listReleases: func(context.Context, *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
			return &quartermasterpb.ListEdgeReleasesResponse{
				Releases: []*quartermasterpb.EdgeRelease{
					{Channel: "stable", Version: "v1.2.3", ComponentsJson: `{"helmsman":{"version":"v0.4.5","artifacts":{"linux/amd64":{"artifact_url":"https://e/h.tgz","checksum":"sha256:bb"}}}}`},
				},
			}, nil
		},
	}
}

// A failed warmup fences the node into maintenance. The rollout excluded such
// nodes (it only considered normal-mode nodes), so a node that later ran the
// target release stayed isolated and skipped every later release. The
// reconciler must lift its own fence once the node reports the target.
func TestReconcileTargetLiftsOrchestratorFenceOnceNodeRunsTarget(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	control.Init(logging.NewLogger(), nil, nil)
	mock := installMockDBOrch(t)
	t.Cleanup(func() {
		state.ResetDefaultManagerForTests()
		control.Init(logging.NewLogger(), nil, nil)
	})
	seedNativeNodeOrch(sm, "edge-1", "cluster-a")
	if err := sm.SetNodeOperationalMode(context.Background(), "edge-1", state.NodeModeMaintenance, control.UpdateOrchestratorModeSetter); err != nil {
		t.Fatal(err)
	}

	loadProgressQ := `COALESCE\(target_release`
	mock.ExpectQuery(loadProgressQ).WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).AddRow("stable:v1.2.2", "failed", nil, time.Now(), "{}"))
	mock.ExpectQuery(`SELECT phase`).WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("failed"))
	mock.ExpectQuery(loadProgressQ).WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).AddRow("stable:v1.2.2", "failed", nil, time.Now(), "{}"))
	mock.ExpectQuery(`FROM foghorn\.node_components`).WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows([]string{"component", "current_version"}).AddRow("helmsman", "v0.4.5"))

	// The mode push to Helmsman fails here (no control stream); the fence lift
	// itself is recorded before the push.
	_ = reconcileTarget(context.Background(), startQMFakeOrch(t, fenceRecoveryRelease(t)), &quartermasterpb.ClusterReleaseTarget{
		ClusterId: "cluster-a", Channel: "stable", TargetVersion: "v1.2.3", RolloutPlanJson: `{"batch_size":1}`,
	})
	if node := sm.GetNodeState("edge-1"); node.OperationalMode != state.NodeModeNormal {
		t.Fatalf("fenced node running the target stayed in %q", node.OperationalMode)
	}
}

func TestReconcileTargetKeepsOperatorMaintenance(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	control.Init(logging.NewLogger(), nil, nil)
	mock := installMockDBOrch(t)
	t.Cleanup(func() {
		state.ResetDefaultManagerForTests()
		control.Init(logging.NewLogger(), nil, nil)
	})
	seedNativeNodeOrch(sm, "edge-1", "cluster-a")
	if err := sm.SetNodeOperationalMode(context.Background(), "edge-1", state.NodeModeMaintenance, "operator"); err != nil {
		t.Fatal(err)
	}

	if err := reconcileTarget(context.Background(), startQMFakeOrch(t, fenceRecoveryRelease(t)), &quartermasterpb.ClusterReleaseTarget{
		ClusterId: "cluster-a", Channel: "stable", TargetVersion: "v1.2.3", RolloutPlanJson: `{"batch_size":1}`,
	}); err != nil {
		t.Fatal(err)
	}
	if node := sm.GetNodeState("edge-1"); node.OperationalMode != state.NodeModeMaintenance {
		t.Fatalf("operator maintenance was changed to %q", node.OperationalMode)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
