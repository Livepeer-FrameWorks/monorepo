package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

// Rename must retire the OLD label before ensuring the NEW one: retire is
// enqueued first so it gets the lower BIGSERIAL seq and the worker dispatches
// it ahead of the ensure. sqlmock enforces ordered expectations, so this test
// fails if the order flips.
func TestEnqueueTenantAliasForSubdomainChangeRetiresBeforeEnsure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "old", "", "", "retire").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("retire-1"))
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.deployment_tier, t\.is_active.*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "deployment_tier", "is_active", "has_cluster"}).
			AddRow("Acme", "new", "pro", true, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "new", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ensure-1"))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if enqErr := server.enqueueTenantAliasForSubdomainChange(ctx, tx, "tenant-1", "old", "new"); enqErr != nil {
		t.Fatalf("enqueueTenantAliasForSubdomainChange: %v", enqErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("ordering/expectations: %v", mErr)
	}
}

func TestEnqueueTenantAliasForSubdomainChangeClearRemoves(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()

	mock.ExpectBegin()
	// Clearing the subdomain → a single full teardown, no retire/ensure.
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "old", "", "", "remove").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("remove-1"))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if enqErr := server.enqueueTenantAliasForSubdomainChange(ctx, tx, "tenant-1", "old", ""); enqErr != nil {
		t.Fatalf("enqueueTenantAliasForSubdomainChange: %v", enqErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestEnqueueTenantAliasForTierChangeDowngrade(t *testing.T) {
	t.Run("removes when no paid access remains", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer func() { _ = db.Close() }()
		server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
		ctx := context.Background()

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT EXISTS`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
			WithArgs("tenant-1", "", "", "", "remove").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("remove-1"))

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if enqErr := server.enqueueTenantAliasForTierChange(ctx, tx, "tenant-1", true); enqErr != nil {
			t.Fatalf("enqueueTenantAliasForTierChange: %v", enqErr)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})

	t.Run("keeps alias when paid access remains", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer func() { _ = db.Close() }()
		server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
		ctx := context.Background()

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT EXISTS`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		// No teardown enqueued.

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if enqErr := server.enqueueTenantAliasForTierChange(ctx, tx, "tenant-1", true); enqErr != nil {
			t.Fatalf("enqueueTenantAliasForTierChange: %v", enqErr)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})
}

func TestSubscribeToClusterEnqueuesEnsure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")

	mock.ExpectQuery(`SELECT deployment_model FROM quartermaster\.infrastructure_clusters`).
		WithArgs("core-1").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_model"}).AddRow("shared"))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO quartermaster\.tenant_cluster_access`).
		WithArgs("tenant-1", "core-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.deployment_tier, t\.is_active.*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "deployment_tier", "is_active", "has_cluster"}).
			AddRow("Acme", "acme", "pro", true, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "acme", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ensure-1"))
	mock.ExpectCommit()

	if _, err := server.SubscribeToCluster(ctx, &pb.SubscribeToClusterRequest{ClusterId: "core-1"}); err != nil {
		t.Fatalf("SubscribeToCluster: %v", err)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestUnsubscribeFromClusterEnqueuesRemoveClusterThenTeardown(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")

	mock.ExpectBegin()
	// remove_cluster first (lower seq), then deactivate, then full teardown.
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "", "core-1", "cluster_unsubscribed", "remove_cluster").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rc-1"))
	mock.ExpectExec(`UPDATE quartermaster\.tenant_cluster_access`).
		WithArgs("tenant-1", "core-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "", "", "", "remove").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rm-1"))
	mock.ExpectCommit()

	if _, err := server.UnsubscribeFromCluster(ctx, &pb.UnsubscribeFromClusterRequest{ClusterId: "core-1"}); err != nil {
		t.Fatalf("UnsubscribeFromCluster: %v", err)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestGrantClusterAccessEnqueuesEnsure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO quartermaster\.tenant_cluster_access`).
		WithArgs("tenant-1", "core-1", "read", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.deployment_tier, t\.is_active.*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "deployment_tier", "is_active", "has_cluster"}).
			AddRow("Acme", "acme", "pro", true, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "acme", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ensure-1"))
	mock.ExpectCommit()

	if _, err := server.GrantClusterAccess(context.Background(), &pb.GrantClusterAccessRequest{
		TenantId: "tenant-1", ClusterId: "core-1",
	}); err != nil {
		t.Fatalf("GrantClusterAccess: %v", err)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// recordAliasOutboxFailure must INCREMENT attempts (attempts + 1), not write
// the carried value back — otherwise the counter sticks at 0 and alert
// thresholds never fire.
func TestRecordAliasOutboxFailureIncrementsAttempts(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectExec(`UPDATE quartermaster\.navigator_tenant_alias_outbox\s+SET attempts = attempts \+ 1`).
		WithArgs("outbox-1", "boom").
		WillReturnResult(sqlmock.NewResult(0, 1))

	server.recordAliasOutboxFailure(context.Background(), "outbox-1", 3, errors.New("boom"))
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}
