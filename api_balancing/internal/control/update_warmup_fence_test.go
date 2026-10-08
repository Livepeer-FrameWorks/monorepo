package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type recordedModes struct {
	mu    sync.Mutex
	modes []state.NodeOperationalMode
}

func (r *recordedModes) all() []state.NodeOperationalMode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]state.NodeOperationalMode(nil), r.modes...)
}

func installUpdateWarmupDoubles(t *testing.T) (sqlmock.Sqlmock, *recordedModes) {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := GetDB()
	SetDB(mockDB)
	rec := &recordedModes{}
	prevApply := applyUpdateNodeMode
	applyUpdateNodeMode = func(_ context.Context, _ string, mode state.NodeOperationalMode) error {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.modes = append(rec.modes, mode)
		return nil
	}
	prevTimeout, prevTick := updateWarmupTimeout, updateWarmupTick
	t.Cleanup(func() {
		SetDB(prevDB)
		_ = mockDB.Close()
		applyUpdateNodeMode = prevApply
		updateWarmupTimeout, updateWarmupTick = prevTimeout, prevTick
	})
	return mock, rec
}

func updateProgressRow(target, phase string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"target_release", "phase", "deadline", "updated_at", "expected_components"}).
		AddRow(target, phase, nil, time.Now(), "{}")
}

