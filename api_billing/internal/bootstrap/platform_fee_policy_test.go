package bootstrap

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestReconcileDefaultPlatformFeePolicyCreatesWhenAbsent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT fee_basis_points[\s\S]*FROM purser\.platform_fee_policy`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO purser\.platform_fee_policy`).
		WithArgs(defaultMarketplaceFeeBPS).
		WillReturnResult(sqlmock.NewResult(0, 1))

	res, err := ReconcileDefaultPlatformFeePolicy(context.Background(), db)
	if err != nil {
		t.Fatalf("ReconcileDefaultPlatformFeePolicy: %v", err)
	}
	if len(res.Created) != 1 {
		t.Fatalf("expected created policy, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock: %v", err)
	}
}

func TestReconcileDefaultPlatformFeePolicyLeavesExistingRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT fee_basis_points[\s\S]*FROM purser\.platform_fee_policy`).
		WillReturnRows(sqlmock.NewRows([]string{"fee_basis_points"}).AddRow(1500))

	res, err := ReconcileDefaultPlatformFeePolicy(context.Background(), db)
	if err != nil {
		t.Fatalf("ReconcileDefaultPlatformFeePolicy: %v", err)
	}
	if len(res.Noop) != 1 {
		t.Fatalf("expected noop policy, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock: %v", err)
	}
}
