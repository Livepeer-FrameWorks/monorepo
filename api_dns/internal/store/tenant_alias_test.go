package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

var tenantAliasColumns = []string{
	"tenant_id", "subdomain", "status", "cert_issued_at", "last_error", "created_at", "updated_at",
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
		AddRow("t1", "acme", "cert_issued", nil, nil, now, now).
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
		AddRow("t1", "acme", "cert_issuing", nil, nil, now, now).
		RowError(0, errors.New("connection reset mid-iteration"))
	mock.ExpectQuery(`FROM navigator\.tenant_aliases`).WillReturnRows(rows)

	if _, err := st.ListPendingTenantAliases(context.Background()); err == nil {
		t.Fatal("expected row iteration error to propagate")
	}
}
