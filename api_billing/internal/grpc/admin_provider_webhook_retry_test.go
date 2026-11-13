package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestProviderWebhookRetryPreviewRefusesPolledMollieConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenant, webhook := "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003"
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "00000000-0000-4000-8000-000000000001")
	ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id::text AS id").WithArgs(tenant, webhook).WillReturnRows(sqlmock.NewRows([]string{"id", "provider", "provider_payment_id", "status", "last_error_code", "attempts", "updated_at"}).AddRow(webhook, "mollie", "tr_conflict", "blocked", "evidence_conflict", 1, time.Now()))
	mock.ExpectCommit()
	preview, err := (&PurserServer{db: db}).AdminRetryProviderWebhook(ctx, &purserpb.AdminRetryProviderWebhookRequest{TenantId: tenant, WebhookId: webhook})
	if err != nil || preview.GetEligible() || preview.GetRetryRequested() {
		t.Fatalf("already-claimed payment replay offered for settlement conflict: %+v %v", preview, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderWebhookRetryRequiresInteractiveOperator(t *testing.T) {
	for _, kind := range []string{"service", "api_token", "jwt"} {
		ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, kind)
		ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "00000000-0000-4000-8000-000000000001")
		if kind != "jwt" {
			ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
		}
		if _, err := (&PurserServer{}).AdminRetryProviderWebhook(ctx, &purserpb.AdminRetryProviderWebhookRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("credential %s bypassed operator gate: %v", kind, err)
		}
	}
}

func TestProviderWebhookRetryRefusesUnfencedConfirmation(t *testing.T) {
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "00000000-0000-4000-8000-000000000001")
	ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	for _, request := range []*purserpb.AdminRetryProviderWebhookRequest{
		{TenantId: "bad"},
		{TenantId: "00000000-0000-4000-8000-000000000002", WebhookId: "bad"},
		{TenantId: "00000000-0000-4000-8000-000000000002", WebhookId: "00000000-0000-4000-8000-000000000003", Confirmed: true, Reason: "repair"},
	} {
		if _, err := (&PurserServer{}).AdminRetryProviderWebhook(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request reached database: %v", err)
		}
	}
}
