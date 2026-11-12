package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func TestSessionRetentionReplaysAbortedTransactions(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	key := sessionRetentionKey{ID: authSessionTestID, TenantID: testTenantID, UserID: sessionTestUserID}
	for attempt := 0; attempt < 2; attempt++ {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM commodore.auth_sessions`).WithArgs(key.TenantID, key.UserID, key.ID).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(key.ID))
		mock.ExpectQuery(`SELECT id FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID, int32(1)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("expired"))
		mock.ExpectQuery(`SELECT EXISTS`).WithArgs(key.TenantID, key.UserID).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectExec(`DELETE FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID, "expired").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`DELETE FROM commodore.auth_sessions`).WithArgs(key.TenantID, key.UserID, key.ID).WillReturnResult(sqlmock.NewResult(0, 1))
		commit := mock.ExpectCommit()
		if attempt == 0 {
			commit.WillReturnError(&pq.Error{Code: "40001", Message: "catalog version changed"})
		}
	}
	deleted, tokens, err := s.retainEndedSession(context.Background(), key, 1)
	if err != nil || deleted != 2 || tokens != 1 {
		t.Fatalf("retention did not replay or counted a rolled-back attempt: deleted=%d tokens=%d err=%v", deleted, tokens, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUnassignedRetentionReplaysAbortedTransactions(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	key := sessionRetentionKey{ID: authSessionTestID, TenantID: testTenantID, UserID: sessionTestUserID}
	for attempt := 0; attempt < 2; attempt++ {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(key.ID))
		mock.ExpectQuery(`SELECT EXISTS`).WithArgs(key.TenantID, key.UserID).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectExec(`DELETE FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID).WillReturnResult(sqlmock.NewResult(0, 1))
		commit := mock.ExpectCommit()
		if attempt == 0 {
			commit.WillReturnError(&pq.Error{Code: "40001", Message: "catalog version changed"})
		}
	}
	deleted, err := s.retainUnassignedToken(context.Background(), key)
	if err != nil || deleted != 1 {
		t.Fatalf("unassigned retention did not replay its transaction: deleted=%d err=%v", deleted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
