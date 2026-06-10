package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	qmcli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
)

// ===========================================================================
// QM ClusterService fake — drives ReconcileReleaseTargets/reconcileTarget.
//
// Both RPCs the reconciler calls (ListClusterReleaseTargets, ListEdgeReleases)
// live on quartermaster's ClusterService. We stand up a localhost gRPC server
// hosting a settable double and dial it with a REAL qmclient.GRPCClient, which
// is what ReconcileReleaseTargets accepts. This is the Commodore-fake pattern
// applied to the Quartermaster concrete client.
// ===========================================================================

type clusterReleaseFakeOrch struct {
	quartermasterpb.UnimplementedClusterServiceServer

	listTargets  func(context.Context, *quartermasterpb.ListClusterReleaseTargetsRequest) (*quartermasterpb.ListClusterReleaseTargetsResponse, error)
	listReleases func(context.Context, *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error)
}

func (f *clusterReleaseFakeOrch) ListClusterReleaseTargets(ctx context.Context, req *quartermasterpb.ListClusterReleaseTargetsRequest) (*quartermasterpb.ListClusterReleaseTargetsResponse, error) {
	if f.listTargets != nil {
		return f.listTargets(ctx, req)
	}
	return &quartermasterpb.ListClusterReleaseTargetsResponse{}, nil
}

func (f *clusterReleaseFakeOrch) ListEdgeReleases(ctx context.Context, req *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
	if f.listReleases != nil {
		return f.listReleases(ctx, req)
	}
	return &quartermasterpb.ListEdgeReleasesResponse{}, nil
}

