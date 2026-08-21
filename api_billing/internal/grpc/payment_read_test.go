package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func paymentRows(now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "invoice_id", "method", "amount", "currency", "tx_id", "status",
		"confirmed_at", "created_at", "updated_at",
	}).AddRow("pay-1", "inv-1", "card", 12.5, "EUR", "pi_safe", "failed", nil, now, now)
}

func TestGetPaymentBindsAuthenticatedTenant(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()
	mock.ExpectQuery(`FROM purser\.billing_payments payment`).
		WithArgs("pay-1", true, "tenant-1").
		WillReturnRows(paymentRows(now))

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	payment, err := s.GetPayment(ctx, &purserpb.GetPaymentRequest{PaymentId: "pay-1"})
	if err != nil {
		t.Fatal(err)
	}
	if payment.GetId() != "pay-1" || payment.GetInvoiceId() != "inv-1" || payment.GetStatus() != "failed" {
		t.Fatalf("payment = %+v", payment)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPaymentsPaginatesAndBindsTenant(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()
	mock.ExpectQuery(`SELECT COUNT\(\*\).*FROM purser\.billing_payments payment`).
		WithArgs("tenant-1", false, "", false, "", false, "").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT payment\.id::text AS id,\s+payment\.invoice_id::text AS invoice_id`).
		WithArgs("tenant-1", false, "", false, "", false, "", false, false, nil, "00000000-0000-0000-0000-000000000000", int32(21)).
		WillReturnRows(paymentRows(now))

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	resp, err := s.ListPayments(ctx, &purserpb.ListPaymentsRequest{
		TenantId:   "tenant-1",
		Pagination: &commonpb.CursorPaginationRequest{First: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetPayments()) != 1 || resp.GetPagination().GetTotalCount() != 1 {
		t.Fatalf("response = %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListPaymentsRejectsCrossTenantRequest(t *testing.T) {
	s := newGuardServer(t)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
	_, err := s.ListPayments(ctx, &purserpb.ListPaymentsRequest{TenantId: "tenant-b"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
}
