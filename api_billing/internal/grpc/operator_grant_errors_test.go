package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/DATA-DOG/go-sqlmock"
	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/yugabyte/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOperatorGrantAmountDatabaseLimit(t *testing.T) {
	for _, amount := range []string{"100000000", "1000000000", "1e9"} {
		t.Run(amount, func(t *testing.T) {
			_, err := parseOperatorGrantChange(&purserpb.AdminSetBillingGrantRequest{BasePrice: &amount, Reason: "test"}, time.Now())
			if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "eight") {
				t.Fatalf("amount %s: %v, want InvalidArgument with eight-digit limit", amount, err)
			}
		})
	}
}

func TestOperatorGrantSetMapsLookupErrors(t *testing.T) {
	for _, test := range []struct {
		name, tier string
		initial    bool
		err        error
		code       codes.Code
	}{
		{"unknown tier", "typo", false, sql.ErrNoRows, codes.NotFound},
		{"database down", "", false, errors.New("database offline"), codes.Internal},
		{"grant lookup down", "", true, errors.New("database offline"), codes.Internal},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if test.initial {
				mock.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnError(test.err)
			} else {
				mock.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnError(sql.ErrNoRows)
				query := "GetSubscriptionTierForGrant"
				if test.tier != "" {
					query = "GetTierByNameForOperatorAssignment"
				}
				mock.ExpectQuery(query).WillReturnError(test.err)
			}
			server := &PurserServer{db: db, logger: logging.NewLogger()}
			_, err = server.AdminSetBillingGrant(context.Background(), &purserpb.AdminSetBillingGrantRequest{TenantId: "10000000-0000-0000-0000-000000000001", TierName: test.tier, Collection: "invoice", Reason: "test"})
			if status.Code(err) != test.code {
				t.Fatalf("error=%v want %v", err, test.code)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOperatorAssignMapsGrantReadFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnError(errors.New("database unavailable"))
	_, err = (&PurserServer{db: db, logger: logging.NewLogger()}).AdminAssignTier(context.Background(), &purserpb.AdminAssignTierRequest{TenantId: "10000000-0000-0000-0000-000000000001", TierName: "enterprise", Reason: "assignment"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("assign error=%v, want Internal", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInheritedFallbackMissingMapsAtItsLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("GetPendingProviderCheckout").WillReturnRows(sqlmock.NewRows([]string{"pending", "deadline", "kind"}).AddRow(false, time.Unix(0, 0), "checkout"))
	mock.ExpectQuery("GetProviderSubscriptionState").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "has_stripe_subscription", "has_mollie_subscription"}).AddRow("sub", false, false))
	mock.ExpectQuery("GetFallbackTierForOperatorGrant").WithArgs("fallback-name").WillReturnError(sql.ErrNoRows)
	mutation := &operatorGrantMutation{previous: &purserdb.GetSubscriptionOperatorGrantRow{FallbackTierID: "fallback", FallbackTierName: "fallback-name"}, change: &operatorGrantChange{collection: "invoice", reason: "renewed"}}
	err = (&PurserServer{}).writeOperatorGrantMutationTx(context.Background(), tx, purserdb.LockTenantSubscriptionForOperatorAssignmentRow{SubscriptionID: "sub", TierID: "tier", TierName: "trial"}, "tenant", "tier", mutation)
	if status.Code(err) != codes.NotFound || !strings.Contains(err.Error(), "fallback-name") {
		t.Fatalf("fallback error=%v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorAssignMapsCurrentTierReadFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "base_price", "waive_usage", "collection", "expires_at", "reason", "granted_by", "granted_at", "fallback_tier_id", "assigned_tier_id", "fallback_tier_name", "active", "expiry_error", "expiry_failures"}).AddRow("sub", nil, true, "invoice", nil, "trial", "operator", time.Now(), "", "tier", "", true, nil, int32(0)))
	mock.ExpectQuery("GetSubscriptionTierForGrant").WillReturnError(errors.New("database unavailable"))
	_, err = (&PurserServer{db: db, logger: logging.NewLogger()}).AdminAssignTier(context.Background(), &purserpb.AdminAssignTierRequest{TenantId: "10000000-0000-0000-0000-000000000001", TierName: "enterprise", Reason: "assignment"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("current tier error=%v, want Internal", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantRevisionRaceIsAbortedAtItsSource(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnError(sql.ErrNoRows)
	mutation := &operatorGrantMutation{previous: &purserdb.GetSubscriptionOperatorGrantRow{GrantedAt: time.Now()}}
	if err := mutation.checkUnchanged(context.Background(), purserdb.New(db), "tenant"); status.Code(err) != codes.Aborted {
		t.Fatalf("revision conflict=%v, want Aborted", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantWritesMapTheirStorageErrors(t *testing.T) {
	for _, stage := range []string{"boundary", "delete", "provider", "upsert"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("database unavailable")
			mutation := &operatorGrantMutation{change: &operatorGrantChange{collection: "invoice", reason: "test"}}
			switch stage {
			case "boundary":
				mutation.at = time.Now()
				mock.ExpectExec("SetOperatorGrantBoundary").WillReturnError(failure)
			case "delete":
				mutation.change = nil
				mock.ExpectExec("DeleteSubscriptionOperatorGrant").WillReturnError(failure)
			case "provider":
				mock.ExpectQuery("GetPendingProviderCheckout").WillReturnRows(sqlmock.NewRows([]string{"pending", "deadline", "kind"}).AddRow(false, time.Unix(0, 0), "checkout"))
				mock.ExpectQuery("GetProviderSubscriptionState").WillReturnError(failure)
			case "upsert":
				mock.ExpectQuery("GetPendingProviderCheckout").WillReturnRows(sqlmock.NewRows([]string{"pending", "deadline", "kind"}).AddRow(false, time.Unix(0, 0), "checkout"))
				mock.ExpectQuery("GetProviderSubscriptionState").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "has_stripe_subscription", "has_mollie_subscription"}).AddRow("sub", false, false))
				mock.ExpectExec("UpsertSubscriptionOperatorGrant").WillReturnError(failure)
			}
			err = (&PurserServer{}).writeOperatorGrantMutationTx(context.Background(), tx, purserdb.LockTenantSubscriptionForOperatorAssignmentRow{SubscriptionID: "sub", TierID: "tier"}, "tenant", "tier", mutation)
			if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "database unavailable") {
				t.Fatalf("%s error=%v, want Internal with cause", stage, err)
			}
			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGrantExpiryFailedReadRetainsLastCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_expiry_failed_items"}, nil)
	gauge.WithLabelValues().Set(100)
	registry := prometheus.NewRegistry()
	if registerErr := registry.Register(gauge); registerErr != nil {
		t.Fatal(registerErr)
	}
	mock.ExpectQuery("ListExpiredOperatorGrants").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "granted_at"}))
	mock.ExpectQuery("CountFailedOperatorGrantExpiries").WillReturnError(errors.New("database unavailable"))
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_expiry_read_failures"}, nil)
	server := &PurserServer{db: db, logger: logging.NewLogger(), metrics: &ServerMetrics{OperatorGrantExpiryFailures: gauge, OperatorGrantExpiryReadFailures: counter}}
	server.expireOperatorGrantsOnce(context.Background())
	metrics, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || len(metrics[0].GetMetric()) != 1 {
		t.Fatalf("expiry metrics=%v", metrics)
	}
	value := metrics[0].GetMetric()[0].GetGauge().GetValue()
	if value != 100 {
		t.Fatalf("failed read cleared the last good count: %v", value)
	}
	if got := testutil.ToFloat64(counter.WithLabelValues()); got != 1 {
		t.Fatalf("read failures=%v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantFallbackLookupKeepsRetryableSQLState(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		for _, code := range []string{"40001", "40P01"} {
			db, mock, dbErr := sqlmock.New()
			if dbErr != nil {
				t.Fatal(dbErr)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectBegin()
			tx, beginErr := db.BeginTx(t.Context(), nil)
			if beginErr != nil {
				t.Fatal(beginErr)
			}
			mock.ExpectQuery("GetPendingProviderCheckout").WillReturnRows(sqlmock.NewRows([]string{"pending", "deadline", "kind"}).AddRow(false, time.Unix(0, 0), "checkout"))
			mock.ExpectQuery("GetProviderSubscriptionState").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "has_stripe_subscription", "has_mollie_subscription"}).AddRow("sub", false, false))
			failure := &pgconn.PgError{Code: code, Message: "fallback lock conflict"}
			mock.ExpectQuery("GetFallbackTierForOperatorGrant").WillReturnError(failure)
			mutation := &operatorGrantMutation{change: &operatorGrantChange{waiveUsage: true, collection: "invoice", reason: "trial"}}
			if explicit {
				name := "fallback"
				mutation.fallbackTierName = &name
			}
			callErr := (&PurserServer{}).writeOperatorGrantMutationTx(t.Context(), tx, purserdb.LockTenantSubscriptionForOperatorAssignmentRow{SubscriptionID: "sub", TierID: "current", TierName: "current"}, "tenant", "target", mutation)
			if fwdb.SQLState(callErr) != code || !errors.Is(callErr, failure) {
				t.Fatalf("explicit=%v code=%s error lost retry cause: %v", explicit, code, callErr)
			}
			mock.ExpectRollback()
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			if unmet := mock.ExpectationsWereMet(); unmet != nil {
				t.Fatal(unmet)
			}
		}
	}
}

func TestGrantExpiryQueueReadFailureCounted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_expiry_queue_read_failures"}, nil)
	mock.ExpectQuery("ListExpiredOperatorGrants").WillReturnError(errors.New("database unavailable"))
	server := &PurserServer{db: db, logger: logging.NewLogger(), metrics: &ServerMetrics{OperatorGrantExpiryReadFailures: counter}}
	server.expireOperatorGrantsOnce(context.Background())
	if got := testutil.ToFloat64(counter.WithLabelValues()); got != 1 {
		t.Fatalf("queue read failures=%v, want 1", got)
	}
	if unmet := mock.ExpectationsWereMet(); unmet != nil {
		t.Fatal(unmet)
	}
}
