package triggers

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// A publisher that reconnects into a buffer Mist still holds FULL gets no
// STREAM_BUFFER edge. The periodic report of a playable buffer with the
// publisher's input connected marks the new session playable, which emits
// its stream.live.
func TestLifecycleReportMarksRepublishedSessionPlayable(t *testing.T) {
	const (
		tenantID = "11111111-2222-3333-4444-555555555555"
		streamID = "b3b1c1de-0000-4000-8000-000000000001"
		internal = "republished"
		nodeID   = "node-A"
	)
	sampled := time.Now().UnixMilli()
	report := func(playable bool, inputs uint32) *ipcpb.MistTrigger {
		return &ipcpb.MistTrigger{
			TriggerType: "STREAM_LIFECYCLE_UPDATE", NodeId: nodeID,
			TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				TenantId: proto.String(tenantID), InternalName: "live+" + internal, Status: "live", TotalInputs: proto.Uint32(inputs),
				BufferPlayable: proto.Bool(playable), BufferSampledUnixMillis: proto.Int64(sampled),
			}},
		}
	}
	for _, tc := range []struct {
		name     string
		playable bool
		inputs   uint32
		marks    bool
	}{
		{"playable buffer with the publisher connected", true, 1, true},
		{"buffer not playable", false, 1, false},
		{"no publisher input", true, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetStateTrigHandlers(t)
			mock := installControlDBForTest(t)
			p := minimalProcessorTrigHandlers(t)
			p.streamCache.Set(tenantID+":"+internal, streamContext{TenantID: tenantID, StreamID: streamID}, time.Minute)
			if tc.marks {
				mock.ExpectBegin()
				mock.ExpectQuery(`UPDATE foghorn\.ingest_sessions[\s\S]*SET playable_at = NOW\(\)`).
					WithArgs(sampled, tenantID, internal, nodeID).
					WillReturnRows(sqlmock.NewRows([]string{"session_id", "stream_id"}).AddRow("session-2", streamID))
				mock.ExpectExec(`INSERT INTO foghorn\.domain_event_outbox`).
					WithArgs(sqlmock.AnyArg(), "stream.live", "foghorn", "streams", sqlmock.AnyArg(), sqlmock.AnyArg(),
						"tenant", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			if _, _, err := p.handleStreamLifecycleUpdate(report(tc.playable, tc.inputs)); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("session playable mark: %v", err)
			}
		})
	}
}
