package control

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// ackRecordingStream records what Foghorn sends, safe against the handler
// goroutines that acknowledge deliveries.
type ackRecordingStream struct {
	*registerOnceStream
	mu   sync.Mutex
	out  []*ipcpb.ControlMessage
	acks chan *ipcpb.DurableDeliveryAck
}

func newAckRecordingStream(msgs []*ipcpb.ControlMessage) *ackRecordingStream {
	return &ackRecordingStream{
		registerOnceStream: &registerOnceStream{msgs: msgs},
		acks:               make(chan *ipcpb.DurableDeliveryAck, 16),
	}
}

func (s *ackRecordingStream) Send(msg *ipcpb.ControlMessage) error {
	s.mu.Lock()
	s.out = append(s.out, msg)
	s.mu.Unlock()
	if ack := msg.GetDurableDeliveryAck(); ack != nil {
		s.acks <- ack
	}
	return nil
}

func (s *ackRecordingStream) first() *ipcpb.ControlMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.out) == 0 {
		return nil
	}
	return s.out[0]
}

func awaitAcks(t *testing.T, acks <-chan *ipcpb.DurableDeliveryAck, n int) map[string]*ipcpb.DurableDeliveryAck {
	t.Helper()
	got := make(map[string]*ipcpb.DurableDeliveryAck)
	for len(got) < n {
		select {
		case ack := <-acks:
			got[ack.GetDeliveryId()] = ack
		case <-time.After(5 * time.Second):
			t.Fatalf("received %d of %d durable delivery acks: %v", len(got), n, got)
		}
	}
	return got
}