// Staging received two apply results for one update. The watcher started by
// the first result timed out and fenced the node while the second result's
// watcher completed, leaving the node in maintenance forever. Only the newest
// watcher may act.
func TestUpdateWarmupSupersededAttemptNeverFences(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	t.Cleanup(sm.Shutdown)
	mock, rec := installUpdateWarmupDoubles(t)
	updateWarmupTimeout, updateWarmupTick = 0, time.Millisecond

	sm.SetNodeInfo("edge-1", "https://edge.example", true, nil, nil, "", "", nil)
	sm.TouchNode("edge-1", false) // never warm, so a current watcher would time out
	sm.TouchNode("edge-2", true)  // another routable node, so fencing edge-1 keeps the cluster served

	first := updateWarmups.begin("edge-1")
	second := updateWarmups.begin("edge-1")

	completeUpdateWarmup("edge-1", "rc:v2", map[string]string{"mist": "v2"}, time.Now(), first, nil)
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("superseded watcher changed the node mode: %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery("GetNodeUpdateProgress").WillReturnRows(updateProgressRow("rc:v2", "warming"))
	mock.ExpectQuery("GetNodeUpdateProgress").WillReturnRows(updateProgressRow("rc:v2", "warming"))
	mock.ExpectExec("UpsertNodeUpdateProgress").WillReturnResult(sqlmock.NewResult(0, 1))
	completeUpdateWarmup("edge-1", "rc:v2", map[string]string{"mist": "v2"}, time.Now(), second, nil)
	if got := rec.all(); len(got) != 1 || got[0] != state.NodeModeMaintenance {
		t.Fatalf("current watcher timing out must fence the node, modes=%v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A node that warms up on the target release is returned to normal even when
// an earlier attempt already fenced it and recorded the update as failed; an
// operator's maintenance mode is never lifted by the update flow.
func TestCompleteUpdateWarmupLiftsOnlyOrchestratorFence(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(edge.Close)

	for _, tc := range []struct {
		setBy    string
		wantLift bool
	}{
		{UpdateOrchestratorModeSetter, true},
		{"operator", false},
	} {
		t.Run(tc.setBy, func(t *testing.T) {
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(func() { state.ResetDefaultManagerForTests() })
			t.Cleanup(sm.Shutdown)
			mock, rec := installUpdateWarmupDoubles(t)

			notBefore := time.Now().Add(-time.Minute)
			sm.SetNodeInfo("edge-1", edge.URL, true, nil, nil, "", "", nil)
			sm.TouchNode("edge-1", true)
			if err := sm.SetNodeOperationalMode(context.Background(), "edge-1", state.NodeModeMaintenance, tc.setBy); err != nil {
				t.Fatal(err)
			}

			mock.ExpectQuery("GetNodeUpdateProgress").WillReturnRows(updateProgressRow("rc:v2", "failed"))
			mock.ExpectQuery("GetNodeComponentVersion").WillReturnRows(sqlmock.NewRows([]string{"current_version"}).AddRow("v2"))
			mock.ExpectExec("UpsertNodeUpdateProgress").WillReturnResult(sqlmock.NewResult(0, 1))

			ready, reason, err := CompleteUpdateWarmupIfReady(context.Background(), "edge-1", "rc:v2", map[string]string{"mist": "v2"}, notBefore, nil)
			if err != nil || !ready {
				t.Fatalf("warmup not completed: ready=%v reason=%q err=%v", ready, reason, err)
			}
			got := rec.all()
			lifted := len(got) == 1 && got[0] == state.NodeModeNormal
			if lifted != tc.wantLift {
				t.Fatalf("setBy=%s: modes applied=%v, want lift=%v", tc.setBy, got, tc.wantLift)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Production v0.3.11: a failed Mist warmup fenced a cluster's only routable
// node into maintenance, taking the whole cluster offline. The failure is
// recorded, but the last routable node returns to normal mode.
func TestUpdateWarmupFailureKeepsClusterLastRoutableNodeNormal(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	t.Cleanup(sm.Shutdown)
	mock, rec := installUpdateWarmupDoubles(t)
	updateWarmupTimeout, updateWarmupTick = 0, time.Millisecond

	sm.SetNodeInfo("edge-1", "https://edge.example", true, nil, nil, "", "", nil)
	sm.TouchNode("edge-1", false)
	if err := sm.SetNodeOperationalMode(context.Background(), "edge-1", state.NodeModeDraining, UpdateOrchestratorModeSetter); err != nil {
		t.Fatal(err)
	}

	attempt := updateWarmups.begin("edge-1")
	mock.ExpectQuery("GetNodeUpdateProgress").WillReturnRows(updateProgressRow("rc:v2", "warming_restore"))
	mock.ExpectQuery("GetNodeUpdateProgress").WillReturnRows(updateProgressRow("rc:v2", "warming_restore"))
	mock.ExpectExec("UpsertNodeUpdateProgress").
		WithArgs("edge-1", "rc:v2", "failed", "warmup probe timed out", sqlmock.AnyArg(), nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	completeUpdateWarmup("edge-1", "rc:v2", map[string]string{"mist": "v2"}, time.Now(), attempt, nil)

	got := rec.all()
	for _, mode := range got {
		if mode == state.NodeModeMaintenance {
			t.Fatalf("the cluster's only routable node was fenced into maintenance: modes=%v", got)
		}
	}
	if len(got) != 1 || got[0] != state.NodeModeNormal {
		t.Fatalf("the cordoned last routable node did not return to normal: modes=%v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Helmsman reports a successful self-update before it execs the new binary.
// Recording that result as idle declared the node updated even when the new
// Helmsman never came back; it must wait for the reconnect instead.
func TestHelmsmanSelfUpdateResultWaitsForReconnect(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	t.Cleanup(sm.Shutdown)
	mock, rec := installUpdateWarmupDoubles(t)

	mock.ExpectQuery("GetNodeUpdateProgress").WillReturnRows(updateProgressRow("rc:v2", "updating"))
	mock.ExpectExec("UpsertNodeUpdateProgress").
		WithArgs("edge-1", "rc:v2", "warming", "", sqlmock.AnyArg(), `{"helmsman":"v2"}`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	processUpdateApplyResult(&ipcpb.UpdateApplyResult{
		NodeId:        "edge-1",
		TargetRelease: "rc:v2",
		Components:    []*ipcpb.ComponentApplyResult{{Component: "helmsman", Version: "v2", Success: true}},
	}, "edge-1", nil)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("helmsman self-update result was not held for reconnect: %v", err)
	}
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("helmsman self-update result changed the node mode: %v", got)
	}
}
