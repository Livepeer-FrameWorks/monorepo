package grpc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A commit rejected with SQLSTATE 40001 replays the whole callback in a fresh
// transaction instead of surfacing the serialization failure to the caller.
func TestSetNodeEnrollmentOriginReplaysTransactionAfterSerializationFailure(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	selectOrigin := `SELECT enrollment_origin FROM quartermaster\.infrastructure_nodes WHERE node_id = \$1`
	updateOrigin := `UPDATE quartermaster\.infrastructure_nodes SET enrollment_origin = \$1`

	mock.ExpectBegin()
	mock.ExpectQuery(selectOrigin).WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"enrollment_origin"}).AddRow("runtime_enrolled"))
	mock.ExpectExec(updateOrigin).WithArgs("adopted_local", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(&pq.Error{Code: "40001", Message: "could not serialize access due to concurrent update"})

	mock.ExpectBegin()
	mock.ExpectQuery(selectOrigin).WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"enrollment_origin"}).AddRow("runtime_enrolled"))
	mock.ExpectExec(updateOrigin).WithArgs("adopted_local", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := server.SetNodeEnrollmentOrigin(serviceCtx(), &quartermasterpb.SetNodeEnrollmentOriginRequest{
		NodeId: "node-1", EnrollmentOrigin: "adopted_local",
	})
	if err != nil {
		t.Fatalf("SetNodeEnrollmentOrigin: %v", err)
	}
	if resp.GetEnrollmentOrigin() != "adopted_local" {
		t.Fatalf("enrollment_origin = %q, want adopted_local", resp.GetEnrollmentOrigin())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A plain PostgreSQL serialization failure raised by a statement inside the
// callback is wrapped in a gRPC status, yet still replays the transaction.
func TestSetNodeEnrollmentOriginReplaysStatementSerializationFailure(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	selectOrigin := `SELECT enrollment_origin FROM quartermaster\.infrastructure_nodes WHERE node_id = \$1`
	updateOrigin := `UPDATE quartermaster\.infrastructure_nodes SET enrollment_origin = \$1`

	mock.ExpectBegin()
	mock.ExpectQuery(selectOrigin).WithArgs("node-1").
		WillReturnError(&pq.Error{Code: "40001", Message: "could not serialize access due to concurrent update"})
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectQuery(selectOrigin).WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"enrollment_origin"}).AddRow("runtime_enrolled"))
	mock.ExpectExec(updateOrigin).WithArgs("adopted_local", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := server.SetNodeEnrollmentOrigin(serviceCtx(), &quartermasterpb.SetNodeEnrollmentOriginRequest{
		NodeId: "node-1", EnrollmentOrigin: "adopted_local",
	})
	if err != nil {
		t.Fatalf("SetNodeEnrollmentOrigin: %v", err)
	}
	if resp.GetEnrollmentOrigin() != "adopted_local" {
		t.Fatalf("enrollment_origin = %q, want adopted_local", resp.GetEnrollmentOrigin())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Once the retry budget is spent, the caller sees the callback's original
// gRPC code and message, not a commit-wrapped Internal error.
func TestSetNodeEnrollmentOriginSurfacesOriginalStatusAfterRetriesExhausted(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	selectOrigin := `SELECT enrollment_origin FROM quartermaster\.infrastructure_nodes WHERE node_id = \$1`
	cause := &pq.Error{Code: "40001", Message: "could not serialize access due to concurrent update"}
	for range database.DefaultRetryAttempts {
		mock.ExpectBegin()
		mock.ExpectQuery(selectOrigin).WithArgs("node-1").WillReturnError(cause)
		mock.ExpectRollback()
	}

	_, err := server.SetNodeEnrollmentOrigin(serviceCtx(), &quartermasterpb.SetNodeEnrollmentOriginRequest{
		NodeId: "node-1", EnrollmentOrigin: "adopted_local",
	})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	want := "read current origin: " + cause.Error()
	if st.Code() != codes.Internal || st.Message() != want {
		t.Fatalf("status = %v %q, want Internal %q", st.Code(), st.Message(), want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestTxStatusErrorfPreservesStatusAndCause(t *testing.T) {
	cause := &pq.Error{Code: "40P01", Message: "deadlock detected"}
	err := txStatusErrorf(cause, codes.FailedPrecondition, "rotate node identity: %v", cause)

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("status.FromError did not recognise %T", err)
	}
	wantMessage := "rotate node identity: " + cause.Error()
	if st.Code() != codes.FailedPrecondition || st.Message() != wantMessage {
		t.Fatalf("status = %v %q, want FailedPrecondition %q", st.Code(), st.Message(), wantMessage)
	}
	if err.Error() != status.Error(codes.FailedPrecondition, wantMessage).Error() {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause not reachable through Unwrap")
	}
	if !database.IsRetryablePostgresError(err) {
		t.Fatal("wrapped 40P01 not classified retryable")
	}
	if database.IsRetryablePostgresError(txStatusErrorf(nil, codes.NotFound, "node not found")) {
		t.Fatal("status without a database cause classified retryable")
	}
}

// The stale-observation counter is a post-commit effect: a replayed callback
// must not increment it once per attempt.
func TestApplyTenantBillingEntitlementsCountsStaleObservationOnceAcrossReplay(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	stale := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_billing_entitlement_stale_total"}, nil)
	server.metrics = &ServerMetrics{BillingEntitlementStale: stale}
	observedAt := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	lockQuery := `SELECT deployment_tier, custom_subdomain_enabled, custom_domain_enabled,`

	// Attempt 1 sees an older observation, so it applies, and the apply aborts. By attempt 2 a newer observation has
	// committed, so this one is stale: it must be reported unapplied and counted once.
	mock.ExpectBegin()
	mock.ExpectQuery(lockQuery).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"deployment_tier", "custom_subdomain_enabled", "custom_domain_enabled", "billing_entitlements_observed_at",
		}).AddRow("production", true, true, observedAt.Add(-time.Minute)))
	mock.ExpectExec(`UPDATE quartermaster.tenants`).
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table tenants: expected 7, got 6"})
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectQuery(lockQuery).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"deployment_tier", "custom_subdomain_enabled", "custom_domain_enabled", "billing_entitlements_observed_at",
		}).AddRow("production", true, true, observedAt.Add(time.Minute)))
	mock.ExpectRollback()

	resp, err := server.ApplyTenantBillingEntitlements(serviceCtx(), &quartermasterpb.ApplyTenantBillingEntitlementsRequest{
		TenantId: "tenant-1", DeploymentTier: "free", ObservedAt: timestamppb.New(observedAt),
	})
	if err != nil {
		t.Fatalf("ApplyTenantBillingEntitlements: %v", err)
	}
	if resp.GetApplied() {
		t.Fatal("stale observation applied")
	}
	var metric dto.Metric
	if err := stale.WithLabelValues().Write(&metric); err != nil {
		t.Fatalf("read stale counter: %v", err)
	}
	if got := metric.GetCounter().GetValue(); got != 1 {
		t.Fatalf("stale counter = %v, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRetryableTxStatusNamesTheTransactionAndKeepsContextCodes(t *testing.T) {
	if got := retryableTxStatus(context.Canceled, "commit tenant update"); status.Code(got) != codes.Canceled {
		t.Fatalf("cancelled transaction = %v, want codes.Canceled", got)
	}
	if got := retryableTxStatus(fmt.Errorf("backoff: %w", context.DeadlineExceeded), "commit tenant update"); status.Code(got) != codes.DeadlineExceeded {
		t.Fatalf("expired transaction = %v, want codes.DeadlineExceeded", got)
	}
	got := retryableTxStatus(errors.New("driver: bad connection"), "commit tenant update")
	if status.Code(got) != codes.Internal || status.Convert(got).Message() != "tenant update transaction failed: driver: bad connection" {
		t.Fatalf("driver failure = %v", got)
	}
	if got := retryableTxStatus(errors.New("boom"), "failed to commit tenant creation"); status.Convert(got).Message() != "tenant creation transaction failed: boom" {
		t.Fatalf("legacy label = %v", got)
	}
	passthrough := status.Error(codes.NotFound, "tenant not found")
	if got := retryableTxStatus(passthrough, "commit tenant update"); !errors.Is(got, passthrough) {
		t.Fatalf("callback status rewritten: %v", got)
	}
}
