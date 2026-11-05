package grpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFinancialAuditRejectsNonOperatorBeforeStorage(t *testing.T) {
	for _, authType := range []string{"", "service", "api_token", "jwt"} {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, authType)
		_, err := (&PurserServer{}).AdminFinancialAuditPage(ctx, &purserpb.AdminFinancialAuditPageRequest{})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("auth=%s err=%v", authType, err)
		}
	}
}

func TestFinancialAuditPagesRetainedRowsWithTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.WithValue(context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	from, through := time.Unix(0, 0).UTC(), time.Now().Add(-time.Minute).UTC()
	tenant := "c8bb4a7d-f739-49c2-8a77-6315216ad4ce"
	rows := sqlmock.NewRows([]string{"id", "payload"})
	for i := 1; i <= 101; i++ {
		id := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
		rows.AddRow(id, fmt.Sprintf(`{"id":%q}`, id))
	}
	mock.ExpectQuery(`-- name: FinancialAuditPage`).WithArgs("invoices", from, through, "", tenant).WillReturnRows(rows)
	page, err := (&PurserServer{db: db}).AdminFinancialAuditPage(ctx, &purserpb.AdminFinancialAuditPageRequest{TenantId: tenant, Source: "invoices", From: timestamppb.New(from), Through: timestamppb.New(through)})
	if err != nil || len(page.GetRecordsJson()) != 100 || page.GetNextCursor() != "00000064-0000-4000-8000-000000000000" {
		t.Fatalf("page=%+v error=%v", page, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinancialAuditRejectsInvalidScopeBeforeStorage(t *testing.T) {
	ctx := context.WithValue(context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	for _, r := range []*purserpb.AdminFinancialAuditPageRequest{
		{TenantId: "not-a-tenant", From: timestamppb.New(time.Unix(0, 0)), Through: timestamppb.New(time.Now().Add(-time.Minute))},
		{TenantId: "c8bb4a7d-f739-49c2-8a77-6315216ad4ce", Source: "invoices"},
	} {
		_, err := (&PurserServer{}).AdminFinancialAuditPage(ctx, r)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err=%v", err)
		}
	}
}
