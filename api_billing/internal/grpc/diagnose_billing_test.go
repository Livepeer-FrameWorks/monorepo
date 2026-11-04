package grpc

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestDiagnoseBillingAuthorization(t *testing.T) {
	for _, auth := range []string{"", "service", "api_token", "jwt"} {
		ctx := context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, auth), ctxkeys.KeyPlatformOperator, true)
		_, err := (&PurserServer{}).DiagnoseBilling(ctx, &purserpb.DiagnoseBillingRequest{})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("auth=%s: %v", auth, err)
		}
	}
	ctx := context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	if _, err := (&PurserServer{}).DiagnoseBilling(ctx, &purserpb.DiagnoseBillingRequest{TenantId: "bad"}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenant := uuid.New()
	m.ExpectQuery("GetBillingDiagnosis").WithArgs(tenant).WillReturnRows(sqlmock.NewRows([]string{"diagnosis"}))
	if _, err := (&PurserServer{db: db}).DiagnoseBilling(ctx, &purserpb.DiagnoseBillingRequest{TenantId: tenant.String()}); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
