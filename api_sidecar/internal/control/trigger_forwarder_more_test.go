package control

import (
	"errors"
	"testing"

	"frameworks/api_sidecar/internal/storage"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// withTestTriggerWAL points the package-global durable WAL at a temp dir and
// marks the forwarder started, restoring both on cleanup so the durability
// state doesn't leak across tests.
func withTestTriggerWAL(t *testing.T) *storage.TriggerWAL {
	t.Helper()
	wal, err := storage.NewTriggerWAL(t.TempDir())
	if err != nil {
		t.Fatalf("NewTriggerWAL: %v", err)
	}
	prevWAL := triggerWAL
	prevStarted := triggerForwarderStarted.Load()
	triggerWAL = wal
	triggerForwarderStarted.Store(true)
	t.Cleanup(func() {
		triggerWAL = prevWAL
		triggerForwarderStarted.Store(prevStarted)
	})
	return wal
}

// SendDurableMistTrigger must refuse before the forwarder is ready, persist a
// fresh trigger, and collapse a re-delivered trigger (same RequestId) so each
// source event is forwarded at most once.
func TestSendDurableMistTrigger(t *testing.T) {
	t.Run("unready forwarder refuses", func(t *testing.T) {
		prevWAL := triggerWAL
		prevStarted := triggerForwarderStarted.Load()
		triggerWAL = nil
		triggerForwarderStarted.Store(false)
		t.Cleanup(func() {
			triggerWAL = prevWAL
			triggerForwarderStarted.Store(prevStarted)
		})
		err := SendDurableMistTrigger(&ipcpb.MistTrigger{RequestId: "evt-1", TriggerType: "USER_END"})
		if !errors.Is(err, errTriggerForwarderUnready) {
			t.Fatalf("expected errTriggerForwarderUnready, got %v", err)
		}
	})

	t.Run("fresh persists, duplicate collapses", func(t *testing.T) {
		wal := withTestTriggerWAL(t)
		trig := &ipcpb.MistTrigger{RequestId: "evt-dup", TriggerType: "USER_END"}

		if err := SendDurableMistTrigger(trig); err != nil {
			t.Fatalf("first send: %v", err)
		}
		if depth, _ := wal.PendingDepth(); depth != 1 {
			t.Fatalf("pending depth after first send = %d, want 1", depth)
		}

		// Same RequestId again — at-most-once collision on disk.
		if err := SendDurableMistTrigger(trig); err != nil {
			t.Fatalf("duplicate send: %v", err)
		}
		if depth, _ := wal.PendingDepth(); depth != 1 {
			t.Fatalf("pending depth after duplicate = %d, want 1", depth)
		}
	})
}

// updateTriggerWALDepthGauge must be a safe no-op when no WAL is open.
func TestUpdateTriggerWALDepthGauge(t *testing.T) {
	prevWAL := triggerWAL
	triggerWAL = nil
	t.Cleanup(func() { triggerWAL = prevWAL })
	updateTriggerWALDepthGauge() // must not panic

	withTestTriggerWAL(t)
	updateTriggerWALDepthGauge() // with a WAL open: still must not panic
}

// handleMistTriggerAck routes an ack to the forwarder pass blocked on its
// RequestId, drops acks with no waiter, and never blocks on a full channel.
func TestHandleMistTriggerAck(t *testing.T) {
	prev := pendingTriggerAcks
	pendingTriggerAcks = make(map[string]chan *ipcpb.MistTriggerAck)
	t.Cleanup(func() { pendingTriggerAcks = prev })

	t.Run("nil ack is a no-op", func(t *testing.T) {
		handleMistTriggerAck(nil)
	})

	t.Run("unknown request id is dropped", func(t *testing.T) {
		handleMistTriggerAck(&ipcpb.MistTriggerAck{RequestId: "nobody-waiting"})
	})

	t.Run("registered waiter receives the ack", func(t *testing.T) {
		ch := make(chan *ipcpb.MistTriggerAck, 1)
		pendingTriggerAcksMu.Lock()
		pendingTriggerAcks["evt-ack"] = ch
		pendingTriggerAcksMu.Unlock()

		ack := &ipcpb.MistTriggerAck{RequestId: "evt-ack"}
		handleMistTriggerAck(ack)
		select {
		case got := <-ch:
			if got != ack {
				t.Fatalf("received %v, want %v", got, ack)
			}
		default:
			t.Fatal("waiter did not receive the ack")
		}

		// Channel now full (cap 1, already drained but refill): a second ack
		// with the buffer full must not block.
		ch <- &ipcpb.MistTriggerAck{RequestId: "evt-ack"}
		handleMistTriggerAck(&ipcpb.MistTriggerAck{RequestId: "evt-ack"})
	})
}
