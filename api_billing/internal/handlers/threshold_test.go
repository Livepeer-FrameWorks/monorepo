package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

type mockCommodoreClient struct {
	invalidations int
	terminations  int
}

func (m *mockCommodoreClient) TerminateTenantStreams(ctx context.Context, tenantID, reason string) (*pb.TerminateTenantStreamsResponse, error) {
	m.terminations++
	return &pb.TerminateTenantStreamsResponse{}, nil
}

func (m *mockCommodoreClient) InvalidateTenantCache(ctx context.Context, tenantID, reason string) (*pb.InvalidateTenantCacheResponse, error) {
	m.invalidations++
	return &pb.InvalidateTenantCacheResponse{}, nil
}

func (m *mockCommodoreClient) GetTenantUserCount(ctx context.Context, tenantID string) (*pb.GetTenantUserCountResponse, error) {
	return &pb.GetTenantUserCountResponse{}, nil
}

func (m *mockCommodoreClient) GetTenantPrimaryUser(ctx context.Context, tenantID string) (*pb.GetTenantPrimaryUserResponse, error) {
	return &pb.GetTenantPrimaryUserResponse{}, nil
}

func TestEnforcePrepaidThresholds_ZeroCrossingInvalidatesCache(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	commodore := &mockCommodoreClient{}
	enforcer := NewThresholdEnforcer(db, logging.NewLogger(), commodore, nil)
	tenantID := "tenant-123"

	mock.ExpectQuery(`SELECT COALESCE\(billing_model, 'postpaid'\)`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_model"}).AddRow("prepaid"))

	err = enforcer.EnforcePrepaidThresholds(context.Background(), tenantID, 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if commodore.invalidations != 1 {
		t.Fatalf("expected 1 cache invalidation, got %d", commodore.invalidations)
	}
	if commodore.terminations != 0 {
		t.Fatalf("expected no terminations, got %d", commodore.terminations)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestEnforcePrepaidThresholds_SuspendsBelowThreshold(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	enforcer := NewThresholdEnforcer(db, logging.NewLogger(), nil, nil)
	tenantID := "tenant-456"

	mock.ExpectQuery(`SELECT COALESCE\(billing_model, 'postpaid'\)`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_model"}).AddRow("prepaid"))
	mock.ExpectExec("UPDATE purser.tenant_subscriptions").
		WithArgs(tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = enforcer.EnforcePrepaidThresholds(context.Background(), tenantID, -500, -1001)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestEnforcePrepaidThresholds_DoesNotSuspendAtThreshold(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	enforcer := NewThresholdEnforcer(db, logging.NewLogger(), nil, nil)
	tenantID := "tenant-789"

	mock.ExpectQuery(`SELECT COALESCE\(billing_model, 'postpaid'\)`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_model"}).AddRow("prepaid"))

	err = enforcer.EnforcePrepaidThresholds(context.Background(), tenantID, -500, -1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
