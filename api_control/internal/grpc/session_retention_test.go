package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func TestSessionRetentionPreservesLiveLegacyAgeAnchor(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	key := sessionRetentionKey{ID: authSessionTestID, TenantID: testTenantID, UserID: sessionTestUserID}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM commodore.auth_sessions`).WithArgs(key.TenantID, key.UserID, key.ID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(key.ID))
	mock.ExpectQuery(`SELECT id FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID, int32(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ancestor"))
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs(key.TenantID, key.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()
	deleted, tokens, err := s.retainEndedSession(context.Background(), key, 2)
	if err != nil || deleted != 0 || tokens != 0 {
		t.Fatalf("live legacy chain lost its age anchor: deleted=%d tokens=%d err=%v", deleted, tokens, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRetentionReplayCountsOnlyCommittedWrites(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	key := sessionRetentionKey{ID: authSessionTestID, TenantID: testTenantID, UserID: sessionTestUserID}
	for attempt := range 2 {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM commodore.auth_sessions`).WithArgs(key.TenantID, key.UserID, key.ID).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(key.ID))
		tokens := sqlmock.NewRows([]string{"id"}).AddRow("first")
		if attempt == 1 {
			tokens.AddRow("second")
		}
		mock.ExpectQuery(`SELECT id FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID, int32(2)).WillReturnRows(tokens)
		mock.ExpectQuery(`SELECT EXISTS`).WithArgs(key.TenantID, key.UserID).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectExec(`DELETE FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID, "first").WillReturnResult(sqlmock.NewResult(0, 1))
		if attempt == 0 {
			mock.ExpectExec(`DELETE FROM commodore.auth_sessions`).WithArgs(key.TenantID, key.UserID, key.ID).
				WillReturnError(&pq.Error{Code: "40001", Message: "restart read required"})
			mock.ExpectRollback()
		} else {
			mock.ExpectExec(`DELETE FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID, "second").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`DELETE FROM commodore.auth_sessions`).WithArgs(key.TenantID, key.UserID, key.ID).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
		}
	}
	deleted, tokens, err := s.retainEndedSession(context.Background(), key, 2)
	if err != nil || deleted != 3 || tokens != 2 {
		t.Fatalf("replay consumed budget for rolled-back writes: deleted=%d tokens=%d err=%v", deleted, tokens, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRetentionCommitsOnlyBoundedOwnerKeys(t *testing.T) {
	for _, failDelete := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[failDelete], func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			key := sessionRetentionKey{ID: authSessionTestID, TenantID: testTenantID, UserID: sessionTestUserID}
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id FROM commodore.auth_sessions`).WithArgs(key.TenantID, key.UserID, key.ID).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(key.ID))
			mock.ExpectQuery(`SELECT id FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID, int32(1)).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("expired"))
			mock.ExpectQuery(`SELECT EXISTS`).WithArgs(key.TenantID, key.UserID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			deletion := mock.ExpectExec(`DELETE FROM commodore.refresh_tokens`).WithArgs(key.TenantID, key.UserID, key.ID, "expired")
			if failDelete {
				deletion.WillReturnError(errors.New("write failed"))
				mock.ExpectRollback()
			} else {
				deletion.WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`DELETE FROM commodore.auth_sessions`).WithArgs(key.TenantID, key.UserID, key.ID).
					WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectCommit()
			}
			deleted, tokens, err := s.retainEndedSession(context.Background(), key, 1)
			if failDelete {
				if err == nil || deleted != 0 || tokens != 0 {
					t.Fatalf("failed transaction acknowledged deletion: count=%d tokens=%d err=%v", deleted, tokens, err)
				}
			} else if err != nil || deleted != 1 || tokens != 1 {
				t.Fatalf("bounded owner deletion count=%d tokens=%d err=%v", deleted, tokens, err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionRetentionAdvancesPastProtectedOwner(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	var progress sessionRetentionProgress
	ended := time.Now().UTC().Add(-40 * 24 * time.Hour)
	zeroAt, zeroID := progress.Expired.values()
	mock.ExpectQuery(`FROM commodore.auth_sessions\s+WHERE idle_expires_at <`).WithArgs(zeroAt, zeroID, sessionRetentionBatch).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "user_id", "ended_at"}).AddRow(authSessionTestID, testTenantID, sessionTestUserID, ended))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM commodore.auth_sessions`).WithArgs(testTenantID, sessionTestUserID, authSessionTestID).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	mock.ExpectQuery(`FROM commodore.auth_sessions\s+WHERE revoked_at <`).WithArgs(zeroAt, zeroID, sessionRetentionBatch).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "user_id", "ended_at"}))
	mock.ExpectQuery(`FROM commodore.refresh_tokens\s+WHERE session_id IS NULL`).WithArgs(zeroAt, zeroID, sessionRetentionBatch).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "user_id", "ended_at"}))
	mock.ExpectQuery(`FROM commodore.refresh_tokens\s+WHERE session_id IS NOT NULL`).WithArgs(zeroAt, zeroID, sessionRetentionBatch).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "user_id", "session_id", "ended_at"}))
	if _, err := s.sweepSessionRetention(context.Background(), &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Expired.ID != authSessionTestID || !progress.Expired.At.Equal(ended) {
		t.Fatalf("locked candidate blocked keyset progress: %+v", progress.Expired)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
