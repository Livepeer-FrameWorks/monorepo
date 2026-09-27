package control

import (
	"errors"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/prometheus/client_golang/prometheus"
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

// The oldest-pending age of each lane is exported at scrape time, so it keeps growing while
// nothing drains.
func TestTriggerWALOldestPendingAgeGauge(t *testing.T) {
	wal := withTestTriggerWAL(t)
	gathered := func(lane string) float64 {
		t.Helper()
		families, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() != "helmsman_trigger_wal_oldest_pending_age_seconds" {
				continue
			}
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "lane" && label.GetValue() == lane {
						return metric.GetGauge().GetValue()
					}
				}
			}
		}
		t.Fatalf("no oldest-pending age for lane %s", lane)
		return 0
	}
	if age := gathered("lifecycle"); age != 0 {
		t.Fatalf("empty lane age = %v, want 0", age)
	}
	stuckSince := time.Now().Add(-7 * time.Minute)
	if _, err := wal.Append(&ipcpb.MistTrigger{RequestId: "stuck-user-end", TriggerType: "USER_END", Timestamp: stuckSince.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := wal.Append(&ipcpb.MistTrigger{RequestId: "fresh-sample", TriggerType: "PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", Timestamp: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if age := gathered("lifecycle"); age < 420 || age > 430 {
		t.Fatalf("lifecycle lane age = %v, want about 420s", age)
	}
	if age := gathered("sample"); age > 10 {
		t.Fatalf("sample lane age = %v, want near 0", age)
	}
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
