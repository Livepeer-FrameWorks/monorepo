package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newOrchestratorServer(t *testing.T) (*sql.DB, *PeriscopeServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	server := &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-query-test"),
	}
	return db, server, mock
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("status = %v, want %v (err=%v)", status.Code(err), want, err)
	}
}

// Intent: GetOrchestrator must reject before touching the database when its
// required inputs are missing — tenant_id (multi-tenant isolation) and
// orch_addr (the lookup key). No query should be issued in either case.
func TestGetOrchestratorGuards(t *testing.T) {
	t.Run("missing tenant", func(t *testing.T) {
		db, server, mock := newOrchestratorServer(t)
		defer db.Close()
		_, err := server.GetOrchestrator(context.Background(), &periscopepb.GetOrchestratorRequest{OrchAddr: "0xabc"})
		wantCode(t, err, codes.InvalidArgument)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("no query should have run: %v", err)
		}
	})

	t.Run("missing orch_addr", func(t *testing.T) {
		db, server, mock := newOrchestratorServer(t)
		defer db.Close()
		_, err := server.GetOrchestrator(context.Background(), &periscopepb.GetOrchestratorRequest{TenantId: "tenant-1"})
		wantCode(t, err, codes.InvalidArgument)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("no query should have run: %v", err)
		}
	})

	t.Run("blank orch_addr is trimmed to empty", func(t *testing.T) {
		db, server, mock := newOrchestratorServer(t)
		defer db.Close()
		_, err := server.GetOrchestrator(context.Background(), &periscopepb.GetOrchestratorRequest{TenantId: "tenant-1", OrchAddr: "   "})
		wantCode(t, err, codes.InvalidArgument)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("no query should have run: %v", err)
		}
	})
}

// Intent: when no identity row exists for (tenant, orch_addr), GetOrchestrator
// returns NotFound — and the lookup is tenant-scoped. The query filter and
// bound args pin that the row is matched by BOTH tenant_id and orch_addr, so a
// tenant can never resolve another tenant's orchestrator. No instance/vantage
// queries run once the identity row is absent.
func TestGetOrchestratorNotFoundIsTenantScoped(t *testing.T) {
	db, server, mock := newOrchestratorServer(t)
	defer db.Close()

	mock.ExpectQuery(`orchestrator_state_current FINAL[\s\S]*WHERE tenant_id = \? AND orch_addr = \?`).
		WithArgs("tenant-1", "0xabc").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "orch_addr", "last_seen", "updated_at"}))

	_, err := server.GetOrchestrator(context.Background(), &periscopepb.GetOrchestratorRequest{
		TenantId: "tenant-1",
		OrchAddr: "0xabc",
	})
	wantCode(t, err, codes.NotFound)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expected exactly the tenant-scoped identity query: %v", err)
	}
}
