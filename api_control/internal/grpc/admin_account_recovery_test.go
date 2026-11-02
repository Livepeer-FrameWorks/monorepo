package grpc

import (
	"context"
	"database/sql"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// retirementRegistry is a Quartermaster listing fixed Foghorn instances for a
// cell, and a Foghorn whose control services answer Unimplemented.
type retirementRegistry struct {
	quartermasterpb.UnimplementedBootstrapServiceServer
	foghornpb.UnimplementedTenantControlServiceServer
	instances []*quartermasterpb.ServiceInstance
	failure   error
}

func (r *retirementRegistry) DiscoverServices(context.Context, *quartermasterpb.ServiceDiscoveryRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	if r.failure != nil {
		return nil, r.failure
	}
	return &quartermasterpb.ServiceDiscoveryResponse{Instances: r.instances}, nil
}

// newRetirementServer serves the registry and returns its address and a
// Commodore wired to it over db.
func newRetirementServer(t *testing.T, registry *retirementRegistry, db *sql.DB) (*CommodoreServer, string) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	quartermasterpb.RegisterBootstrapServiceServer(rpc, registry)
	foghornpb.RegisterTenantControlServiceServer(rpc, registry)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), Timeout: 5 * time.Second, ServiceToken: "commodore-service-token", PreferServiceToken: true})
	if err != nil {
		t.Fatal(err)
	}
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logging.NewLogger(), AllowInsecure: true, ServiceToken: "commodore-service-token"})
	t.Cleanup(func() { _ = qm.Close(); _ = pool.Close() })
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), quartermasterClient: qm, foghornPool: pool, foghornCandidateNext: make(map[string]int)}
	return server, listener.Addr().String()
}

func instanceAt(t *testing.T, addr string) *quartermasterpb.ServiceInstance {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	portNumber := int32(number)
	return &quartermasterpb.ServiceInstance{Status: "running", HealthStatus: "healthy", Host: &host, Port: &portNumber}
}

