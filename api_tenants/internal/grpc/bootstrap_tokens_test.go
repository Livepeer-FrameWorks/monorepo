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

	"frameworks/pkg/ctxkeys"
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

func TestCreateEnrollmentTokenRejectsCrossTenantRequest(t *testing.T) {
	srv, _, _ := newMockQuartermasterServer(t)

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-caller")
	_, err := srv.CreateEnrollmentToken(ctx, &pb.CreateEnrollmentTokenRequest{
		ClusterId: "cluster-1",
		TenantId:  ptr("tenant-other"),
	})
	if err == nil {
		t.Fatal("expected permission error for tenant mismatch")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func ptr[T any](v T) *T { return &v }
