package mediaauthority

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPruneApplyAuditUsesBoundedRetentionDelete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec(`(?s)WITH expired AS.*DELETE FROM foghorn\.media_authority_apply_audit`).
		WithArgs(int64(auditRetention.Seconds()), auditRetentionBatch).
		WillReturnResult(sqlmock.NewResult(0, 17))

	rows, err := (&Store{db: db}).pruneApplyAudit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rows != 17 {
		t.Fatalf("pruned rows = %d, want 17", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
