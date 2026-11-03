package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const grantNoticeTenant = "00000000-0000-4000-8000-0000000000b1"

func grantNoticeOperatorCtx() context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator")
	return context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
}

func TestGrantNoticeRetryRequiresInteractivePlatformOperator(t *testing.T) {
	for _, kind := range []string{"service", "api_token", "jwt"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, kind)
			ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator")
			if kind != "jwt" {
				ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
			}
			_, err := (&PurserServer{}).AdminResendOperatorGrantNotice(ctx, &purserpb.AdminResendOperatorGrantNoticeRequest{})
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("credential %s bypassed operator check: %v", kind, err)
			}
		})
	}
}

// The preview names the tenant's newest abandoned notice and its last error
// and writes nothing.
func TestGrantNoticeRetryPreviewShowsTheAbandonedNotice(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ends := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`-- name: GetAbandonedOperatorGrantNotice`).WithArgs(grantNoticeTenant).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "ends_at", "last_error", "recipient"}).
			AddRow(handlers.OperatorGrantNoticeEnded, ends, "550 mailbox unavailable", "billing@example.com"))
	out, err := (&PurserServer{db: db}).AdminResendOperatorGrantNotice(grantNoticeOperatorCtx(), &purserpb.AdminResendOperatorGrantNoticeRequest{TenantId: grantNoticeTenant})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetStatus() != "notice_abandoned" || out.GetKind() != handlers.OperatorGrantNoticeEnded || !out.GetEndsAt().AsTime().Equal(ends) ||
		out.GetRecipient() != "billing@example.com" || out.GetDetail() != "550 mailbox unavailable" {
		t.Fatalf("preview = %+v", out)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Confirmation needs the previewed notice and a reason, and refuses to
// requeue a notice without a billing email to send it to.
func TestGrantNoticeRetryConfirmationRefusals(t *testing.T) {
	ends := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		req  *purserpb.AdminResendOperatorGrantNoticeRequest
	}{
		{"no reason", &purserpb.AdminResendOperatorGrantNoticeRequest{TenantId: grantNoticeTenant, Confirmed: true, Kind: "ended", EndsAt: timestamppb.New(ends)}},
		{"unknown kind", &purserpb.AdminResendOperatorGrantNoticeRequest{TenantId: grantNoticeTenant, Confirmed: true, Reason: "r", Kind: "grace", EndsAt: timestamppb.New(ends)}},
		{"no end", &purserpb.AdminResendOperatorGrantNoticeRequest{TenantId: grantNoticeTenant, Confirmed: true, Reason: "r", Kind: "ended"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := (&PurserServer{}).AdminResendOperatorGrantNotice(grantNoticeOperatorCtx(), tc.req); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
		})
	}

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`-- name: GetOperatorGrantNoticeRecovery`).WithArgs(grantNoticeTenant, "ended", ends).
		WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "notified", "abandoned", "status", "recipient"}).
			AddRow("sub-1", false, true, "active", ""))
	mock.ExpectRollback()
	_, err = (&PurserServer{db: db}).AdminResendOperatorGrantNotice(grantNoticeOperatorCtx(), &purserpb.AdminResendOperatorGrantNoticeRequest{
		TenantId: grantNoticeTenant, Confirmed: true, Reason: "email repaired", Kind: "ended", EndsAt: timestamppb.New(ends),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("requeue without a billing email: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
