package orchestrator

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// installMockDB swaps control's package-level *sql.DB for a sqlmock and
// restores the original on cleanup.
func installMockDB(t *testing.T) sqlmock.Sqlmock {
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

func sampleComponents() []*ipcpb.DesiredComponent {
	return []*ipcpb.DesiredComponent{
		{
			Component:   "mist",
			Version:     "v1.2.3",
			ArtifactUrl: "https://example.test/mist.tgz",
			Checksum:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
}

func loadProgressColumns() []string {
	return []string{"target_release", "phase", "deadline", "updated_at", "expected_components"}
}

// Regression: ApplyMistUpdate must preserve the `updating_restore` phase when
// called for a node that's already in flight. The reconciler routes
// `updating_restore` here (not to ApplyDirectUpdate) because ApplyDirectUpdate
// only handles `updating`/`warming` and would otherwise persist plain
// `updating`, silently downgrading the restore phase.
func TestApplyMistUpdatePreservesUpdatingRestoreOnReDrive(t *testing.T) {
	mock := installMockDB(t)
	deadline := time.Now().Add(10 * time.Minute)

	mock.ExpectQuery(`FROM foghorn\.node_update_state`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).
			AddRow("stable:v1.2.3", "updating_restore", deadline, time.Now(), "{}"))

	err := ApplyMistUpdate(context.Background(), MistUpdateRequest{
		NodeID:        "node-1",
		ClusterID:     "cluster-a",
		TargetRelease: "stable:v1.2.3",
		Components:    sampleComponents(),
	})
	if err != nil {
		t.Fatalf("ApplyMistUpdate: %v", err)
	}
	// No persistPhase write expected for the future-deadline branch.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ApplyMistUpdate transitions `updating_restore` to `failed` when the deadline
// has expired — proving the re-drive correctly frees stuck rollout budget.
func TestApplyMistUpdateExpiresUpdatingRestoreOnDeadline(t *testing.T) {
	mock := installMockDB(t)
	expired := time.Now().Add(-1 * time.Minute)

	mock.ExpectQuery(`FROM foghorn\.node_update_state`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).
			AddRow("stable:v1.2.3", "updating_restore", expired, time.Now(), "{}"))
	mock.ExpectExec(`INSERT INTO foghorn\.node_update_state`).
		WithArgs("node-1", "stable:v1.2.3", "failed", "update apply result deadline reached", expired, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := ApplyMistUpdate(context.Background(), MistUpdateRequest{
		NodeID:        "node-1",
		ClusterID:     "cluster-a",
		TargetRelease: "stable:v1.2.3",
		Components:    sampleComponents(),
	})
	if err != nil {
		t.Fatalf("ApplyMistUpdate: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// When a node is wedged in a non-terminal phase for an OLD target (operator
// changed cluster target while the prior rollout was still in flight) and its
// deadline has expired, ApplyMistUpdate is no longer the right path — the
// reconciler must directly mark it failed against its original target so it
// stops consuming rollout budget. This sanity-checks the deadlineExpired
// helper that backs that path.
func TestDeadlineExpiredHandlesAbandonedOldTarget(t *testing.T) {
	t.Parallel()
	if !deadlineExpired(time.Now().Add(-1 * time.Minute)) {
		t.Fatal("deadlineExpired returned false for a past deadline")
	}
	if deadlineExpired(time.Time{}) {
		t.Fatal("deadlineExpired returned true for zero deadline (should be treated as not expired)")
	}
	if deadlineExpired(time.Now().Add(10 * time.Minute)) {
		t.Fatal("deadlineExpired returned true for a future deadline")
	}
}

// buildComponentsForNode without a `current` map (re-drive path) emits all
// release components with full artifact metadata — not expected-version-only
// stubs that would lose ArtifactUrl/Checksum and break `drained`'s
// SendDesiredStateUpdate payload.
func TestBuildComponentsForNodeRedriveIncludesAllArtifacts(t *testing.T) {
	t.Parallel()

	components := map[string]releaseComponent{
		"mist": {
			Version: "v1.2.3",
			Artifacts: map[string]releaseArtifact{
				"linux/amd64": {ArtifactURL: "https://example.test/mist.tgz", Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			},
		},
		"helmsman": {
			Version: "v0.4.5",
			Artifacts: map[string]releaseArtifact{
				"linux/amd64": {ArtifactURL: "https://example.test/helmsman.tgz", Checksum: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			},
		},
		"config_schema": {Version: "4"},
	}
	node := &state.NodeState{NodeID: "node-1", OS: "linux", Arch: "amd64"}

	got, ok, err := buildComponentsForNode(context.Background(), components, nil, node, "stable:v1.2.3")
	if err != nil {
		t.Fatalf("buildComponentsForNode: %v", err)
	}
	if !ok {
		t.Fatal("expected non-empty components for re-drive")
	}
	if len(got) != 2 {
		t.Fatalf("got %d components, want 2 (config_schema must be excluded)", len(got))
	}
	for _, c := range got {
		if c.GetArtifactUrl() == "" || c.GetChecksum() == "" {
			t.Fatalf("component %s missing artifact metadata: %+v", c.GetComponent(), c)
		}
	}
}

// With a `current` map matching desired versions (fresh path, post-upgrade),
// no components are emitted — preserves the existing no-op behavior.
func TestBuildComponentsForNodeFreshPathSkipsCurrent(t *testing.T) {
	t.Parallel()

	components := map[string]releaseComponent{
		"mist": {
			Version: "v1.2.3",
			Artifacts: map[string]releaseArtifact{
				"linux/amd64": {ArtifactURL: "https://example.test/mist.tgz", Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			},
		},
	}
	node := &state.NodeState{NodeID: "node-1", OS: "linux", Arch: "amd64"}
	current := map[string]string{"mist": "v1.2.3"}

	got, ok, err := buildComponentsForNode(context.Background(), components, current, node, "stable:v1.2.3")
	if err != nil {
		t.Fatalf("buildComponentsForNode: %v", err)
	}
	if ok || len(got) != 0 {
		t.Fatalf("expected no components when current matches desired, got %d", len(got))
	}
}