// startQMFakeOrch serves the fake on a localhost listener and returns a real
// qmclient.GRPCClient pointed at it. All resources are torn down on cleanup.
func startQMFakeOrch(t *testing.T, fake *clusterReleaseFakeOrch) *qmcli.GRPCClient {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	quartermasterpb.RegisterClusterServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := qmcli.NewGRPCClient(qmcli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("qm client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return client
}

// installMockDBOrch swaps control's package-level *sql.DB for a sqlmock and
// restores the prior DB on cleanup. Unique helper for this assignment.
func installMockDBOrch(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prev := control.GetDB()
	control.SetDB(mockDB)
	t.Cleanup(func() {
		control.SetDB(prev)
		mockDB.Close()
	})
	return mock
}

// seedNativeNodeOrch registers a healthy, native, normal-mode node in the
// default state manager so eligibleNodes/nodesInCluster will return it. The
// state manager is reset by the caller via state.ResetDefaultManagerForTests().
func seedNativeNodeOrch(sm *state.StreamStateManager, nodeID, clusterID string) {
	sm.TouchNode(nodeID, true)
	sm.SetNodeRuntimeInfo(nodeID, "native", "linux", "amd64")
	sm.SetNodeConnectionInfo(context.Background(), nodeID, "", "", clusterID, nil)
}

// ===========================================================================
// ReconcileReleaseTargets — top-level fan-out + error joining
// ===========================================================================

// A QM list error propagates out of ReconcileReleaseTargets unchanged. This is
// the fail-fast arm before any per-target work.
func TestReconcileReleaseTargets_ListErrorPropagatesOrch(t *testing.T) {
	qm := startQMFakeOrch(t, &clusterReleaseFakeOrch{
		listTargets: func(context.Context, *quartermasterpb.ListClusterReleaseTargetsRequest) (*quartermasterpb.ListClusterReleaseTargetsResponse, error) {
			return nil, context.DeadlineExceeded
		},
	})
	if err := ReconcileReleaseTargets(context.Background(), qm); err == nil {
		t.Fatal("ReconcileReleaseTargets returned nil for a QM list error")
	}
}

// A paused target is skipped without invoking ListEdgeReleases — the rollout
// must never advance a cluster an operator has explicitly paused. (Note: a
// repeated proto message field cannot carry a nil entry over the wire — gRPC
// materializes it as an empty message — so the `target == nil` guard in the
// loop is only reachable in-process and is not asserted here.)
func TestReconcileReleaseTargets_SkipsPausedTargetOrch(t *testing.T) {
	qm := startQMFakeOrch(t, &clusterReleaseFakeOrch{
		listTargets: func(context.Context, *quartermasterpb.ListClusterReleaseTargetsRequest) (*quartermasterpb.ListClusterReleaseTargetsResponse, error) {
			return &quartermasterpb.ListClusterReleaseTargetsResponse{
				Targets: []*quartermasterpb.ClusterReleaseTarget{
					{ClusterId: "cluster-paused", Paused: true, TargetVersion: "v1"},
				},
			}, nil
		},
		listReleases: func(context.Context, *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
			t.Error("ListEdgeReleases called for a paused target")
			return &quartermasterpb.ListEdgeReleasesResponse{}, nil
		},
	})
	if err := ReconcileReleaseTargets(context.Background(), qm); err != nil {
		t.Fatalf("ReconcileReleaseTargets: %v", err)
	}
}

// reconcileTarget errors are joined and surfaced wrapped with the cluster id.
// We force an error by returning an invalid rollout plan for a live target.
func TestReconcileReleaseTargets_JoinsTargetErrorsOrch(t *testing.T) {
	qm := startQMFakeOrch(t, &clusterReleaseFakeOrch{
		listTargets: func(context.Context, *quartermasterpb.ListClusterReleaseTargetsRequest) (*quartermasterpb.ListClusterReleaseTargetsResponse, error) {
			return &quartermasterpb.ListClusterReleaseTargetsResponse{
				Targets: []*quartermasterpb.ClusterReleaseTarget{
					{ClusterId: "cluster-bad", TargetVersion: "v1", RolloutPlanJson: `{"capacity_floor":2}`},
				},
			}, nil
		},
		listReleases: func(context.Context, *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
			return &quartermasterpb.ListEdgeReleasesResponse{
				Releases: []*quartermasterpb.EdgeRelease{
					{Channel: "stable", Version: "v1", ComponentsJson: "{}"},
				},
			}, nil
		},
	})
	err := ReconcileReleaseTargets(context.Background(), qm)
	if err == nil {
		t.Fatal("ReconcileReleaseTargets swallowed a per-target error")
	}
	if !strings.Contains(err.Error(), "cluster-bad") {
		t.Fatalf("joined error %q does not name the failing cluster", err.Error())
	}
}

// ===========================================================================
// reconcileTarget — early-exit decision arms
// ===========================================================================

// No releases for the target channel/version is a no-op (returns nil, no
// snapshot/budget work). Locks the "nothing published yet" arm.
func TestReconcileTarget_NoReleasesIsNoopOrch(t *testing.T) {
	qm := startQMFakeOrch(t, &clusterReleaseFakeOrch{
		listReleases: func(context.Context, *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
			return &quartermasterpb.ListEdgeReleasesResponse{}, nil
		},
	})
	err := reconcileTarget(context.Background(), qm, &quartermasterpb.ClusterReleaseTarget{
		ClusterId:     "cluster-a",
		Channel:       "stable",
		TargetVersion: "v1.2.3",
	})
	if err != nil {
		t.Fatalf("reconcileTarget with no releases = %v, want nil", err)
	}
}

// An invalid rollout_plan_json fails parsing inside reconcileTarget and the
// error propagates. Guards the plan-validation gate on the live path.
func TestReconcileTarget_InvalidPlanPropagatesOrch(t *testing.T) {
	qm := startQMFakeOrch(t, &clusterReleaseFakeOrch{
		listReleases: func(context.Context, *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
			return &quartermasterpb.ListEdgeReleasesResponse{
				Releases: []*quartermasterpb.EdgeRelease{
					{Channel: "stable", Version: "v1.2.3", ComponentsJson: "{}"},
				},
			}, nil
		},
	})
	err := reconcileTarget(context.Background(), qm, &quartermasterpb.ClusterReleaseTarget{
		ClusterId:       "cluster-a",
		Channel:         "stable",
		TargetVersion:   "v1.2.3",
		RolloutPlanJson: `{"capacity_floor_percent":80}`, // unsupported -> parse error
	})
	if err == nil {
		t.Fatal("reconcileTarget accepted an unsupported rollout plan")
	}
}

// Malformed components_json fails parseReleaseComponents and propagates. This
// is reached only after rolloutFailed returns false (the default plan has no
// abort threshold), so it also exercises that gate's false arm.
func TestReconcileTarget_MalformedComponentsPropagatesOrch(t *testing.T) {
	prevDB := control.GetDB()
	control.SetDB(nil) // rolloutFailed/budget DB helpers tolerate nil DB
	t.Cleanup(func() { control.SetDB(prevDB) })

	qm := startQMFakeOrch(t, &clusterReleaseFakeOrch{
		listReleases: func(context.Context, *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
			return &quartermasterpb.ListEdgeReleasesResponse{
				Releases: []*quartermasterpb.EdgeRelease{
					{Channel: "stable", Version: "v1.2.3", ComponentsJson: "{not json"},
				},
			}, nil
		},
	})
	err := reconcileTarget(context.Background(), qm, &quartermasterpb.ClusterReleaseTarget{
		ClusterId:     "cluster-a",
		Channel:       "stable",
		TargetVersion: "v1.2.3",
	})
	if err == nil {
		t.Fatal("reconcileTarget accepted malformed components_json")
	}
}

// With valid components but ZERO eligible nodes in the cluster, reconcileTarget
// returns nil before touching the DB. Proves the empty-cluster short-circuit
// (len(nodes)==0) gates the rollout, never the budget loop.
func TestReconcileTarget_NoEligibleNodesIsNoopOrch(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	prevDB := control.GetDB()
	control.SetDB(nil)
	t.Cleanup(func() {
		control.SetDB(prevDB)
		state.ResetDefaultManagerForTests()
	})
	// A docker node in the target cluster is present but NOT eligible.
	sm.TouchNode("docker-node", true)
	sm.SetNodeRuntimeInfo("docker-node", "docker", "linux", "amd64")
	sm.SetNodeConnectionInfo(context.Background(), "docker-node", "", "", "cluster-a", nil)

	qm := startQMFakeOrch(t, &clusterReleaseFakeOrch{
		listReleases: func(context.Context, *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
			return &quartermasterpb.ListEdgeReleasesResponse{
				Releases: []*quartermasterpb.EdgeRelease{
					{Channel: "stable", Version: "v1.2.3", ComponentsJson: `{"helmsman":{"version":"v0.4.5","artifact_url":"https://e/h.tgz","checksum":"sha256:bb"}}`},
				},
			}, nil
		},
	})
	err := reconcileTarget(context.Background(), qm, &quartermasterpb.ClusterReleaseTarget{
		ClusterId:     "cluster-a",
		Channel:       "stable",
		TargetVersion: "v1.2.3",
	})
	if err != nil {
		t.Fatalf("reconcileTarget with no eligible nodes = %v, want nil", err)
	}
}

// Full happy path: one eligible native node, idle progress, budget=1. The
// reconciler loads progress (idle), reconcileWarmups is a no-op, budget allows
// one update, currentNodeComponents reports the node lacks the desired version,
// and applyReleaseUpdate routes the non-Mist (helmsman) component through the
// DIRECT path — which persists `updating` then attempts a control-stream push.
// With no control stream connected that push fails with ErrNotConnected, which
// reconcileTarget surfaces. This locks the cordon->...->apply SEQUENCING for a
// fresh node under available budget.
func TestReconcileTarget_FreshNodeDrivesDirectUpdateOrch(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	control.Init(logging.NewLogger(), nil, nil) // no live control stream
	mock := installMockDBOrch(t)
	t.Cleanup(func() {
		state.ResetDefaultManagerForTests()
		control.Init(logging.NewLogger(), nil, nil)
	})
	seedNativeNodeOrch(sm, "edge-1", "cluster-a")

	// Call order inside reconcileTarget for a single idle node under default
	// (non-canary) plan: reconcileWarmups->loadProgress, rolloutBudget->
	// activeUpdateCount(phase), budget-loop->loadProgress, currentNodeComponents,
	// applyReleaseUpdate->ApplyDirectUpdate->loadProgress, then two writes.
	// Distinct SQL regexes keep the phase (1-col) and loadProgress (5-col) rows
	// from being mismatched by ordered sqlmock.
	loadProgressQ := `COALESCE\(target_release`
	phaseQ := `SELECT phase`

	// reconcileWarmups loadProgress -> idle (not warming).
	mock.ExpectQuery(loadProgressQ).
		WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).
			AddRow("", "idle", nil, nil, "{}"))
	// rolloutBudget: activeUpdateCount queries node_update_state.phase -> idle.
	mock.ExpectQuery(phaseQ).
		WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("idle"))
	// Budget loop: loadProgress -> idle (same node).
	mock.ExpectQuery(loadProgressQ).
		WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).
			AddRow("", "idle", nil, nil, "{}"))
	// currentNodeComponents: node has no helmsman version yet.
	mock.ExpectQuery(`FROM foghorn\.node_components`).
		WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows([]string{"component", "current_version"}))
	// ApplyDirectUpdate loadProgress -> idle (fresh, so it persists `updating`).
	mock.ExpectQuery(loadProgressQ).
		WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).
			AddRow("", "idle", nil, nil, "{}"))
	// persistPhase("updating", ...) then persistFailure("failed", ...) once the
	// control push fails. Both write node_update_state.
	mock.ExpectExec(`INSERT INTO foghorn\.node_update_state`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.node_update_state`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	qm := startQMFakeOrch(t, &clusterReleaseFakeOrch{
		listReleases: func(context.Context, *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
			return &quartermasterpb.ListEdgeReleasesResponse{
				Releases: []*quartermasterpb.EdgeRelease{
					{Channel: "stable", Version: "v1.2.3", ComponentsJson: `{"helmsman":{"version":"v0.4.5","artifacts":{"linux/amd64":{"artifact_url":"https://e/h.tgz","checksum":"sha256:bb"}}}}`},
				},
			}, nil
		},
	})
	err := reconcileTarget(context.Background(), qm, &quartermasterpb.ClusterReleaseTarget{
		ClusterId:       "cluster-a",
		Channel:         "stable",
		TargetVersion:   "v1.2.3",
		RolloutPlanJson: `{"batch_size":1}`,
	})
	if err == nil {
		t.Fatal("expected control-stream ErrNotConnected to surface from the apply path")
	}
	if !errors.Is(err, control.ErrNotConnected) {
		t.Fatalf("reconcileTarget error = %v, want control.ErrNotConnected", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// ===========================================================================
// DB-backed decision helpers — exact tenant/phase predicate semantics
// ===========================================================================

// updatingNode returns true only for a non-terminal phase row, false for
// idle/failed/empty, and false (no error) when the row is absent.
func TestUpdatingNode_PhasePredicateOrch(t *testing.T) {
	mock := installMockDBOrch(t)
	q := `FROM foghorn\.node_update_state`

	mock.ExpectQuery(q).WithArgs("n-updating").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("updating"))
	mock.ExpectQuery(q).WithArgs("n-idle").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("idle"))
	mock.ExpectQuery(q).WithArgs("n-failed").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("failed"))
	mock.ExpectQuery(q).WithArgs("n-missing").
		WillReturnError(sql.ErrNoRows)

	if !updatingNode(context.Background(), "n-updating") {
		t.Error("updatingNode(updating) = false, want true")
	}
	if updatingNode(context.Background(), "n-idle") {
		t.Error("updatingNode(idle) = true, want false")
	}
	if updatingNode(context.Background(), "n-failed") {
		t.Error("updatingNode(failed) = true, want false")
	}
	if updatingNode(context.Background(), "n-missing") {
		t.Error("updatingNode(no-row) = true, want false")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// updatingNode and activeUpdateCount both return their safe zero (false / 0)
// when the DB is unavailable — the rollout must not crash without a DB.
func TestActiveUpdateCount_NilDBIsZeroOrch(t *testing.T) {
	prevDB := control.GetDB()
	control.SetDB(nil)
	t.Cleanup(func() { control.SetDB(prevDB) })

	if updatingNode(context.Background(), "any") {
		t.Error("updatingNode with nil DB = true, want false")
	}
	nodes := []*state.NodeState{{NodeID: "a"}, {NodeID: "b"}}
	if got := activeUpdateCount(context.Background(), nodes); got != 0 {
		t.Errorf("activeUpdateCount with nil DB = %d, want 0", got)
	}
}

// activeUpdateCount counts only nodes whose phase is non-terminal. Two nodes,
// one updating + one idle, must yield exactly 1.
func TestActiveUpdateCount_CountsNonTerminalOrch(t *testing.T) {
	mock := installMockDBOrch(t)
	q := `FROM foghorn\.node_update_state`
	mock.ExpectQuery(q).WithArgs("busy").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("draining"))
	mock.ExpectQuery(q).WithArgs("free").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("idle"))

	nodes := []*state.NodeState{{NodeID: "busy"}, {NodeID: "free"}}
	if got := activeUpdateCount(context.Background(), nodes); got != 1 {
		t.Fatalf("activeUpdateCount = %d, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// failedTargetCount filters on BOTH node_id AND target_release (tenant/target
// isolation): only the matching node that is in phase `failed` counts. A node
// failed against a DIFFERENT target_release does not match the WHERE clause.
func TestFailedTargetCount_FiltersByTargetReleaseOrch(t *testing.T) {
	mock := installMockDBOrch(t)
	q := `WHERE node_id = \$1 AND target_release = \$2`

	mock.ExpectQuery(q).WithArgs("n1", "stable:v2").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("failed"))
	mock.ExpectQuery(q).WithArgs("n2", "stable:v2").
		WillReturnError(sql.ErrNoRows) // no row for THIS target -> not failed

	nodes := []*state.NodeState{{NodeID: "n1"}, {NodeID: "n2"}}
	if got := failedTargetCount(context.Background(), nodes, "stable:v2"); got != 1 {
		t.Fatalf("failedTargetCount = %d, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// completedTargetCount counts only phase==idle rows for the matching target,
// also asserting the (node_id, target_release) predicate is bound.
func TestCompletedTargetCount_CountsIdleForTargetOrch(t *testing.T) {
	mock := installMockDBOrch(t)
	q := `WHERE node_id = \$1 AND target_release = \$2`

	mock.ExpectQuery(q).WithArgs("done", "stable:v2").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("idle"))
	mock.ExpectQuery(q).WithArgs("mid", "stable:v2").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("draining"))

	nodes := []*state.NodeState{{NodeID: "done"}, {NodeID: "mid"}}
	if got := completedTargetCount(context.Background(), nodes, "stable:v2"); got != 1 {
		t.Fatalf("completedTargetCount = %d, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// failedTargetCount and completedTargetCount both return 0 with a nil DB rather
// than panicking — the abort/canary math must degrade safely.
func TestTargetCounts_NilDBAreZeroOrch(t *testing.T) {
	prevDB := control.GetDB()
	control.SetDB(nil)
	t.Cleanup(func() { control.SetDB(prevDB) })

	nodes := []*state.NodeState{{NodeID: "a"}}
	if got := failedTargetCount(context.Background(), nodes, "rel"); got != 0 {
		t.Errorf("failedTargetCount nil DB = %d, want 0", got)
	}
	if got := completedTargetCount(context.Background(), nodes, "rel"); got != 0 {
		t.Errorf("completedTargetCount nil DB = %d, want 0", got)
	}
}

// rolloutFailed returns false immediately (no DB read) when the plan has no
// abort threshold (ErrorAbort=false AND MaxFailed<=0). Locks the "abort
// disabled" gate that precedes any failedTargetCount query.
func TestRolloutFailed_DisabledWhenNoThresholdOrch(t *testing.T) {
	prevDB := control.GetDB()
	control.SetDB(nil)
	t.Cleanup(func() { control.SetDB(prevDB) })

	if rolloutFailed(context.Background(), "cluster-a", "stable:v2", rolloutPlan{}) {
		t.Fatal("rolloutFailed = true with no abort threshold configured")
	}
}

// rolloutFailed trips when failed nodes in the cluster reach MaxFailed. We seed
// one native node into the cluster snapshot and back its failed phase via
// sqlmock; with MaxFailed=1 the rollout must report failed (true).
func TestRolloutFailed_TripsAtMaxFailedOrch(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	mock := installMockDBOrch(t)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	seedNativeNodeOrch(sm, "edge-x", "cluster-a")

	mock.ExpectQuery(`WHERE node_id = \$1 AND target_release = \$2`).
		WithArgs("edge-x", "stable:v2").
		WillReturnRows(sqlmock.NewRows([]string{"phase"}).AddRow("failed"))

	if !rolloutFailed(context.Background(), "cluster-a", "stable:v2", rolloutPlan{MaxFailed: 1}) {
		t.Fatal("rolloutFailed = false; want true when failed count meets MaxFailed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// currentNodeComponents maps component->current_version filtered by node_id,
// and returns an error (not a partial map) when the DB query itself fails.
func TestCurrentNodeComponents_MapsRowsAndErrorsOrch(t *testing.T) {
	mock := installMockDBOrch(t)

	mock.ExpectQuery(`FROM foghorn\.node_components`).
		WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows([]string{"component", "current_version"}).
			AddRow("mist", "v1.2.3").
			AddRow("helmsman", "v0.4.5"))

	got, err := currentNodeComponents(context.Background(), "edge-1")
	if err != nil {
		t.Fatalf("currentNodeComponents: %v", err)
	}
	if got["mist"] != "v1.2.3" || got["helmsman"] != "v0.4.5" {
		t.Fatalf("component map = %v, want mist/helmsman versions", got)
	}

	mock.ExpectQuery(`FROM foghorn\.node_components`).
		WithArgs("edge-2").
		WillReturnError(context.DeadlineExceeded)
	if _, err := currentNodeComponents(context.Background(), "edge-2"); err == nil {
		t.Fatal("currentNodeComponents swallowed a query error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// currentNodeComponents returns an error (component-version DB unavailable) when
// control has no DB, so the reconciler aborts rather than treating a node as
// already up to date.
func TestCurrentNodeComponents_NilDBErrorsOrch(t *testing.T) {
	prevDB := control.GetDB()
	control.SetDB(nil)
	t.Cleanup(func() { control.SetDB(prevDB) })

	if _, err := currentNodeComponents(context.Background(), "edge-1"); err == nil {
		t.Fatal("currentNodeComponents with nil DB returned nil error")
	}
}
