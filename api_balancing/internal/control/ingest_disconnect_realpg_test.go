//go:build schema_verify

package control

import (
	"database/sql"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func TestIngestDisconnectFencesPendingPublisher_RealPG(t *testing.T) {
	conn, _ := startRealPGWithDSN(t)
	testIngestDisconnectFencesPendingPublisher(t, conn)
}
func TestIngestDisconnectFencesPendingPublisher_RealYugabyte(t *testing.T) {
	conn, ok := dockerpg.OpenSharedYugabyteBaseline(t, "ingest_disconnect_fence", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testIngestDisconnectFencesPendingPublisher(t, conn)
}
func testIngestDisconnectFencesPendingPublisher(t *testing.T, conn *sql.DB) {
	repository := NewPublisherAdmissionRepository(conn)
	logger := logging.NewLogger()
	ctx := t.Context()
	request := IngestSessionRequest{TenantID: uuid.NewString(), StreamID: uuid.NewString(), NodeID: uuid.NewString(), InternalName: uuid.NewString(), TriggerUUID: uuid.NewString(), ConnectorPID: 123, StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell"}
	prepared, err := repository.Prepare(ctx, request, logger)
	if err != nil || prepared.Outcome != IngestSessionActive {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	if err := repository.FenceExecution(ctx, request, prepared.Generation); err != nil {
		t.Fatal(err)
	}
	registry := NewStreamRegistry(nil, "cell", time.Minute)
	if applied, _, err := repository.Project(ctx, registry, request, prepared, AdmissionEffectIntent{}); err == nil || applied {
		t.Fatalf("late publisher confirmation survived durable disconnect: applied=%v error=%v", applied, err)
	}
	if replay, err := repository.Prepare(ctx, request, logger); err != nil || replay.Outcome != IngestSessionAlreadyEnded {
		t.Fatalf("stopped execution admitted again: %+v %v", replay, err)
	}
	var stillReserved bool
	if err := conn.QueryRowContext(ctx, `SELECT ended_at IS NULL FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND id=$2::uuid`, request.TenantID, prepared.Generation).Scan(&stillReserved); err != nil || !stillReserved {
		t.Fatalf("unconfirmed stop released publisher capacity: reserved=%v error=%v", stillReserved, err)
	}
	if err := repository.CompleteStoppedExecution(ctx, request, prepared.Generation); err != nil {
		t.Fatal(err)
	}
	var idleData []byte
	if err := conn.QueryRowContext(ctx, `SELECT payload FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND aggregate_id=$2 AND event_type='stream.idle'`, request.TenantID, request.StreamID).Scan(&idleData); err != nil {
		t.Fatal(err)
	}
	var idle publicv1.StreamIdle
	if err := proto.Unmarshal(idleData, &idle); err != nil || idle.GetSessionId() != prepared.Generation {
		t.Fatalf("settlement lost old session ID: %v %v", &idle, err)
	}
	successor := request
	successor.TriggerUUID = uuid.NewString()
	successor.ConnectorPID++
	next, err := repository.Prepare(ctx, successor, logger)
	if err != nil || next.Outcome != IngestSessionActive {
		t.Fatalf("reconnect refused: %+v %v", next, err)
	}
	if err := repository.CompleteStoppedExecution(ctx, request, prepared.Generation); err != nil {
		t.Fatal(err)
	}
	if applied, _, err := repository.Project(ctx, registry, successor, next, AdmissionEffectIntent{}); err != nil || !applied {
		t.Fatalf("retry of old disconnect retired its successor: applied=%v error=%v", applied, err)
	}
}

func TestPublisherAdmissionEventsCarrySessionIDs_RealPG(t *testing.T) {
	conn, _ := startRealPGWithDSN(t)
	testPublisherAdmissionEventsCarrySessionIDs(t, conn)
}
func TestPublisherAdmissionEventsCarrySessionIDs_RealYugabyte(t *testing.T) {
	conn, ok := dockerpg.OpenSharedYugabyteBaseline(t, "publisher_session_events", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testPublisherAdmissionEventsCarrySessionIDs(t, conn)
}
func testPublisherAdmissionEventsCarrySessionIDs(t *testing.T, conn *sql.DB) {
	ctx := t.Context()
	repository := NewPublisherAdmissionRepository(conn)
	request := IngestSessionRequest{TenantID: uuid.NewString(), StreamID: uuid.NewString(), NodeID: uuid.NewString(), InternalName: uuid.NewString(), TriggerUUID: uuid.NewString(), ConnectorPID: 123, StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cell"}
	first, err := repository.Prepare(ctx, request, logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	request.TriggerUUID = uuid.NewString()
	request.StartedAtMillis++
	second, err := repository.Prepare(ctx, request, logging.NewLogger())
	if err != nil || second.Outcome != IngestSessionActive {
		t.Fatalf("PID reuse: %+v %v", second, err)
	}
	rows, err := conn.QueryContext(ctx, `SELECT event_type,payload FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND aggregate_id=$2`, request.TenantID, request.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type sessionEvent struct{ kind, session string }
	expected := map[sessionEvent]bool{
		{"stream.connected", first.Generation}:  false,
		{"stream.idle", first.Generation}:       false,
		{"stream.connected", second.Generation}: false,
	}
	index := 0
	for rows.Next() {
		var kind string
		var payload []byte
		if err := rows.Scan(&kind, &payload); err != nil {
			t.Fatal(err)
		}
		var session string
		switch kind {
		case "stream.connected":
			var msg publicv1.StreamConnected
			if err := proto.Unmarshal(payload, &msg); err != nil {
				t.Fatal(err)
			}
			session = msg.GetSessionId()
		case "stream.idle":
			var msg publicv1.StreamIdle
			if err := proto.Unmarshal(payload, &msg); err != nil {
				t.Fatal(err)
			}
			session = msg.GetSessionId()
		default:
			t.Fatalf("unexpected publisher event type %q", kind)
		}
		key := sessionEvent{kind, session}
		seen, exists := expected[key]
		if !exists || seen {
			t.Fatalf("unexpected or duplicate publisher event (%s) session=%q", kind, session)
		}
		expected[key] = true
		index++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != len(expected) {
		t.Fatalf("got %d events, want %d", index, len(expected))
	}
}
