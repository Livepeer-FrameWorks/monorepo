package datamigrations

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

func TestInspectNodeIdentityKeysReportsWithoutMutating(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	progress, err := inspectNodeIdentityKeys(context.Background(), db, datamigrate.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Scanned != 3 || progress.Skipped != 3 || !progress.Done || progress.Changed != 0 {
		t.Fatalf("unexpected progress: %+v", progress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyNodeIdentityKeysBlocksKeylessActiveNodes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT node.node_id").WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("edge-recover-1"))

	err = verifyNodeIdentityKeys(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "token-authorized identity-key recovery") || !strings.Contains(err.Error(), "edge-recover-1") {
		t.Fatalf("expected recovery gate error, got %v", err)
	}
}

func TestNodeIdentityKeyGateNeverMutatesFingerprints(t *testing.T) {
	lower := strings.ToLower(managedActiveKeylessNodeCountSQL + managedActiveKeylessNodeIDsSQL)
	for _, mutation := range []string{"update ", "insert ", "delete "} {
		if strings.Contains(lower, mutation) {
			t.Fatalf("identity-key census must be read-only; found %q", mutation)
		}
	}
}

func TestNodeIdentityKeyGateOnlyBlocksOperatorManagedNodes(t *testing.T) {
	lower := strings.ToLower(managedActiveKeylessNodeCountSQL + managedActiveKeylessNodeIDsSQL)
	if !strings.Contains(lower, "node.enrollment_origin in ('gitops_seed', 'adopted_local')") {
		t.Fatalf("identity-key release gate must follow operator remediation ownership: %s", lower)
	}
	if strings.Contains(lower, "'runtime_enrolled'") {
		t.Fatalf("runtime-enrolled self-hosted nodes must not block the platform release gate: %s", lower)
	}
}
