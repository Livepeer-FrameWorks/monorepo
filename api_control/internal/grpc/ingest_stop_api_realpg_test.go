//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	controlpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIngestStopDurability_RealPG(t *testing.T) {
	testIngestStopDurability(t, startCommodoreRealPG(t))
}
func TestIngestStopDurability_RealYugabyte(t *testing.T) {
	testIngestStopDurability(t, startPlacementDeliveryYugabyte(t, "ingest_stop_api"))
}

func testIngestStopDurability(t *testing.T, db *sql.DB) {
	tenant, user, stream, name := seedDeleteStopStream(t, db)
	ctx := ctxAs(user, tenant, "owner")
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_claim_id='old-claim',active_ingest_control_cell_id='delete-stop-cell' WHERE tenant_id=$1 AND id=$2`, tenant, stream); err != nil {
		t.Fatal(err)
	}
	live := &claimedIngestStopServer{unavailable: true}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
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
	response, err := server.DisconnectIngest(ctx, &pb.DisconnectIngestRequest{StreamId: stream})
	if err != nil || !response.GetStopRequested() {
		t.Fatalf("disconnect did not durably capture the active claim: %v %v", response, err)
	}
	var key string
	var payload []byte
	if err := db.QueryRowContext(ctx, `SELECT target_key,ingest_stop_payload FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND lane='ingest_stop'`, tenant).Scan(&key, &payload); err != nil {
		t.Fatal(err)
	}
	var captured ingestStopPayload
	if err := json.Unmarshal(payload, &captured); err != nil {
		t.Fatal(err)
	}
	if captured.ClaimID != "old-claim" || captured.StreamID != stream || captured.InternalName != name || captured.RefreshAuthority {
		t.Fatalf("wrong durable disconnect identity: %+v", captured)
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_claim_id='replacement' WHERE tenant_id=$1 AND id=$2`, tenant, stream); err != nil {
		t.Fatal(err)
	}
	server.processIngestStopObligations(ctx)
	row, found := readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneIngestStop, key)
	if !found || row.status != "pending" || row.attempts != 1 {
		t.Fatalf("failed stop lost its retry: %+v %v", row, found)
	}
	live.mu.Lock()
	live.unavailable = false
	live.mu.Unlock()
	if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_refresh_obligations SET next_attempt_at=NOW() WHERE tenant_id=$1 AND lane='ingest_stop' AND target_key=$2`, tenant, key); err != nil {
		t.Fatal(err)
	}
	server.processIngestStopObligations(ctx)
	row, found = readObligation(t, ctx, db, commodoredb.MediaAuthorityLaneIngestStop, key)
	if !found || row.status != "completed" {
		t.Fatalf("recovered stop did not converge: %+v %v", row, found)
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if len(live.claims) != 2 || live.claims[0] != "old-claim" || live.claims[1] != "old-claim" {
		t.Fatalf("retry recaptured replacement: %v", live.claims)
	}
	var current string
	if err := db.QueryRowContext(ctx, `SELECT active_ingest_claim_id FROM commodore.streams WHERE tenant_id=$1 AND id=$2`, tenant, stream).Scan(&current); err != nil || current != "replacement" {
		t.Fatalf("stop modified replacement claim: %s %v", current, err)
	}
}

type claimedIngestStopServer struct {
	foghornpb.UnimplementedTenantControlServiceServer
	mu          sync.Mutex
	unavailable bool
	claims      []string
}

func (s *claimedIngestStopServer) TerminateClaimedStreamSessions(_ context.Context, request *controlpb.TerminateStreamSessionsRequest) (*controlpb.TerminateTenantStreamsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = append(s.claims, request.GetClaimFences()[request.GetStreamNames()[0]])
	if s.unavailable {
		return nil, status.Error(codes.Unavailable, "temporary edge outage")
	}
	return &controlpb.TerminateTenantStreamsResponse{StreamsTerminated: 1, SessionsTerminated: 1}, nil
}

func TestRotateAndEndAtomicity_RealPG(t *testing.T) {
	testRotateAndEndAtomicity(t, startCommodoreRealPG(t))
}
func TestRotateAndEndAtomicity_RealYugabyte(t *testing.T) {
	testRotateAndEndAtomicity(t, startPlacementDeliveryYugabyte(t, "rotate_stop_atomic"))
}
func testRotateAndEndAtomicity(t *testing.T, db *sql.DB) {
	tenant, user, stream, _ := seedDeleteStopStream(t, db)
	ctx := ctxAs(user, tenant, "owner")
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_claim_id='old-claim',active_ingest_control_cell_id='delete-stop-cell' WHERE tenant_id=$1 AND id=$2`, tenant, stream); err != nil {
		t.Fatal(err)
	}
	server := newTenantFanoutTestServer(t, &clusterRoute{})
	server.db, server.tokenHasher = db, testTokenHasher(t)
	var old string
	if err := db.QueryRowContext(ctx, `SELECT stream_key FROM commodore.streams WHERE tenant_id=$1 AND id=$2`, tenant, stream).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION commodore.reject_ingest_stop_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.lane='ingest_stop' THEN RAISE EXCEPTION 'forced stop enqueue failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_ingest_stop_test BEFORE INSERT ON commodore.media_authority_refresh_obligations FOR EACH ROW EXECUTE FUNCTION commodore.reject_ingest_stop_test()`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.RefreshStreamKeyAndEndIngest(ctx, &pb.RefreshStreamKeyRequest{StreamId: stream}); err == nil {
		t.Fatal("rotation succeeded without its durable stop")
	}
	var after string
	if err := db.QueryRowContext(ctx, `SELECT stream_key FROM commodore.streams WHERE tenant_id=$1 AND id=$2`, tenant, stream).Scan(&after); err != nil || after != old {
		t.Fatalf("failed stop enqueue still rotated key: same=%v error=%v", after == old, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.domain_event_outbox WHERE tenant_id=$1 AND event_type='stream.key_rotated'`, tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed rotation published event: %d %v", count, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_ingest_stop_test ON commodore.media_authority_refresh_obligations`); err != nil {
		t.Fatal(err)
	}
	response, err := server.RefreshStreamKeyAndEndIngest(ctx, &pb.RefreshStreamKeyRequest{StreamId: stream})
	if err != nil || response.GetStreamKey() == old {
		t.Fatalf("rotation did not commit with its stop: %v %v", response, err)
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT ingest_stop_payload FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND lane='ingest_stop'`, tenant).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload ingestStopPayload
	if err := json.Unmarshal(raw, &payload); err != nil || !payload.RefreshAuthority || payload.ClaimID != "old-claim" {
		t.Fatalf("rotation stop lost its authority/claim fence: %+v %v", payload, err)
	}
}
