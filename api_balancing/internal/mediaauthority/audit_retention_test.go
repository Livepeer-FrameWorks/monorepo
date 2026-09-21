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

func TestPruneApplyAuditDrainsFullBatchesUntilShortOrCapped(t *testing.T) {
	const prune = `(?s)WITH expired AS.*DELETE FROM foghorn\.media_authority_apply_audit`
	t.Run("stops at the first short batch", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectExec(prune).WillReturnResult(sqlmock.NewResult(0, auditRetentionBatch))
		mock.ExpectExec(prune).WillReturnResult(sqlmock.NewResult(0, auditRetentionBatch))
		mock.ExpectExec(prune).WillReturnResult(sqlmock.NewResult(0, 3))

		rows, err := (&Store{db: db}).pruneApplyAudit(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if want := int64(2*auditRetentionBatch + 3); rows != want {
			t.Fatalf("pruned rows = %d, want %d", rows, want)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("is capped per tick", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for pass := 0; pass < auditRetentionPasses; pass++ {
			mock.ExpectExec(prune).WillReturnResult(sqlmock.NewResult(0, auditRetentionBatch))
		}

		rows, err := (&Store{db: db}).pruneApplyAudit(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if want := int64(auditRetentionPasses * auditRetentionBatch); rows != want {
			t.Fatalf("pruned rows = %d, want %d", rows, want)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
