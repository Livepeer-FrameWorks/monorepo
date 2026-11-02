package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTierDowngradeRecoveryRequiresInteractivePlatformOperator(t *testing.T) {
	for _, kind := range []string{"service", "api_token", "jwt"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, kind)
			ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator")
			if kind != "jwt" {
				ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
			}
			_, err := (&PurserServer{}).AdminRecoverTierDowngrade(ctx, &purserpb.AdminRecoverTierDowngradeRequest{})
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("credential %s bypassed operator check: %v", kind, err)
			}
		})
	}
}

func TestTierDowngradeRecoveryValidatesConfirmation(t *testing.T) {
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator")
	ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	const tenant = "00000000-0000-4000-8000-000000000001"
	for _, r := range []*purserpb.AdminRecoverTierDowngradeRequest{
		{TenantId: "bad"},
		{TenantId: tenant, Confirmed: true, TargetTierId: "tier"},
		{TenantId: tenant, Confirmed: true, Reason: "pricing fixed"},
	} {
		if _, err := (&PurserServer{}).AdminRecoverTierDowngrade(ctx, r); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("request %v = %v, want InvalidArgument", r, err)
		}
	}
}

func operatorRecoveryCtx(t *testing.T) context.Context {
	t.Helper()
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator")
	return context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
}

func expectHeldDowngradeRow(mock sqlmock.Sqlmock, tenantID, invoiceStatus string) {
	mock.ExpectQuery(`-- name: GetTierDowngradeHold`).WithArgs(tenantID).WillReturnRows(sqlmock.NewRows(
		[]string{"tenant_id", "target_tier_id", "target_tier_name", "effective_at", "provider_confirmed", "invoice_id", "invoice_status"}).
		AddRow(tenantID, "tier-B", "developer", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), true, "invoice-1", invoiceStatus))
}

// A preview reports what holds the downgrade, so a script can branch on it,
// and writes nothing.
func TestTierDowngradeRecoveryPreviewNamesTheHold(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const tenant = "00000000-0000-4000-8000-000000000001"
	expectHeldDowngradeRow(mock, tenant, "manual_review")
	s := &PurserServer{billing: handlers.NewService(db, logging.NewLogger(), nil, nil, nil, nil, nil)}
	out, err := s.AdminRecoverTierDowngrade(operatorRecoveryCtx(t), &purserpb.AdminRecoverTierDowngradeRequest{TenantId: tenant, Reason: "pricing fixed"})
	if err != nil || out.GetStatus() != handlers.DowngradeRecoveryInvoiceHeld || out.GetDowngrade().GetInvoiceStatus() != "manual_review" {
		t.Fatalf("preview = %v, %v, want invoice_held with the manual_review invoice", out, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A confirmation for a target other than the previewed one is refused before
// anything is recorded.
func TestTierDowngradeRecoveryRefusesAChangedTarget(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const tenant = "00000000-0000-4000-8000-000000000001"
	expectHeldDowngradeRow(mock, tenant, "manual_review")
	s := &PurserServer{db: db, billing: handlers.NewService(db, logging.NewLogger(), nil, nil, nil, nil, nil)}
	_, err = s.AdminRecoverTierDowngrade(operatorRecoveryCtx(t), &purserpb.AdminRecoverTierDowngradeRequest{
		TenantId: tenant, Confirmed: true, Reason: "pricing fixed", TargetTierId: "tier-A",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale target = %v, want FailedPrecondition", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
