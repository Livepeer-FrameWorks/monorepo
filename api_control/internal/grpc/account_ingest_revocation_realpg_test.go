//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// liveAccountIngest records a live publisher until Foghorn receives its stop command.
type liveAccountIngest struct {
	foghornpb.UnimplementedTenantControlServiceServer
	mu          sync.Mutex
	live        map[string]bool
	unavailable bool
	calls       int
}

func (f *liveAccountIngest) TerminateTenantStreams(_ context.Context, _ *foghorncontrolpb.TerminateTenantStreamsRequest) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
	return f.stop(nil)
}

func (f *liveAccountIngest) TerminateStreamSessions(_ context.Context, r *foghorncontrolpb.TerminateStreamSessionsRequest) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
	return f.stop(r.GetStreamNames())
}

func (f *liveAccountIngest) stop(names []string) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.unavailable {
		return nil, status.Error(codes.Unavailable, "cell disconnected")
	}
	if len(names) == 0 {
		for name := range f.live {
			names = append(names, name)
		}
	}
	for _, name := range names {
		delete(f.live, name)
	}
	return &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: int32(len(names))}, nil
}

func (f *liveAccountIngest) active(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[name]
}

func (f *liveAccountIngest) recover() { f.mu.Lock(); defer f.mu.Unlock(); f.unavailable = false }

