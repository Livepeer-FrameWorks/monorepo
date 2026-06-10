package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnqueueNavigatorTenantAliasTxValidation(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()

	cases := []struct {
		name      string
		subdomain string
		action    string
		clusterID string
		wantErr   bool
	}{
		{"ensure requires subdomain", "", "ensure", "", true},
		{"retire requires subdomain", "", "retire", "", true},
		{"remove_cluster requires cluster", "", "remove_cluster", "", true},
		{"unknown action", "acme", "frobnicate", "", true},
		{"remove is tenant-only", "", "remove", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Only the "remove" success case reaches the INSERT; mock it loosely.
			mockDB, mock, mErr := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if mErr != nil {
				t.Fatalf("sqlmock: %v", mErr)
			}
			defer func() { _ = mockDB.Close() }()
			srv := NewQuartermasterServer(mockDB, logging.NewLogger(), nil, nil, nil, nil, nil)
			if !tc.wantErr {
				mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("outbox-1"))
			}
			_, gotErr := srv.EnqueueNavigatorTenantAliasTx(ctx, mockDB, "tenant-1", tc.subdomain, tc.action, tc.clusterID, "")
			if (gotErr != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", gotErr, tc.wantErr)
			}
		})
	}
	_ = server
}

func TestClaimAliasOutboxBatchHonorsNextRetryAt(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM quartermaster\.navigator_tenant_alias_outbox o.*\(o\.next_retry_at IS NULL OR o\.next_retry_at <= NOW\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "subdomain", "cluster_id", "reason", "action", "attempts"}))
	mock.ExpectCommit()

	if rows, claimErr := server.claimAliasOutboxBatch(context.Background()); claimErr != nil {
		t.Fatalf("claimAliasOutboxBatch: %v", claimErr)
	} else if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestAliasOutboxStoreReturnsMarkCompletedError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	store := &aliasOutboxStore{server: server}

	mock.ExpectExec(`UPDATE quartermaster\.navigator_tenant_alias_outbox\s+SET completed_at = NOW\(\)`).
		WithArgs("outbox-1").
		WillReturnError(errors.New("db down"))

	if err := store.MarkCompleted(context.Background(), "outbox-1"); err == nil {
		t.Fatal("MarkCompleted returned nil for failed persistence")
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestAliasOutboxStoreReturnsRecordFailureError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	store := &aliasOutboxStore{server: server}

	mock.ExpectExec(`UPDATE quartermaster\.navigator_tenant_alias_outbox\s+SET attempts = attempts \+ 1`).
		WithArgs("outbox-1", "boom", "1000 milliseconds").
		WillReturnError(errors.New("db down"))

	if err := store.RecordFailure(context.Background(), "outbox-1", 0, nil, errors.New("boom"), time.Second); err == nil {
		t.Fatal("RecordFailure returned nil for failed persistence")
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// Alias eligibility is an explicit allowlist of monthly paid tiers. free,
// payg (prepaid pay-as-you-go), the legacy ”/global values, and unknown tier
// names are all ineligible — the gate fails closed.
func TestEnqueueTenantAliasEnsureTxSkipsIneligibleTiers(t *testing.T) {
	for _, tier := range []string{"free", "", "payg", "global", "pro"} {
		t.Run("tier="+tier, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()
			server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
			ctx := context.Background()

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.deployment_tier, t\.is_active.*FOR UPDATE`).
				WithArgs("tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "deployment_tier", "is_active", "has_cluster"}).
					AddRow("Acme", "acme", tier, true, true))
			// No INSERT expected: ineligible tiers get no alias.

			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if enqErr := server.enqueueTenantAliasEnsureTx(ctx, tx, "tenant-1", false); enqErr != nil {
				t.Fatalf("enqueueTenantAliasEnsureTx: %v", enqErr)
			}
			if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
				t.Fatalf("unexpected queries: %v", mockErr)
			}
		})
	}
}

func TestEnqueueTenantAliasEnsureTxEnqueuesForPaidActive(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.deployment_tier, t\.is_active.*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "deployment_tier", "is_active", "has_cluster"}).
			AddRow("Acme", "acme", "supporter", true, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "acme", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("outbox-1"))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if enqErr := server.enqueueTenantAliasEnsureTx(ctx, tx, "tenant-1", false); enqErr != nil {
		t.Fatalf("enqueueTenantAliasEnsureTx: %v", enqErr)
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unexpected queries: %v", mockErr)
	}
}
