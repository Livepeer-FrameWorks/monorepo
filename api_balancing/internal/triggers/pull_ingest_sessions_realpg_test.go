//go:build schema_verify

package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func TestStreamBufferPullMintsSessionEvents_RealPG(t *testing.T) {
	conn := startFoghornTriggersRealPG(t)
	previous := control.GetDB()
	control.SetDB(conn)
	t.Cleanup(func() { control.SetDB(previous) })
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	reg := installRegistryForTest(t)
	tenant, streamID := uuid.NewString(), uuid.NewString()
	const internal, node = "pull-buffer-events", "pull-buffer-node"
	reg.UpsertLocalSource(control.StreamEntry{TenantID: tenant, StreamID: streamID, InternalName: internal, IngestMode: control.IngestPull})
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.streamCache.Set(streamContextCacheKey("pull+"+internal), streamContext{TenantID: tenant, StreamID: streamID}, time.Minute)
	trigger := &ipcpb.MistTrigger{TriggerType: "STREAM_BUFFER", NodeId: node, TenantId: &tenant, StreamId: &streamID, TriggerUuid: uuid.NewString(), TriggerUnixMillis: time.Now().UnixMilli(), TriggerPayload: &ipcpb.MistTrigger_StreamBuffer{StreamBuffer: &ipcpb.StreamBufferTrigger{StreamName: "pull+" + internal, BufferState: "FULL", BufferPid: proto.Int64(4242)}}}
	if _, _, err := p.handleStreamBuffer(trigger); err != nil {
		t.Fatal(err)
	}
	var connected, live int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FILTER(WHERE event_type='stream.connected'), count(*) FILTER(WHERE event_type='stream.live') FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND aggregate_id=$2`, tenant, streamID).Scan(&connected, &live); err != nil {
		t.Fatal(err)
	}
	if connected != 1 || live != 1 {
		t.Fatalf("playable pull emitted connected=%d live=%d want 1/1", connected, live)
	}
	if _, _, err := p.handleStreamBuffer(trigger); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND aggregate_id=$2`, tenant, streamID).Scan(&total); err != nil || total != 2 {
		t.Fatalf("replayed FULL emitted extra events: count=%d err=%v", total, err)
	}
	endedAt := trigger.GetTriggerUnixMillis() + 100
	if _, _, err := p.handleStreamEnd(&ipcpb.MistTrigger{TriggerType: "STREAM_END", NodeId: node, TenantId: &tenant, StreamId: &streamID, TriggerUnixMillis: endedAt, TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "pull+" + internal, TriggerUnixMillis: endedAt}}}); err != nil {
		t.Fatal(err)
	}
	var idle int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND aggregate_id=$2 AND event_type='stream.idle'`, tenant, streamID).Scan(&idle); err != nil || idle != 1 {
		t.Fatalf("pull STREAM_END emitted idle=%d err=%v want 1", idle, err)
	}

}
