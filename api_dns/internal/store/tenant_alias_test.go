package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

var tenantAliasColumns = []string{
	"tenant_id", "subdomain", "status", "authority_version", "cert_issued_at", "last_error", "created_at", "updated_at",
}

func TestRevokeTenantAliasClusterAuthorityRetriesSerializationFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	st := NewStore(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO navigator\.tenant_alias_cluster_authority`).
		WithArgs("cluster-1", int64(7), "tenant-1").
		WillReturnError(&pq.Error{Code: "40001", Message: "restart read required"})
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO navigator\.tenant_alias_cluster_authority`).
		WithArgs("cluster-1", int64(7), "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"applied"}).AddRow(true))
	mock.ExpectExec(`DELETE FROM navigator\.tenant_edge_apply_state`).
		WithArgs("tenant-1", "cluster-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if applied, err := st.RevokeTenantAliasClusterAuthority(context.Background(), "tenant-1", "cluster-1", 7); err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRevokeTenantAliasClusterAuthorityRetriesDuplicateKeyInsertRace pins the
// scoped 23505 retry: some Yugabyte versions surface a concurrent
// grant/tombstone insert race as duplicate-key instead of 40001; the re-run
// must take the ON CONFLICT arm and apply normally.
func TestRevokeTenantAliasClusterAuthorityRetriesDuplicateKeyInsertRace(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	st := NewStore(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO navigator\.tenant_alias_cluster_authority`).
		WithArgs("cluster-1", int64(7), "tenant-1").
		WillReturnError(&pq.Error{Code: "23505", Message: "duplicate key value violates unique constraint"})
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO navigator\.tenant_alias_cluster_authority`).
		WithArgs("cluster-1", int64(7), "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"applied"}).AddRow(true))
	mock.ExpectExec(`DELETE FROM navigator\.tenant_edge_apply_state`).
		WithArgs("tenant-1", "cluster-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if applied, err := st.RevokeTenantAliasClusterAuthority(context.Background(), "tenant-1", "cluster-1", 7); err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRevokeTenantAliasClusterAuthoritySupersededSkipsDelete pins the
// zero-rows branch: a superseded (or alias-less) tombstone must commit as
// applied=false without running the edge delete.
func TestRevokeTenantAliasClusterAuthoritySupersededSkipsDelete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	st := NewStore(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO navigator\.tenant_alias_cluster_authority`).
		WithArgs("cluster-1", int64(7), "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"applied"}))
	mock.ExpectCommit()

	if applied, err := st.RevokeTenantAliasClusterAuthority(context.Background(), "tenant-1", "cluster-1", 7); err != nil || applied {
		t.Fatalf("superseded revocation applied=%v err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSetTenantAliasStatusRetriesSerializationFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	st := NewStore(db, nil)

	mock.ExpectExec(`UPDATE navigator\.tenant_aliases`).
		WithArgs("tearing_down", "", "tenant-1", "", int64(0)).
		WillReturnError(&pq.Error{Code: "40001", Message: "restart read required"})
	mock.ExpectExec(`UPDATE navigator\.tenant_aliases`).
		WithArgs("tearing_down", "", "tenant-1", "", int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if transitioned, err := st.SetTenantAliasStatus(context.Background(), "tenant-1", "", 0, "tearing_down", ""); err != nil || !transitioned {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A DB error surfaced mid-iteration must not be swallowed: these lists feed the
// cert-issuance and alias DNS/teardown workers, so a partial pass with no error
// would look like a successful (but incomplete) reconcile with no retry signal.
func TestListTenantAliasesByStatusPropagatesRowError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	st := NewStore(db, nil)

	now := time.Now()
	rows := sqlmock.NewRows(tenantAliasColumns).
		AddRow("t1", "acme", "cert_issued", int64(1), nil, nil, now, now).
		RowError(0, errors.New("connection reset mid-iteration"))
	mock.ExpectQuery(`FROM navigator\.tenant_aliases`).WillReturnRows(rows)

	if _, err := st.ListTenantAliasesByStatus(context.Background(), []string{"cert_issued"}); err == nil {
		t.Fatal("expected row iteration error to propagate")
	}
}

func TestListPendingTenantAliasesPropagatesRowError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	st := NewStore(db, nil)

	now := time.Now()
	rows := sqlmock.NewRows(tenantAliasColumns).
		AddRow("t1", "acme", "cert_issuing", int64(1), nil, nil, now, now).
		RowError(0, errors.New("connection reset mid-iteration"))
	mock.ExpectQuery(`FROM navigator\.tenant_aliases`).WillReturnRows(rows)

	if _, err := st.ListPendingTenantAliases(context.Background()); err == nil {
		t.Fatal("expected row iteration error to propagate")
	}
}
