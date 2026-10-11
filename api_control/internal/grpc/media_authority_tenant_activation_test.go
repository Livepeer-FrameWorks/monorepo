package grpc

import (
	"context"
	"database/sql"
	"net"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// capabilityCellFake is one cell's Foghorn answering the placement capability
// probe: with an attestation, with an error code, or, as a release that
// predates the probe, as unimplemented.
type capabilityCellFake struct {
	foghornpb.UnimplementedMediaAuthorityControlServiceServer
	capability    *foghornpb.MediaCellPlacementCapability
	code          codes.Code
	unimplemented bool
}

func (f *capabilityCellFake) GetMediaCellPlacementCapability(ctx context.Context, req *emptypb.Empty) (*foghornpb.MediaCellPlacementCapability, error) {
	if f.unimplemented {
		return f.UnimplementedMediaAuthorityControlServiceServer.GetMediaCellPlacementCapability(ctx, req)
	}
	if f.code != codes.OK {
		return nil, status.Error(f.code, "capability unavailable at fake cell")
	}
	return f.capability, nil
}

type capabilityDiscoveryFake struct {
	quartermasterpb.UnimplementedBootstrapServiceServer
	cells map[string]*net.TCPAddr
}

func (f *capabilityDiscoveryFake) DiscoverServices(_ context.Context, req *quartermasterpb.ServiceDiscoveryRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	addr := f.cells[req.GetClusterId()]
	if addr == nil {
		return &quartermasterpb.ServiceDiscoveryResponse{}, nil
	}
	host, port := addr.IP.String(), int32(addr.Port)
	return &quartermasterpb.ServiceDiscoveryResponse{Instances: []*quartermasterpb.ServiceInstance{{ClusterId: req.GetClusterId(), Status: "running", HealthStatus: "healthy", Host: &host, Port: &port}}}, nil
}

func serveCapabilityFake(t *testing.T, register func(*grpc.Server)) *net.TCPAddr {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	register(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", listener.Addr())
	}
	return addr
}

func newCapabilityProbeServer(t *testing.T, db *sql.DB, cells map[string]*capabilityCellFake) *CommodoreServer {
	t.Helper()
	discovery := &capabilityDiscoveryFake{cells: map[string]*net.TCPAddr{}}
	for cellID, fake := range cells {
		discovery.cells[cellID] = serveCapabilityFake(t, func(server *grpc.Server) {
			foghornpb.RegisterMediaAuthorityControlServiceServer(server, fake)
		})
	}
	qmAddr := serveCapabilityFake(t, func(server *grpc.Server) {
		quartermasterpb.RegisterBootstrapServiceServer(server, discovery)
	})
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: net.JoinHostPort(qmAddr.IP.String(), strconv.Itoa(qmAddr.Port)), AllowInsecure: true, Logger: logging.NewLogger(), Timeout: 5 * time.Second, ServiceToken: "commodore-service-token", PreferServiceToken: true})
	if err != nil {
		t.Fatal(err)
	}
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logging.NewLogger(), AllowInsecure: true, ServiceToken: "commodore-service-token"})
	t.Cleanup(func() { _ = qm.Close(); _ = pool.Close() })
	return &CommodoreServer{db: db, logger: logging.NewLogger(), quartermasterClient: qm, foghornPool: pool, foghornCandidateNext: make(map[string]int)}
}

// A tenant still on the legacy schema activates placement once every target
// cell attests it. A target cell that has not attested yet, or could not be
// asked, may still attest; the refresh is retried instead of dropped. A cell
// whose Foghorn states it cannot enforce placement keeps the tenant on the
// legacy schema, as before, without retrying.
func TestTenantPlacementActivationRetriesOnlyWhileACellMayStillAttest(t *testing.T) {
	capabilityColumns := []string{"cell_id", "max_schema_version", "enforcement_ready", "live_replicas", "attested_at"}
	for _, scenario := range []struct {
		name      string
		cellB     *capabilityCellFake
		recordedB *[2]any // max_schema_version, enforcement_ready recorded from B's answer
		wantRetry bool
	}{
		{name: "probe of B fails", cellB: &capabilityCellFake{code: codes.Unavailable}, wantRetry: true},
		{name: "B attests schema 2 but does not enforce yet", cellB: &capabilityCellFake{capability: &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 1}}, recordedB: &[2]any{int32(2), false}, wantRetry: true},
		{name: "B predates the capability probe", cellB: &capabilityCellFake{unimplemented: true}},
		{name: "B attests only the legacy schema", cellB: &capabilityCellFake{capability: &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1}, LiveReplicas: 1}}, recordedB: &[2]any{int32(1), false}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cellA := &capabilityCellFake{capability: readyPlacementAttestation()}
			server := newCapabilityProbeServer(t, db, map[string]*capabilityCellFake{"cell-a": cellA, "cell-b": scenario.cellB})
			payload := &mediapb.TenantAuthority{
				SchemaVersion:   1,
				TenantId:        "tenant-activation",
				Lifecycle:       mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
			}
			mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WithArgs("tenant", payload.GetTenantId()).WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.media_cell_placement_capabilities")).
				WillReturnRows(sqlmock.NewRows(capabilityColumns).AddRow("cell-a", int32(2), true, int32(1), time.Now()))
			afterProbe := sqlmock.NewRows(capabilityColumns).AddRow("cell-a", int32(2), true, int32(1), time.Now())
			if scenario.recordedB != nil {
				mock.ExpectBegin()
				mock.ExpectQuery("UpsertMediaCellPlacementCapability").
					WillReturnRows(sqlmock.NewRows([]string{"enforcement_ready", "first_ready_at", "activation_schema_version"}).AddRow(false, nil, int32(0)))
				mock.ExpectCommit()
				afterProbe.AddRow("cell-b", scenario.recordedB[0], scenario.recordedB[1], int32(1), time.Now())
			}
			mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.media_cell_placement_capabilities")).WillReturnRows(afterProbe)

			err = server.compileTenantPlacement(ctx, payload, nil, []string{"cell-a", "cell-b"})
			if scenario.wantRetry {
				if err == nil {
					t.Fatal("compileTenantPlacement returned nil: the activation is dropped until a later attestation re-queues it, want a retryable error")
				}
				if class, code := classifyAuthorityCompileError(err); class != authorityCompileTransient {
					t.Fatalf("activation error classified %d (%s), want transient so the obligation retries with backoff: %v", class, code, err)
				}
			} else if err != nil {
				t.Fatalf("a cell that cannot enforce placement must keep the tenant on the legacy schema without retrying: %v", err)
			}
			if payload.GetSchemaVersion() != 1 || payload.GetMediaPlacement() != nil {
				t.Fatalf("tenant left the legacy schema with a target cell not ready: %+v", payload)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func readyPlacementAttestation() *foghornpb.MediaCellPlacementCapability {
	return &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, EnforcementReady: true, LiveReplicas: 2}
}
