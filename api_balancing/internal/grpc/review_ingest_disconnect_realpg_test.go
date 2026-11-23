//go:build schema_verify

package grpc

import (
	"context"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"github.com/google/uuid"
	"testing"
)

func TestReviewNaturalEndDoesNotEmitPlatformDisconnect_RealPG(t *testing.T) {
	for _, reason := range []string{"requested", "key_rotated"} {
		t.Run(reason, func(t *testing.T) {
			conn := startFoghornGRPCRealPG(t)
			tenant, stream, generation, claim := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			_, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.ingest_sessions(id,tenant_id,node_id,stream_internal_name,connector_pid,start_trigger_uuid,started_at_unix_millis,stream_id,ended_at,ended_reason) VALUES($1::uuid,$2::uuid,'node','stream',123,$3,1,$4::uuid,NOW(),'stream_end')`, generation, tenant, claim, stream)
			if err != nil {
				t.Fatal(err)
			}
			server := &FoghornGRPCServer{db: conn, logger: logging.NewLogger()}
			request := &pb.TerminateStreamSessionsRequest{TenantId: tenant, StreamId: stream, StreamNames: []string{"stream"}, ClaimFences: map[string]string{"stream": claim}, Reason: reason}
			if _, err := server.TerminateClaimedStreamSessions(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "service"), request); err != nil {
				t.Fatal(err)
			}
			var events int
			if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND event_type='stream.ingest_disconnected'`, tenant).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if events != 0 {
				t.Fatalf("natural publisher end emitted %d platform-disconnected events for %s", events, reason)
			}
			if _, err := conn.ExecContext(t.Context(), `UPDATE foghorn.ingest_sessions SET ingest_disconnect_requested_at=ended_at+INTERVAL '1 second' WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, generation); err != nil {
				t.Fatal(err)
			}
			if _, err := server.TerminateClaimedStreamSessions(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "service"), request); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND event_type='stream.ingest_disconnected'`, tenant).Scan(&events); err != nil || events != 0 {
				t.Fatalf("a later stop claimed a naturally ended session: events=%d err=%v", events, err)
			}
		})
	}
}
