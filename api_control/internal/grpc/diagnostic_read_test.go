package grpc

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestDiagnosticAccessHistory(t *testing.T) {
	tenant, actor, target := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ctx := context.WithValue(ctxAsOperator(actor, tenant, "owner"), ctxkeys.KeyAuthType, "jwt")
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m.ExpectExec("RecordOperatorAccountAudit").WithArgs(tenant, actor, target, "diagnose.billing", `CLI diagnostic access requested; service="purser"`).WillReturnResult(sqlmock.NewResult(0, 1))
	s := &CommodoreServer{db: db}
	if _, err := s.AdminRecordDiagnosticRead(ctx, &commodorepb.AdminRecordDiagnosticReadRequest{Kind: "billing", TargetId: target, Service: "purser"}); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	for _, auth := range []string{"service", "api_token", "jwt"} {
		denied := context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, auth), ctxkeys.KeyUserID, actor)
		if _, err := s.AdminRecordDiagnosticRead(denied, &commodorepb.AdminRecordDiagnosticReadRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s: %v", auth, err)
		}
		if _, err := s.DiagnoseStream(denied, &commodorepb.DiagnoseStreamRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("stream %s: %v", auth, err)
		}
	}
}