func testAccountRevocationStopsIngest(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, tc := range []struct {
		name                string
		closure, reactivate bool
	}{{name: "owner ingest stop"}, {name: "reactivated owner", reactivate: true}, {name: "tenant ingest stop", closure: true}} {
		closure := tc.closure
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tenant, user, other, stream := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			internal, otherInternal := "revoked-"+stream, "bystander-"+stream
			for _, id := range []string{user, other} {
				if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active) VALUES($1,$2,$3,true)`, id, tenant, id+"@revocation.test"); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			for _, item := range []struct{ id, owner, name string }{{stream, user, internal}, {uuid.NewString(), other, otherInternal}} {
				if _, writeErr := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,internal_name,stream_key,playback_id,title,active_ingest_cluster_id) VALUES($1,$2,$3,$4::text,$4::text,$4::text,'Live push','account-cell')`, item.id, tenant, item.owner, item.name); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			live := &liveAccountIngest{live: map[string]bool{internal: true, otherInternal: true}, unavailable: true}
			listener, listenErr := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
			if listenErr != nil {
				t.Fatal(listenErr)
			}
			rpc := grpc.NewServer()
			foghornpb.RegisterTenantControlServiceServer(rpc, live)
			quartermasterpb.RegisterTenantServiceServer(rpc, &operatorAccountQuartermaster{closureHealthy: tenant})
			go func() { _ = rpc.Serve(listener) }()
			defer rpc.Stop()
			qm, clientErr := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), ServiceToken: "test", PreferServiceToken: true})
			if clientErr != nil {
				t.Fatal(clientErr)
			}
			defer qm.Close()
			server := newTenantFanoutTestServer(t, &clusterRoute{})
			server.db, server.quartermasterClient, server.tokenHasher, server.authorityTenantSource = db, qm, testTokenHasher(t), operatorAccountTenantSource{}
			server.foghornDiscovery.Store("account-cell", &foghornDiscoveryEntry{addrs: []string{listener.Addr().String()}, at: time.Now()})
			op := context.WithValue(ctxAsOperator(uuid.NewString(), tenant, "owner"), ctxkeys.KeyAuthType, "jwt")
			row := commodoredb.ClaimMediaAuthorityObligationsRow{TargetKind: commodoredb.MediaAuthorityTargetLiveStream, TargetKey: commodoredb.LiveStreamMediaAuthorityTarget(stream).Key, TenantID: tenant}
			if closure {
				_, closeErr := server.AdminDeactivateTenant(op, &commodorepb.AdminDeactivateTenantRequest{TenantId: tenant, TenantName: "Trial Tenant", Reason: "close active tenant"})
				if closeErr != nil {
					t.Fatalf("offline cell blocks tenant synchronization: %v", closeErr)
				}
				var completed bool
				if readErr := db.QueryRowContext(ctx, `SELECT quartermaster_closed_at IS NOT NULL FROM commodore.tenant_account_states WHERE tenant_id=$1`, tenant).Scan(&completed); readErr != nil {
					t.Fatal(readErr)
				}
				if !completed {
					t.Fatal("Quartermaster closure was not completed independently of session stop")
				}
				if readErr := db.QueryRowContext(ctx, `SELECT lane,target_kind,target_key,tenant_id::text FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND lane=$2 AND target_kind=$3 AND status='pending'`, tenant, commodoredb.MediaAuthorityLaneIngestRevocation, commodoredb.MediaAuthorityTargetTenant).Scan(&row.Lane, &row.TargetKind, &row.TargetKey, &row.TenantID); readErr != nil {
					t.Fatalf("closure did not enqueue its durable tenant stop: %v", readErr)
				}

				if stopErr := server.compileMediaAuthorityObligation(ctx, row); stopErr == nil {
					t.Fatal("offline stop must remain pending independently of closure")
				}

			} else {
				server.mediaAuthorityPrivateKey = nil
				if _, deactivateErr := server.AdminSetUserActive(op, &commodorepb.AdminSetUserActiveRequest{TenantId: tenant, UserId: user, Active: false, Reason: "revoke active owner"}); deactivateErr != nil {
					t.Fatal(deactivateErr)
				}

				if readErr := db.QueryRowContext(ctx, `SELECT lane,target_kind,target_key,tenant_id::text FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND target_key=$2 AND lane=$3 AND status='pending'`, tenant, row.TargetKey, commodoredb.MediaAuthorityLaneIngestRevocation).Scan(&row.Lane, &row.TargetKind, &row.TargetKey, &row.TenantID); readErr != nil {
					t.Fatalf("deactivation did not independently queue its stream stop: %v", readErr)
				}
				if dispatchErr := server.compileMediaAuthorityObligation(ctx, row); dispatchErr == nil {
					t.Fatal("failed owner stop must remain pending")
				}
			}
			live.mu.Lock()
			attempts := live.calls
			live.mu.Unlock()
			if attempts == 0 {
				t.Fatal("account revocation never tried to stop the active push")
			}
			if !live.active(internal) {
				t.Fatal("failed delivery falsely reported publisher stopped")
			}
			if tc.reactivate {
				if _, err := server.AdminSetUserActive(op, &commodorepb.AdminSetUserActiveRequest{TenantId: tenant, UserId: user, Active: true, Reason: "restore owner before offline cell returns"}); err != nil {
					t.Fatal(err)
				}
				live.recover()
				if err := server.compileMediaAuthorityObligation(ctx, row); err != nil {
					t.Fatal(err)
				}
				live.mu.Lock()
				calls := live.calls
				live.mu.Unlock()
				if calls != attempts || !live.active(internal) {
					t.Fatalf("superseded stop touched the reactivated publisher: calls=%d before=%d", calls, attempts)
				}
				return
			}
			live.recover()
			if closure {
				if stopErr := server.compileMediaAuthorityObligation(ctx, row); stopErr != nil {
					t.Fatal(stopErr)
				}
			} else {
				row.Lane = commodoredb.MediaAuthorityLaneIngestRevocation
				var pending int
				if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND target_key=$2 AND lane=$3 AND status='pending'`, tenant, row.TargetKey, row.Lane).Scan(&pending); readErr != nil {
					t.Fatal(readErr)
				}
				if pending != 1 {
					t.Fatalf("failed stop has no durable independent obligation: %d", pending)
				}
				if compileErr := server.compileMediaAuthorityObligation(ctx, row); compileErr != nil {
					t.Fatalf("stop lane depends on authority compilation: %v", compileErr)
				}
			}
			if live.active(internal) {
				t.Fatal("revoked account's active push survived retry")
			}
			if live.active(otherInternal) == closure {
				t.Fatalf("tenant-wide stop=%v, bystander live=%v", closure, live.active(otherInternal))
			}
		})
	}
}

// Discovery reactivates the owner after the initial revocation read but before
// the per-cell stop decision. The positive control leaves the owner inactive.
type reactivatingAccountDiscovery struct {
	quartermasterpb.UnimplementedBootstrapServiceServer
	onDiscover func() error
	host       string
	port       int32
}

func (f *reactivatingAccountDiscovery) DiscoverServices(_ context.Context, r *quartermasterpb.ServiceDiscoveryRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	if f.onDiscover != nil {
		if err := f.onDiscover(); err != nil {
			return nil, err
		}
	}
	return &quartermasterpb.ServiceDiscoveryResponse{Instances: []*quartermasterpb.ServiceInstance{{ClusterId: r.GetClusterId(), Status: "running", HealthStatus: "healthy", Host: &f.host, Port: &f.port}}}, nil
}

func testAccountReactivationDuringCellResolution(t *testing.T, db *sql.DB) {
	for _, reactivate := range []bool{false, true} {
		t.Run(fmt.Sprint("reactivate in discovery=", reactivate), func(t *testing.T) {
			ctx := t.Context()
			tenant, user, stream := uuid.NewString(), uuid.NewString(), uuid.NewString()
			name := "cell-race-" + stream
			if _, err := db.Exec(`INSERT INTO commodore.users(id,tenant_id,email,is_active) VALUES($1,$2,$3,false)`, user, tenant, user+"@cell-race.test"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO commodore.streams(id,tenant_id,user_id,internal_name,stream_key,playback_id,title,active_ingest_cluster_id) VALUES($1,$2,$3,$4::text,$4::text,$4::text,'Cell resolution race','reactivation-cell')`, stream, tenant, user, name); err != nil {
				t.Fatal(err)
			}
			listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().(*net.TCPAddr)
			live := &liveAccountIngest{live: map[string]bool{name: true}}
			discovery := &reactivatingAccountDiscovery{host: addr.IP.String(), port: int32(addr.Port)}
			if reactivate {
				discovery.onDiscover = func() error {
					_, err := db.ExecContext(ctx, `UPDATE commodore.users SET is_active=true WHERE tenant_id=$1 AND id=$2`, tenant, user)
					return err
				}
			}
			rpc := grpc.NewServer()
			quartermasterpb.RegisterBootstrapServiceServer(rpc, discovery)
			foghornpb.RegisterTenantControlServiceServer(rpc, live)
			go func() { _ = rpc.Serve(listener) }()
			defer rpc.Stop()
			qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), ServiceToken: "test", PreferServiceToken: true})
			if err != nil {
				t.Fatal(err)
			}
			defer qm.Close()
			server := newTenantFanoutTestServer(t, &clusterRoute{})
			server.db = db
			server.quartermasterClient = qm
			if err := server.stopRevokedStreamIngest(ctx, tenant, stream); err != nil {
				t.Fatal(err)
			}
			live.mu.Lock()
			calls := live.calls
			live.mu.Unlock()
			if reactivate {
				if calls != 0 || !live.active(name) {
					t.Fatalf("per-cell recheck missed reactivation: calls=%d live=%v", calls, live.active(name))
				}
			} else if calls != 1 || live.active(name) {
				t.Fatalf("inactive-owner positive control did not stop: calls=%d live=%v", calls, live.active(name))
			}
		})
	}
}
