package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/lib/pq"
)

func TestValidateTenantRetriesRetryablePostgresErrors(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := "tenant-1"
	retryable := &pq.Error{
		Code:    "40001",
		Message: "schema version mismatch for table x: expected 2, got 1",
	}
	mock.ExpectQuery(`FROM quartermaster\.tenants\s+WHERE id = \$1`).
		WithArgs(tenantID).
		WillReturnError(retryable)
	mock.ExpectQuery(`FROM quartermaster\.tenants\s+WHERE id = \$1`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"name", "is_active", "rate_limit_per_minute", "rate_limit_burst",
		}).AddRow("Tenant", true, int32(60), int32(120)))

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	resp, err := server.ValidateTenant(context.Background(), &pb.ValidateTenantRequest{TenantId: tenantID})
	if err != nil {
		t.Fatalf("ValidateTenant: %v", err)
	}
	if !resp.GetValid() || resp.GetTenantName() != "Tenant" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
