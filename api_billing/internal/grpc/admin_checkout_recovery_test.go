package grpc

import (
	"context"
	"database/sql"
	"strings"
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

func TestCheckoutRecoveryRequiresPlatformOperator(t *testing.T) {
	for _, auth := range []string{"", "service", "api_token", "jwt"} {
		ctx := context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyUserID, "ordinary-user"), ctxkeys.KeyAuthType, auth)
		s := &PurserServer{}
		if _, err := s.AdminListCheckoutRecovery(ctx, &purserpb.AdminListCheckoutRecoveryRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("inventory allowed %s: %v", auth, err)
		}
		if _, err := s.AdminRetainDeliveredCheckout(ctx, &purserpb.AdminRecoverCheckoutRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("delivery decision allowed %s: %v", auth, err)
		}
		if _, err := s.AdminRecoverCheckout(ctx, &purserpb.AdminRecoverCheckoutRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("recovery allowed %s: %v", auth, err)
		}
	}
}

func TestCheckoutRecoveryValidatesBeforeStorage(t *testing.T) {
	ctx := context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	s := &PurserServer{}
	if _, err := s.AdminListCheckoutRecovery(ctx, &purserpb.AdminListCheckoutRecoveryRequest{Limit: 101}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if _, err := s.AdminRecoverCheckout(ctx, &purserpb.AdminRecoverCheckoutRequest{TenantId: "mistyped"}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}

func TestCheckoutRecoveryReportsPendingForMissingCustomer(t *testing.T) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const tenant = "00000000-0000-4000-8000-000000000001"
	const intent = "00000000-0000-4000-8000-000000000002"
	for attempt := 0; attempt < 2; attempt++ {
		if attempt == 1 {
			m.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
		}
		m.ExpectBegin()
		m.ExpectQuery("LockTenantSubscriptionForProviderChange").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "tier_id", "status", "billing_model", "stripe_subscription_id", "mollie_subscription_id", "pending_reason", "free_fallback_provider_stopping", "free_fallback_provider_ended_at"}).AddRow("sub", "tier", "active", "postpaid", nil, nil, nil, false, nil))
		m.ExpectQuery("IsTenantBillingClosed").WillReturnRows(sqlmock.NewRows([]string{"closed"}).AddRow(false))
		m.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnError(sql.ErrNoRows)
		m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationJournalRows(intent, tenant, "mollie", "mollie_subscription_create", nil, nil, nil, "provider-sub", "pending", time.Now().Add(-time.Hour), 0))
		if attempt == 0 {
			m.ExpectQuery("InsertCheckoutRecoveryAudit").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("00000000-0000-4000-8000-000000000003"))
			m.ExpectExec("INSERT INTO purser.domain_event_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
			m.ExpectExec("RequestCheckoutRecovery").WillReturnResult(sqlmock.NewResult(0, 1))
			m.ExpectCommit()
		} else {
			m.ExpectCommit()
			m.ExpectQuery("GetMollieCustomerID").WillReturnError(sql.ErrNoRows)
			m.ExpectQuery("SELECT pg_advisory_unlock").WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))
		}
	}
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationJournalRows(intent, tenant, "mollie", "mollie_subscription_create", nil, nil, nil, "provider-sub", "pending", time.Now().Add(-time.Hour), 0))
	m.ExpectExec("FinishCheckoutRecoveryAudit").WillReturnResult(sqlmock.NewResult(0, 1))
	ctx := context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	s := &PurserServer{billing: handlers.NewService(db, logging.NewLogger(), nil, nil, nil, nil, nil)}
	out, err := s.AdminRecoverCheckout(ctx, &purserpb.AdminRecoverCheckoutRequest{TenantId: tenant, ReservationId: intent, Reason: "abandoned", Confirmed: true})
	if err != nil || out.GetOutcome() != "pending" || !strings.Contains(out.GetDetail(), "customer") {
		t.Fatalf("an existing reservation with missing provider data was misreported: %v %v", out, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutRecoveryRefusesAnUnrelatedPaymentIntent(t *testing.T) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const tenant = "00000000-0000-4000-8000-000000000001"
	const intent = "00000000-0000-4000-8000-000000000002"
	m.ExpectBegin()
	m.ExpectQuery("LockTenantSubscriptionForProviderChange").WillReturnRows(sqlmock.NewRows([]string{"subscription_id", "tier_id", "status", "billing_model", "stripe_subscription_id", "mollie_subscription_id", "pending_reason", "free_fallback_provider_stopping", "free_fallback_provider_ended_at"}).AddRow("sub", "tier", "active", "postpaid", nil, nil, nil, false, nil))
	m.ExpectQuery("IsTenantBillingClosed").WillReturnRows(sqlmock.NewRows([]string{"closed"}).AddRow(false))
	m.ExpectQuery("GetSubscriptionOperatorGrant").WillReturnError(sql.ErrNoRows)
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows(intent, tenant, "stripe", "prepaid_topup", nil, nil, nil, nil, "pending", time.Now().Add(time.Hour), 0))
	m.ExpectRollback()
	ctx := context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	s := &PurserServer{billing: handlers.NewService(db, logging.NewLogger(), nil, nil, nil, nil, nil)}
	_, err = s.AdminRecoverCheckout(ctx, &purserpb.AdminRecoverCheckoutRequest{TenantId: tenant, ReservationId: intent, Reason: "abandoned", Confirmed: true})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("recovery accepted a non-reservation payment intent: %v", err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutRecoveryPreviewCannotWrite(t *testing.T) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const tenant = "00000000-0000-4000-8000-000000000001"
	const intent = "00000000-0000-4000-8000-000000000002"
	for n := 0; n < 2; n++ {
		m.ExpectQuery("GetProviderCheckoutReservation").WithArgs(tenant, intent).WillReturnRows(checkoutReservationTestRows(intent, tenant, "stripe", "tenant_subscription_checkout", nil, nil, nil, nil, "provider_call_failed", time.Now().Add(time.Hour), 0))
	}
	ctx := context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	s := &PurserServer{billing: handlers.NewService(db, logging.NewLogger(), nil, nil, nil, nil, nil)}
	out, err := s.AdminRecoverCheckout(ctx, &purserpb.AdminRecoverCheckoutRequest{TenantId: tenant, ReservationId: intent, Reason: "abandoned"})
	if err != nil || out.GetOutcome() != "preview" || out.GetAuditId() != "" {
		t.Fatalf("unconfirmed request=%v err=%v", out, err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
