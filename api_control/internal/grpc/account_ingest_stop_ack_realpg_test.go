//go:build schema_verify

package grpc

import (
	"database/sql"
	"net"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

func TestAccountIngestRevocationWorkerRequiresConfirmedStop_RealPG(t *testing.T) {
	testAccountIngestRevocationWorkerRequiresConfirmedStop(t, startCommodoreRealPG(t))
}

func TestAccountIngestRevocationWorkerRequiresConfirmedStop_RealYugabyte(t *testing.T) {
	testAccountIngestRevocationWorkerRequiresConfirmedStop(t, startPlacementDeliveryYugabyte(t, "account_stop_ack"))
}

func testAccountIngestRevocationWorkerRequiresConfirmedStop(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	tenant, user, stream := uuid.NewString(), uuid.NewString(), uuid.NewString()
	name := "pending-stop-" + stream
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active) VALUES($1,$2,$3,false)`, user, tenant, user+"@stop.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,internal_name,stream_key,playback_id,title,active_ingest_cluster_id) VALUES($1,$2,$3,$4::text,$4::text,$4::text,'Pending verified stop','account-cell')`, stream, tenant, user, name); err != nil {
		t.Fatal(err)
	}
	live := &liveAccountIngest{live: map[string]bool{name: true}, unavailable: true}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	foghornpb.RegisterTenantControlServiceServer(rpc, live)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(rpc.Stop)
	server := newTenantFanoutTestServer(t, &clusterRoute{})
	server.db, server.authorityTenantSource = db, operatorAccountTenantSource{}
	server.foghornDiscovery.Store("account-cell", &foghornDiscoveryEntry{addrs: []string{listener.Addr().String()}, at: time.Now()})
	target := commodoredb.LiveStreamMediaAuthorityTarget(stream)
	if err := commodoredb.New(db).EnqueueMediaAuthorityObligation(ctx, commodoredb.EnqueueMediaAuthorityObligationParams{
		Lane: commodoredb.MediaAuthorityLaneIngestRevocation, TargetKey: target.Key,
		TargetKind: target.Kind, TenantID: tenant, Reason: sharedauthority.ReasonAccountInactive,
		SourceService: "commodore", SourceEventID: "stop-proof-" + stream,
	}); err != nil {
		t.Fatal(err)
	}
	server.processAccountIngestRevocations(ctx)
	row, found := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneIngestRevocation, target.Key)
	if !found || row.status != "pending" || row.attempts != 1 || row.leased || !live.active(name) {
		t.Fatalf("unconfirmed stop completed durable work: row=%+v found=%v active=%v", row, found, live.active(name))
	}
	var retainedError bool
	if err := db.QueryRowContext(ctx, `SELECT last_error IS NOT NULL FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND lane=$2 AND target_key=$3`, tenant, commodoredb.MediaAuthorityLaneIngestRevocation, target.Key).Scan(&retainedError); err != nil || !retainedError {
		t.Fatalf("unconfirmed stop lost its durable failure: retained=%v err=%v", retainedError, err)
	}
	live.recover()
	if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_refresh_obligations SET next_attempt_at=NOW() WHERE tenant_id=$1 AND lane=$2 AND target_key=$3 AND status='pending'`, tenant, commodoredb.MediaAuthorityLaneIngestRevocation, target.Key); err != nil {
		t.Fatal(err)
	}
	server.processAccountIngestRevocations(ctx)
	row, found = readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneIngestRevocation, target.Key)
	if !found || row.status != "completed" || row.attempts != 2 || row.leased || live.active(name) {
		t.Fatalf("confirmed retry did not complete exact durable stop: row=%+v found=%v active=%v", row, found, live.active(name))
	}
}