// closedAddr is a loopback address nothing listens on, so a dial is refused.
func closedAddr(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func operatorContext(tenantID string) context.Context {
	return context.WithValue(ctxAsOperator(uuid.NewString(), tenantID, "owner"), ctxkeys.KeyAuthType, "jwt")
}

func TestAccountStopSettlesOnlyUnreachableRetiredCells(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("removed-cell").AddRow("offline-cell"))
	mock.ExpectQuery("retired_ingest_cells").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("removed-cell"))
	server := &CommodoreServer{db: db, logger: logging.NewLogger()}
	for _, cell := range []string{"removed-cell", "offline-cell"} {
		server.foghornDiscovery.Store(cell, &foghornDiscoveryEntry{at: time.Now()})
	}
	err = server.stopAccountIngest(t.Context(), "tenant", "", nil)
	if err == nil || !strings.Contains(err.Error(), "offline-cell") {
		t.Fatalf("an unretired offline cell must keep the stop pending: %v", err)
	}
	if strings.Contains(err.Error(), "removed-cell") {
		t.Fatalf("an unreachable retired cell still holds the stop: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("removed-cell"))
	mock.ExpectQuery("retired_ingest_cells").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("removed-cell"))
	if err = server.stopAccountIngest(t.Context(), "tenant", "", nil); err != nil {
		t.Fatalf("a stop owed only to a retired unreachable cell did not settle: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAccountStopKeepsStopWhenRetirementCannotBeRead(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("removed-cell"))
	mock.ExpectQuery("retired_ingest_cells").WillReturnError(sql.ErrConnDone)
	server := &CommodoreServer{db: db, logger: logging.NewLogger()}
	server.foghornDiscovery.Store("removed-cell", &foghornDiscoveryEntry{at: time.Now()})
	if err = server.stopAccountIngest(t.Context(), "tenant", "", nil); err == nil || !strings.Contains(err.Error(), "removed-cell") {
		t.Fatalf("an unreadable retirement record settled the stop: %v", err)
	}
}

func TestAccountStopKeepsAnsweringRetiredCellPending(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, addr := newRetirementServer(t, &retirementRegistry{}, db)
	server.foghornDiscovery.Store("returned-cell", &foghornDiscoveryEntry{addrs: []string{addr}, at: time.Now()})
	mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("returned-cell"))
	// No retirement read is expected: an answering cell is never a candidate,
	// and an unexpected read would surface as a "retired" error.
	err = server.stopAccountIngest(t.Context(), "tenant", "", nil)
	if err == nil || !strings.Contains(err.Error(), "Unimplemented") || strings.Contains(err.Error(), "retired") {
		t.Fatalf("a cell that answers must keep the stop pending with its answer: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetireIngestCellRequiresOperatorAndReason(t *testing.T) {
	s := &CommodoreServer{}
	service := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if _, err := s.AdminRetireIngestCell(service, &commodorepb.AdminRetireIngestCellRequest{ClusterId: "cell", Reason: "gone"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("service credential retired a cell: %v", err)
	}
	if _, err := s.AdminGetTenantAccountState(service, &commodorepb.AdminGetTenantAccountStateRequest{TenantId: uuid.NewString()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("service credential read account state: %v", err)
	}
	op := operatorContext(uuid.NewString())
	for _, request := range []*commodorepb.AdminRetireIngestCellRequest{{Reason: "gone"}, {ClusterId: "cell", Reason: "  "}} {
		if _, err := s.AdminRetireIngestCell(op, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("request=%v error=%v", request, err)
		}
	}
	if _, err := s.AdminGetTenantAccountState(op, &commodorepb.AdminGetTenantAccountStateRequest{TenantId: "not-a-uuid"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid tenant: %v", err)
	}
}

func TestRetireIngestCellRefusesUnlessTheCellIsGone(t *testing.T) {
	op := operatorContext(uuid.NewString())
	request := &commodorepb.AdminRetireIngestCellRequest{ClusterId: "removed-cell", Reason: "hardware returned"}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	unreadable, _ := newRetirementServer(t, &retirementRegistry{failure: status.Error(codes.Internal, "registry down")}, db)
	if _, err = unreadable.AdminRetireIngestCell(op, request); status.Code(err) != codes.Unavailable {
		t.Fatalf("an unreadable registry let retirement proceed: %v", err)
	}

	answering := &retirementRegistry{}
	server, addr := newRetirementServer(t, answering, db)
	answering.instances = []*quartermasterpb.ServiceInstance{instanceAt(t, closedAddr(t)), instanceAt(t, addr)}
	if _, err = server.AdminRetireIngestCell(op, request); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), addr) {
		t.Fatalf("a cell whose Foghorn answers was retired: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a refused retirement wrote state: %v", err)
	}
}

func TestRetireIngestCellRetriesOnlyStopsThatIncludeTheCell(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := &retirementRegistry{}
	server, _ := newRetirementServer(t, registry, db)
	registry.instances = []*quartermasterpb.ServiceInstance{instanceAt(t, closedAddr(t))}
	op := operatorContext(uuid.NewString())
	retiredAt := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("INSERT INTO commodore.retired_ingest_cells").WithArgs("removed-cell", sqlmock.AnyArg(), "hardware returned").
		WillReturnRows(sqlmock.NewRows([]string{"cell_id", "retired_by", "reason", "retired_at"}).AddRow("removed-cell", "operator", "hardware returned", retiredAt))
	// One statement finds the stops delivered to the cell and reschedules
	// them; the real-engine lifecycle test proves which stops it selects.
	mock.ExpectQuery(`affected AS \(\s*SELECT DISTINCT target_key FROM cells WHERE cell_id = \$3::text`).
		WithArgs("", commodoredb.LiveStreamMediaAuthorityTarget("").Key, "removed-cell").
		WillReturnRows(sqlmock.NewRows([]string{"matched", "retried"}).AddRow(1, 1))
	response, err := server.AdminRetireIngestCell(op, &commodorepb.AdminRetireIngestCellRequest{ClusterId: " removed-cell ", Reason: "hardware returned"})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetClusterId() != "removed-cell" || response.GetAlreadyRetired() || response.GetPendingIngestStops() != 1 || !response.GetRetiredAt().AsTime().Equal(retiredAt) {
		t.Fatalf("response=%v", response)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTenantAccountStateReportsClosureSyncAndStops(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenant := uuid.NewString()
	closedAt := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	mock.ExpectQuery("FROM commodore.tenant_account_states").WithArgs(tenant).
		WillReturnRows(sqlmock.NewRows([]string{"closed_at", "closed_by", "quartermaster_closed_at", "closure_attempted_at", "closure_retry_after", "closure_error", "closure_failures"}).
			AddRow(closedAt, "operator", nil, closedAt.Add(time.Minute), closedAt.Add(2*time.Minute), "quartermaster unavailable", 3))
	mock.ExpectQuery("lane = 'ingest_revocation'").WithArgs(tenant, commodoredb.LiveStreamMediaAuthorityTarget("").Key, pendingIngestStopsPerTenant+1).
		WillReturnRows(pendingStopRows().
			AddRow(commodoredb.TenantMediaAuthorityTarget(tenant).Key, commodoredb.MediaAuthorityTargetTenant, tenant, "pending", 3, closedAt, closedAt.Add(time.Hour), "stop ingest on cell removed-cell: unavailable", "{live-cell,removed-cell}", "{removed-cell}"))
	state, err := (&CommodoreServer{db: db}).AdminGetTenantAccountState(operatorContext(tenant), &commodorepb.AdminGetTenantAccountStateRequest{TenantId: strings.ToUpper(tenant)})
	if err != nil {
		t.Fatal(err)
	}
	if state.GetClosureSync() != closureSyncRetrying || state.GetClosureFailures() != 3 || state.GetClosureError() != "quartermaster unavailable" || state.GetQuartermasterClosedAt() != nil {
		t.Fatalf("closure state=%v", state)
	}
	stops := state.GetPendingIngestStops()
	if state.GetPendingIngestStopsTruncated() {
		t.Fatal("a complete stop list reported as truncated")
	}
	if len(stops) != 1 || stops[0].GetTargetId() != tenant || strings.Join(stops[0].GetCells(), ",") != "live-cell,removed-cell" || strings.Join(stops[0].GetRetiredCells(), ",") != "removed-cell" {
		t.Fatalf("stops=%v", stops)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClosureSyncState(t *testing.T) {
	at := sql.NullTime{Time: time.Now(), Valid: true}
	for _, tc := range []struct {
		state commodoredb.TenantAccountClosureState
		want  string
	}{
		{commodoredb.TenantAccountClosureState{}, closureSyncOpen},
		{commodoredb.TenantAccountClosureState{ClosedAt: at}, closureSyncPending},
		{commodoredb.TenantAccountClosureState{ClosedAt: at, ClosureError: "down"}, closureSyncRetrying},
		{commodoredb.TenantAccountClosureState{ClosedAt: at, ClosureError: "down", QuartermasterClosedAt: at}, closureSyncSynchronized},
	} {
		if got := closureSyncState(tc.state); got != tc.want {
			t.Fatalf("state=%+v got %s want %s", tc.state, got, tc.want)
		}
	}
}

func pendingStopRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"target_key", "target_kind", "tenant_id", "status", "attempts", "pending_since", "next_attempt_at", "last_error", "cells", "retired_cells"})
}

func TestTenantAccountStateMarksTruncatedStopList(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenant := uuid.NewString()
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	mock.ExpectQuery("FROM commodore.tenant_account_states").WithArgs(tenant).WillReturnError(sql.ErrNoRows)
	rows := pendingStopRows()
	for i := range pendingIngestStopsPerTenant + 1 {
		rows.AddRow(commodoredb.LiveStreamMediaAuthorityTarget(uuid.NewString()).Key, commodoredb.MediaAuthorityTargetLiveStream, tenant, "pending", 1, at.Add(time.Duration(i)*time.Second), at.Add(time.Hour), "", "{offline-cell}", "{}")
	}
	mock.ExpectQuery("lane = 'ingest_revocation'").WithArgs(tenant, commodoredb.LiveStreamMediaAuthorityTarget("").Key, pendingIngestStopsPerTenant+1).WillReturnRows(rows)
	state, err := (&CommodoreServer{db: db}).AdminGetTenantAccountState(operatorContext(tenant), &commodorepb.AdminGetTenantAccountStateRequest{TenantId: tenant})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.GetPendingIngestStops()) != pendingIngestStopsPerTenant || !state.GetPendingIngestStopsTruncated() || state.GetClosureSync() != closureSyncOpen {
		t.Fatalf("stops=%d truncated=%v sync=%s", len(state.GetPendingIngestStops()), state.GetPendingIngestStopsTruncated(), state.GetClosureSync())
	}
}

func TestUnretireIngestCellRemovesTheRecord(t *testing.T) {
	s := &CommodoreServer{}
	service := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if _, err := s.AdminUnretireIngestCell(service, &commodorepb.AdminUnretireIngestCellRequest{ClusterId: "cell", Reason: "returned"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("service credential unretired a cell: %v", err)
	}
	op := operatorContext(uuid.NewString())
	for _, request := range []*commodorepb.AdminUnretireIngestCellRequest{{Reason: "returned"}, {ClusterId: "cell", Reason: " "}} {
		if _, err := s.AdminUnretireIngestCell(op, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("request=%v error=%v", request, err)
		}
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &CommodoreServer{db: db, logger: logging.NewLogger()}
	retiredAt := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("DELETE FROM commodore.retired_ingest_cells").WithArgs("wrong-cell").
		WillReturnRows(sqlmock.NewRows([]string{"cell_id", "retired_by", "reason", "retired_at"}).AddRow("wrong-cell", "operator", "typo", retiredAt))
	response, err := server.AdminUnretireIngestCell(op, &commodorepb.AdminUnretireIngestCellRequest{ClusterId: " wrong-cell ", Reason: "retired the wrong cell"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.GetWasRetired() || response.GetClusterId() != "wrong-cell" || response.GetRetirementReason() != "typo" || !response.GetRetiredAt().AsTime().Equal(retiredAt) {
		t.Fatalf("response=%v", response)
	}
	mock.ExpectQuery("DELETE FROM commodore.retired_ingest_cells").WithArgs("never-retired").WillReturnRows(sqlmock.NewRows([]string{"cell_id", "retired_by", "reason", "retired_at"}))
	response, err = server.AdminUnretireIngestCell(op, &commodorepb.AdminUnretireIngestCellRequest{ClusterId: "never-retired", Reason: "check"})
	if err != nil || response.GetWasRetired() || response.GetRetiredAt() != nil {
		t.Fatalf("a cell without a record: response=%v err=%v", response, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
