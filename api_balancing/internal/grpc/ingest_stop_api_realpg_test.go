//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestClaimedIngestStopEventIsDurableOnce_RealPG(t *testing.T) {
	testClaimedIngestStopEventIsDurableOnce(t, startFoghornGRPCRealPG(t))
}
func TestClaimedIngestStopEventIsDurableOnce_RealYugabyte(t *testing.T) {
	conn, ok := dockerpg.OpenSharedYugabyteBaseline(t, "claimed_ingest_stop_event", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testClaimedIngestStopEventIsDurableOnce(t, conn)
}
func testClaimedIngestStopEventIsDurableOnce(t *testing.T, conn *sql.DB) {
	tenant, stream, generation := uuid.NewString(), uuid.NewString(), uuid.NewString()
	name, claim := uuid.NewString(), uuid.NewString()
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "service")
	if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.ingest_sessions(id,tenant_id,node_id,stream_internal_name,connector_pid,start_trigger_uuid,started_at_unix_millis,stream_id,ended_at,ended_reason,ingest_disconnect_requested_at) VALUES($1::uuid,$2::uuid,'old-node',$3,123,$4,(EXTRACT(EPOCH FROM NOW())*1000)::bigint,$5::uuid,NOW(),'admission_abandoned',NOW())`, generation, tenant, name, claim, stream); err != nil {
		t.Fatal(err)
	}
	server := &FoghornGRPCServer{db: conn, logger: logging.NewLogger()}
	request := &pb.TerminateStreamSessionsRequest{TenantId: tenant, StreamId: stream, StreamNames: []string{name}, ClaimFences: map[string]string{name: claim}, Reason: "requested"}
	if _, err := conn.ExecContext(ctx, `CREATE FUNCTION foghorn.reject_ingest_disconnected_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='stream.ingest_disconnected' THEN RAISE EXCEPTION 'forced event outage'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_ingest_disconnected_test BEFORE INSERT ON foghorn.domain_event_outbox FOR EACH ROW EXECUTE FUNCTION foghorn.reject_ingest_disconnected_test()`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.TerminateClaimedStreamSessions(ctx, request); status.Code(err) != codes.Unavailable {
		t.Fatalf("event failure was acknowledged: %v", err)
	}
	var marked bool
	if err := conn.QueryRowContext(ctx, `SELECT ingest_disconnected_at IS NOT NULL FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, generation).Scan(&marked); err != nil || marked {
		t.Fatalf("event failure persisted completion: %v %v", marked, err)
	}
	if _, err := conn.ExecContext(ctx, `DROP TRIGGER reject_ingest_disconnected_test ON foghorn.domain_event_outbox`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := server.TerminateClaimedStreamSessions(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var payload []byte
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND event_type='stream.ingest_disconnected'`, tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry duplicated or lost event: %d %v", count, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT payload FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND event_type='stream.ingest_disconnected'`, tenant).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event publicv1.StreamIngestDisconnected
	if err := proto.Unmarshal(payload, &event); err != nil || event.GetSessionId() != generation || event.GetStreamId() != stream || event.GetReason() != "requested" {
		t.Fatalf("disconnect event lost exact identity: %v %v", &event, err)
	}
}
