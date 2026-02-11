package grpc

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "frameworks/pkg/proto"
)

func newMockQuartermasterServer(t *testing.T) (*QuartermasterServer, *sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &QuartermasterServer{db: db}, db, mock
}

func TestValidateBootstrapTokenConsumeRaceRejected(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT kind, tenant_id, cluster_id, expected_ip::text, expires_at, usage_limit, usage_count, used_at
		FROM quartermaster.bootstrap_tokens
		WHERE token = $1
	`)).
		WithArgs("bt_edge").
		WillReturnRows(sqlmock.NewRows([]string{"kind", "tenant_id", "cluster_id", "expected_ip", "expires_at", "usage_limit", "usage_count", "used_at"}).
			AddRow("edge_node", "tenant-1", "cluster-1", nil, expiresAt, nil, int32(0), nil))

	mock.ExpectExec(regexp.QuoteMeta(`
			UPDATE quartermaster.bootstrap_tokens
			SET usage_count = usage_count + 1, used_at = NOW()
			WHERE token = $1
			  AND expires_at > NOW()
			  AND (
				(usage_limit IS NULL AND used_at IS NULL) OR
				(usage_limit IS NOT NULL AND usage_count < usage_limit)
			  )
		`)).
		WithArgs("bt_edge").
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := srv.ValidateBootstrapToken(context.Background(), &pb.ValidateBootstrapTokenRequest{
		Token:   "bt_edge",
		Consume: true,
	})
	if err != nil {
		t.Fatalf("ValidateBootstrapToken returned unexpected error: %v", err)
	}
	if resp.GetValid() {
		t.Fatal("expected token consume race to return invalid response")
	}
	if got := resp.GetReason(); got != "already_used" {
		t.Fatalf("expected reason already_used, got %q", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceRejectsClusterMismatchWithoutConsumingToken(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta(`
			SELECT kind, COALESCE(cluster_id, ''), expires_at
			FROM quartermaster.bootstrap_tokens
			WHERE token = $1 AND used_at IS NULL
		`)).
		WithArgs("bt_service").
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cluster_id", "expires_at"}).
			AddRow("service", "cluster-a", expiresAt))

	token := "bt_service"
	requestCluster := "cluster-b"
	_, err := srv.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:      "mist",
		Token:     &token,
		ClusterId: &requestCluster,
	})
	if err == nil {
		t.Fatal("expected cluster mismatch error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceTokenConsumeRaceReturnsUnauthenticated(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta(`
			SELECT kind, COALESCE(cluster_id, ''), expires_at
			FROM quartermaster.bootstrap_tokens
			WHERE token = $1 AND used_at IS NULL
		`)).
		WithArgs("bt_service").
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cluster_id", "expires_at"}).
			AddRow("service", "cluster-a", expiresAt))

	mock.ExpectExec(regexp.QuoteMeta(`
			UPDATE quartermaster.bootstrap_tokens
			SET used_at = NOW(), usage_count = usage_count + 1
			WHERE token = $1
			  AND kind = 'service'
			  AND used_at IS NULL
			  AND expires_at > NOW()
		`)).
		WithArgs("bt_service").
		WillReturnResult(sqlmock.NewResult(0, 0))

	token := "bt_service"
	_, err := srv.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:  "mist",
		Token: &token,
	})
	if err == nil {
		t.Fatal("expected unauthenticated error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %s", st.Code())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
