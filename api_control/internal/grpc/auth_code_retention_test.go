package grpc

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
)

func TestAuthCodeRetentionDeletesOnlyDiscoveredOwnerKeys(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectQuery(`FROM commodore.auth_authorization_codes\s+WHERE expires_at <`).
		WithArgs(int32(100)).WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow(authSessionTestID, testTenantID))
	mock.ExpectExec(`DELETE FROM commodore.auth_authorization_codes`).WithArgs(authSessionTestID, testTenantID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM commodore.auth_authorization_codes\s+WHERE consumed_at <`).
		WithArgs(int32(100)).WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}))
	mock.ExpectQuery(`FROM commodore.auth_device_codes\s+WHERE expires_at <`).
		WithArgs(int32(100)).WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow("50000000-0000-4000-8000-000000000082", nil))
	mock.ExpectExec(`DELETE FROM commodore.auth_device_codes`).WithArgs("50000000-0000-4000-8000-000000000082", nil).WillReturnResult(sqlmock.NewResult(0, 1))
	deleted, err := s.sweepAuthCodeRetention(context.Background())
	if err != nil || deleted != 2 {
		t.Fatalf("expired code cleanup deleted=%d err=%v, want two discovered keys", deleted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
