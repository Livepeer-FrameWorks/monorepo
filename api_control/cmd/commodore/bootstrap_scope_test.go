package main

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"testing"

	"frameworks/api_control/internal/appconfig"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/lib/pq"
	"google.golang.org/grpc"
)

func TestAccountsOnlyCommandLeavesDeclaredStreamsAlone(t *testing.T) {
	t.Run("active account", func(t *testing.T) { testAccountsOnlyCommand(t, false) })
	t.Run("closed account", func(t *testing.T) { testAccountsOnlyCommand(t, true) })
}

func testAccountsOnlyCommand(t *testing.T, closed bool) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	quartermasterpb.RegisterTenantServiceServer(server, accountScopeQM{})
	go func() {
		if err := server.Serve(listener); err != nil {
			t.Errorf("serve fixture: %v", err)
		}
	}()
	t.Cleanup(server.Stop)
	load := func(logging.Logger) (*appconfig.CommodoreBootstrap, error) {
		return &appconfig.CommodoreBootstrap{QuartermasterClient: appconfig.QuartermasterClient{QuartermasterGRPCAddr: listener.Addr().String()}, GRPCClientTLS: config.GRPCClientTLS{AllowInsecure: true}}, nil
	}
	connect := func(database.Config, logging.Logger) *sql.DB { return db }
	path := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(path, []byte(`accounts:
  - kind: system_operator
    tenant: {ref: quartermaster.system_tenant}
    users:
      - email: scope@example.test
        role: owner
        password: fixture-password
        platform_operator: true
commodore:
  pull_streams:
    - playback_id: pull-fixture
      owner_tenant: {ref: quartermaster.system_tenant}
      title: Pull fixture
      source_uri: https://example.test/live
      enabled: true
  mist_native_streams:
    - playback_id: native-fixture
      owner_tenant: {ref: quartermaster.system_tenant}
      title: Native fixture
      source: ts-exec:fixture
`), 0600); err != nil {
		t.Fatal(err)
	}
	// Only account lookup and creation are expected; stream writes and prune are unexpected.
	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.users").WithArgs("10000000-0000-4000-8000-000000000001", "scope@example.test").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("SAVEPOINT bootstrap_account_user").WillReturnResult(sqlmock.NewResult(0, 0))
	if closed {
		mock.ExpectExec("INSERT INTO commodore.users").WillReturnError(&pq.Error{Code: "23514", Constraint: "tenant_account_closed", Message: "tenant account is permanently closed"})
		mock.ExpectExec("ROLLBACK TO SAVEPOINT bootstrap_account_user").WillReturnResult(sqlmock.NewResult(0, 0))
	} else {
		mock.ExpectExec("INSERT INTO commodore.users").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec("RELEASE SAVEPOINT bootstrap_account_user").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectClose()
	expectedCode := 0
	if closed {
		expectedCode = 1
	}
	if code := runBootstrapCommandWithRuntime([]string{"--file", path, "--accounts-only"}, load, connect); code != expectedCode {
		t.Fatalf("exit=%d", code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type accountScopeQM struct {
	quartermasterpb.UnimplementedTenantServiceServer
}

func (accountScopeQM) ResolveTenantAliases(context.Context, *quartermasterpb.ResolveTenantAliasesRequest) (*quartermasterpb.ResolveTenantAliasesResponse, error) {
	return &quartermasterpb.ResolveTenantAliasesResponse{Mapping: map[string]string{"frameworks": "10000000-0000-4000-8000-000000000001"}}, nil
}
