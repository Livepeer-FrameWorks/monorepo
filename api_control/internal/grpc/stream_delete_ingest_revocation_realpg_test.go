//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"net"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

func seedDeleteStopStream(t *testing.T, db *sql.DB) (string, string, string, string) {
	t.Helper()
	tenant, user, stream := uuid.NewString(), uuid.NewString(), uuid.NewString()
	name := "delete-stop-" + stream
	if _, err := db.ExecContext(t.Context(), `INSERT INTO commodore.users(id,tenant_id,email,is_active) VALUES($1,$2,$3,true)`, user, tenant, user+"@delete-stop.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO commodore.streams(id,tenant_id,user_id,internal_name,stream_key,playback_id,title,active_ingest_cluster_id) VALUES($1,$2,$3,$4::text,$4::text,$4::text,'Delete verified stop','delete-stop-cell')`, stream, tenant, user, name); err != nil {
		t.Fatal(err)
	}
	return tenant, user, stream, name
}

func TestDeleteStreamWaitsForDurableIngestRevocation_RealPG(t *testing.T) {
	testDeleteStreamWaitsForDurableIngestRevocation(t, startCommodoreRealPG(t))
}

func TestDeleteStreamWaitsForDurableIngestRevocation_RealYugabyte(t *testing.T) {
	testDeleteStreamWaitsForDurableIngestRevocation(t, startPlacementDeliveryYugabyte(t, "delete_ingest_stop"))
}

func testDeleteStreamWaitsForDurableIngestRevocation(t *testing.T, db *sql.DB) {
	t.Helper()
	tenant, user, stream, name := seedDeleteStopStream(t, db)
	live := &liveAccountIngest{live: map[string]bool{name: true}, unavailable: true}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	foghornpb.RegisterTenantControlServiceServer(rpc, live)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(rpc.Stop)
	server := newTenantFanoutTestServer(t, &clusterRoute{})
	server.db, server.tokenHasher = db, testTokenHasher(t)
	server.foghornDiscovery.Store("delete-stop-cell", &foghornDiscoveryEntry{addrs: []string{listener.Addr().String()}, at: time.Now()})
	server.streamThumbnailDeleteFn = func(context.Context, string, string, string) error { return nil }
	response, err := server.DeleteStream(ctxAs(user, tenant, "owner"), &commodorepb.DeleteStreamRequest{StreamId: stream})
	if err != nil {
		t.Fatal(err)
	}
	target := commodoredb.LiveStreamMediaAuthorityTarget(stream)
	row, found := readObligation(t, t.Context(), db, commodoredb.MediaAuthorityLaneIngestRevocation, target.Key)
	if !found || row.status != "pending" {
		t.Fatalf("deleteStream did not persist a pending ingest stop: response=%v row=%+v found=%v", response, row, found)
	}
	var softDeleted bool
	if err := db.QueryRowContext(t.Context(), `SELECT deleted_at IS NOT NULL FROM commodore.streams WHERE tenant_id=$1 AND id=$2`, tenant, stream).Scan(&softDeleted); err != nil || !softDeleted {
		t.Fatalf("stream identity must survive until the publisher is stopped: soft_deleted=%v err=%v", softDeleted, err)
	}
	server.processAccountIngestRevocations(t.Context())
	row, found = readObligation(t, t.Context(), db, commodoredb.MediaAuthorityLaneIngestRevocation, target.Key)
	if !found || row.status != "pending" || row.attempts != 1 || !live.active(name) {
		t.Fatalf("failed stop was not retained for retry: row=%+v found=%v publisher_live=%v", row, found, live.active(name))
	}
	if err := server.finalizeStreamDeletion(t.Context(), stream, tenant, ""); err == nil {
		t.Fatal("stream deletion finalized while the durable publisher stop was pending")
	}
	live.recover()
	if _, err := db.ExecContext(t.Context(), `UPDATE commodore.media_authority_refresh_obligations SET next_attempt_at=NOW() WHERE tenant_id=$1 AND lane=$2 AND target_key=$3 AND status='pending'`, tenant, commodoredb.MediaAuthorityLaneIngestRevocation, target.Key); err != nil {
		t.Fatal(err)
	}
	server.processAccountIngestRevocations(t.Context())
	row, found = readObligation(t, t.Context(), db, commodoredb.MediaAuthorityLaneIngestRevocation, target.Key)
	if !found || row.status != "completed" || live.active(name) {
		t.Fatalf("confirmed retry did not stop the deleted stream: row=%+v found=%v publisher_live=%v", row, found, live.active(name))
	}
	if err := server.finalizeStreamDeletion(t.Context(), stream, tenant, ""); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM commodore.streams WHERE tenant_id=$1 AND id=$2`, tenant, stream).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("confirmed stop did not permit final deletion: remaining=%d err=%v", remaining, err)
	}
}

func TestDeleteStreamRevocationFailureRollsBackDeletion_RealPG(t *testing.T) {
	testDeleteStreamRevocationFailureRollsBackDeletion(t, startCommodoreRealPG(t))
}

func TestDeleteStreamRevocationFailureRollsBackDeletion_RealYugabyte(t *testing.T) {
	testDeleteStreamRevocationFailureRollsBackDeletion(t, startPlacementDeliveryYugabyte(t, "delete_stop_rollback"))
}

func testDeleteStreamRevocationFailureRollsBackDeletion(t *testing.T, db *sql.DB) {
	t.Helper()
	tenant, user, stream, _ := seedDeleteStopStream(t, db)
	if _, err := db.ExecContext(t.Context(), `CREATE FUNCTION commodore.reject_delete_stop_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.lane='ingest_revocation' THEN RAISE EXCEPTION 'forced revocation enqueue failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER reject_delete_stop_test BEFORE INSERT ON commodore.media_authority_refresh_obligations FOR EACH ROW EXECUTE FUNCTION commodore.reject_delete_stop_test()`); err != nil {
		t.Fatal(err)
	}
	server := newTenantFanoutTestServer(t, &clusterRoute{})
	server.db, server.tokenHasher = db, testTokenHasher(t)
	server.streamThumbnailDeleteFn = func(context.Context, string, string, string) error { return nil }
	if _, err := server.DeleteStream(ctxAs(user, tenant, "owner"), &commodorepb.DeleteStreamRequest{StreamId: stream}); err == nil {
		t.Fatal("deleteStream succeeded without committing its required ingest stop")
	}
	var untouched bool
	if err := db.QueryRowContext(t.Context(), `SELECT deleted_at IS NULL FROM commodore.streams WHERE tenant_id=$1 AND id=$2`, tenant, stream).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("failed revocation enqueue did not roll back soft deletion: untouched=%v err=%v", untouched, err)
	}
	var queued int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM commodore.stream_cleanup_outbox WHERE tenant_id=$1 AND stream_id=$2`, tenant, stream).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("failed deletion left a cleanup obligation: queued=%d err=%v", queued, err)
	}
}
