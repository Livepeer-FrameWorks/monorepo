package triggers

import (
	"testing"

	"frameworks/api_balancing/internal/control"
	"github.com/DATA-DOG/go-sqlmock"
)

func expectOfflineEffectInsert(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`INSERT INTO foghorn\.ingest_offline_effects`).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
}

func projectSourceForTest(t *testing.T, registry *control.StreamRegistry, internalName, nodeID string, pid int64, triggerUUID, generation string, revision int64) {
	t.Helper()
	_, applied, err := registry.ProjectSource(internalName, nodeID, pid, triggerUUID, generation, revision)
	if err != nil {
		t.Fatalf("project source: %v", err)
	}
	if !applied {
		t.Fatalf("source revision %d was not applied", revision)
	}
}

func markSourceInactiveForTest(t *testing.T, registry *control.StreamRegistry, internalName, nodeID, generation string, revision int64) bool {
	t.Helper()
	flipped, err := registry.PublishSourceInactive(internalName, nodeID, generation, revision)
	if err != nil {
		t.Fatalf("mark source inactive: %v", err)
	}
	return flipped
}