// Foghorn opens the stream with its capabilities and acknowledges each
// durable delivery with its handler's outcome: a delivery whose effect did
// not commit is refused so Helmsman sends it again.
func TestConnectAcknowledgesDurableDeliveriesWithHandlerOutcome(t *testing.T) {
	ensureRegistry(t)
	durableDeliveries = newDurableDeliveryTracker()
	t.Cleanup(func() { durableDeliveries = newDurableDeliveryTracker() })
	stubFingerprintResolves(t, "node-ack", "tenant-a")

	store, _ := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "inst-self", "10.0.0.1:9090", &mockRelayPool{}))
	sm := state.ResetDefaultManagerForTests()
	if err := sm.EnableRedisSync(context.Background(), store, "inst-self", logging.NewLogger()); err != nil {
		t.Fatalf("EnableRedisSync: %v", err)
	}
	t.Cleanup(func() { sm.Shutdown() })
	previousSchedule := scheduleReconnectPushTargetRearmFn
	scheduleReconnectPushTargetRearmFn = func(string, int64, string, logging.Logger) {}
	t.Cleanup(func() { scheduleReconnectPushTargetRearmFn = previousSchedule })

	handled := make(chan struct{}, 3)
	prevH := artifactDeletedHandler
	artifactDeletedHandler = func(_ context.Context, del *ipcpb.ArtifactDeleted) error {
		defer func() { handled <- struct{}{} }()
		if del.GetArtifactHash() == "uncommitted" {
			return errors.New("database unavailable")
		}
		return nil
	}
	t.Cleanup(func() { artifactDeletedHandler = prevH })

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB; mockDB.Close() })
	mock.ExpectQuery(`INSERT INTO foghorn.node_control_fence_counter`).
		WithArgs("node-ack").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(3)))
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO foghorn.node_config_seeds`).
		WithArgs("node-ack").
		WillReturnRows(sqlmock.NewRows([]string{"version_counter"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT COALESCE\(seed_version, 0\)::bigint AS seed_version, seed_payload`).
		WithArgs("node-ack").
		WillReturnRows(sqlmock.NewRows([]string{"seed_version", "seed_payload"}))
	mock.ExpectExec(`UPDATE foghorn.node_config_seeds`).
		WithArgs(int64(1), sqlmock.AnyArg(), "node-ack").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	deleted := func(id, hash string) *ipcpb.ControlMessage {
		return &ipcpb.ControlMessage{DurableDeliveryId: id, Payload: &ipcpb.ControlMessage_ArtifactDeleted{
			ArtifactDeleted: &ipcpb.ArtifactDeleted{ArtifactHash: hash, Reason: "evict"},
		}}
	}
	stream := newAckRecordingStream([]*ipcpb.ControlMessage{
		{Payload: &ipcpb.ControlMessage_Register{Register: &ipcpb.Register{NodeId: "node-ack", ControlProtocolVersion: RestreamAttemptFenceProtocolMin}}},
		deleted("row-committed", "committed"),
		deleted("row-uncommitted", "uncommitted"),
		// A Helmsman that predates acks: handled, never acknowledged.
		deleted("", "committed"),
	})
	_ = (&Server{}).Connect(stream)
	for range 3 {
		select {
		case <-handled:
		case <-time.After(5 * time.Second):
			t.Fatal("not every artifact deletion was handled")
		}
	}

	if caps := stream.first().GetControlCapabilities(); !caps.GetDurableDeliveryAcks() {
		t.Fatalf("first message on the stream = %v, want ControlCapabilities with durable delivery acks", stream.first())
	}
	acks := awaitAcks(t, stream.acks, 2)
	if ack := acks["row-committed"]; !ack.GetSuccess() {
		t.Fatalf("committed delivery ack = %+v, want success", ack)
	}
	if ack := acks["row-uncommitted"]; ack.GetSuccess() || ack.GetError() == "" {
		t.Fatalf("uncommitted delivery ack = %+v, want a refusal with the error", ack)
	}
	select {
	case extra := <-stream.acks:
		t.Fatalf("unexpected ack %+v for a delivery without an id", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

func durableAckOn(t *testing.T, stream *ackRecordingStream) *ipcpb.DurableDeliveryAck {
	t.Helper()
	select {
	case ack := <-stream.acks:
		return ack
	case <-time.After(5 * time.Second):
		t.Fatal("no durable delivery ack")
		return nil
	}
}

// A resend of a row that is still being handled, or was acknowledged a moment
// ago, is answered from the first handling instead of running it again.
func TestDurableDeliveryResendsAreDeduplicatedPerNode(t *testing.T) {
	tracker := newDurableDeliveryTracker()
	now := time.Unix(1_000_000, 0)
	tracker.now = func() time.Time { return now }

	var runs atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	handle := func() error {
		runs.Add(1)
		started <- struct{}{}
		<-release
		return nil
	}

	first := newAckRecordingStream(nil)
	resent := newAckRecordingStream(nil)
	go tracker.run(first, "node-a", "row-1", handle)
	<-started
	tracker.run(resent, "node-a", "row-1", handle)
	close(release)
	if ack := durableAckOn(t, first); !ack.GetSuccess() || ack.GetDeliveryId() != "row-1" {
		t.Fatalf("first stream ack = %+v", ack)
	}
	if ack := durableAckOn(t, resent); !ack.GetSuccess() || ack.GetDeliveryId() != "row-1" {
		t.Fatalf("resend stream ack = %+v", ack)
	}

	late := newAckRecordingStream(nil)
	tracker.run(late, "node-a", "row-1", handle)
	if ack := durableAckOn(t, late); !ack.GetSuccess() {
		t.Fatalf("late resend ack = %+v", ack)
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("handler ran %d times for one row, want 1", n)
	}

	other := newAckRecordingStream(nil)
	tracker.run(other, "node-b", "row-1", handle)
	<-started
	if n := runs.Load(); n != 2 {
		t.Fatalf("the same delivery id from another node ran the handler %d times in total, want 2", n)
	}
	durableAckOn(t, other)

	now = now.Add(durableDeliverySettledTTL)
	expired := newAckRecordingStream(nil)
	tracker.run(expired, "node-a", "row-1", handle)
	<-started
	durableAckOn(t, expired)
	if n := runs.Load(); n != 3 {
		t.Fatalf("a resend after the settled window ran the handler %d times in total, want 3", n)
	}
}

// A refused delivery is not remembered: its resend runs the handler again.
func TestRefusedDurableDeliveryRunsAgainOnResend(t *testing.T) {
	tracker := newDurableDeliveryTracker()
	var runs atomic.Int32
	handle := func() error {
		if runs.Add(1) == 1 {
			return errors.New("database unavailable")
		}
		return nil
	}
	stream := newAckRecordingStream(nil)
	tracker.run(stream, "node-a", "row-1", handle)
	if ack := durableAckOn(t, stream); ack.GetSuccess() {
		t.Fatalf("first ack = %+v, want refusal", ack)
	}
	tracker.run(stream, "node-a", "row-1", handle)
	if ack := durableAckOn(t, stream); !ack.GetSuccess() {
		t.Fatalf("resend ack = %+v, want success", ack)
	}
	if n := runs.Load(); n != 2 {
		t.Fatalf("handler ran %d times, want 2", n)
	}
}
