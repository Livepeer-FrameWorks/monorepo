package control

import (
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Helmsman keeps every terminal transition (processing results, DVR and sync
// completions, deletions, segment ledger reports, update and seed apply
// results) in a durable outbox and stamps each send with the row's
// durable_delivery_id. Foghorn opens every control stream with
// ControlCapabilities{durable_delivery_acks}, and answers each stamped message
// with a DurableDeliveryAck once its handler has returned:
//
//   - success: the effect committed, the outcome is deterministic (a rejected,
//     stale or duplicate report that a resend would only reject again), or the
//     remaining retry belongs to Foghorn's own durable state (a thumbnail or
//     sync attempt left for its recovery reconciler, a chapter finalize attempt).
//   - failure: the handler's durable write did not commit. Helmsman returns the
//     row to pending and resends it after a delay.
//
// A Foghorn that dies before acking leaves the row in flight on Helmsman,
// which resends it to the next instance under the same id. Every handler is
// replay-safe against its own durable state (attempt and owner fences, guarded
// status transitions), which is what makes a resend that reaches another
// instance harmless. Within one instance, durableDeliveries also collapses
// concurrent and recently acknowledged resends of the same row so a slow
// handler is not run twice.
//
// A Helmsman that predates acks sends no delivery id and receives no ack; it
// ignores the ControlCapabilities message.

const (
	// durableDeliverySettledTTL bounds how long an acknowledged delivery is
	// answered from memory; a resend after that runs its replay-safe handler.
	durableDeliverySettledTTL = 10 * time.Minute
	// durableDeliverySettledCap bounds the acknowledged-delivery memory.
	durableDeliverySettledCap = 16384
)

var durableDeliveries = newDurableDeliveryTracker()

type durableDeliveryTracker struct {
	mu       sync.Mutex
	inflight map[string]*durableDeliveryRun
	settled  map[string]time.Time
	now      func() time.Time
}

type durableDeliveryRun struct {
	waiters []ipcpb.HelmsmanControl_ConnectServer
}

func newDurableDeliveryTracker() *durableDeliveryTracker {
	return &durableDeliveryTracker{
		inflight: make(map[string]*durableDeliveryRun),
		settled:  make(map[string]time.Time),
		now:      time.Now,
	}
}

// announceControlCapabilities is the first message on every control stream.
// Helmsman settles its delivery contract from the first message it receives,
// so nothing may be sent on the stream before it.
func announceControlCapabilities(stream ipcpb.HelmsmanControl_ConnectServer) error {
	return stream.Send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ControlCapabilities{
		ControlCapabilities: &ipcpb.ControlCapabilities{DurableDeliveryAcks: true},
	}})
}

// dispatchDurableDelivery runs handle in its own goroutine and acknowledges
// deliveryID on stream once it returns. Without a delivery id (an older
// Helmsman) it only runs handle.
func dispatchDurableDelivery(stream ipcpb.HelmsmanControl_ConnectServer, nodeID, deliveryID string, handle func() error) {
	if deliveryID == "" {
		go func() {
			// The handler logged the failure; without a delivery id there is
			// no row to send again.
			if err := handle(); err != nil {
				controlLogger().WithError(err).WithField("node_id", nodeID).Debug("Control message from a Helmsman without delivery acks did not commit")
			}
		}()
		return
	}
	go durableDeliveries.run(stream, nodeID, deliveryID, handle)
}

func (t *durableDeliveryTracker) run(stream ipcpb.HelmsmanControl_ConnectServer, nodeID, deliveryID string, handle func() error) {
	// Delivery ids are unique per Helmsman outbox, not across nodes.
	key := nodeID + "\x00" + deliveryID
	t.mu.Lock()
	if at, ok := t.settled[key]; ok && t.now().Sub(at) < durableDeliverySettledTTL {
		t.mu.Unlock()
		sendDurableDeliveryAck(stream, deliveryID, nil)
		return
	}
	if run, ok := t.inflight[key]; ok {
		run.waiters = append(run.waiters, stream)
		t.mu.Unlock()
		return
	}
	run := &durableDeliveryRun{waiters: []ipcpb.HelmsmanControl_ConnectServer{stream}}
	t.inflight[key] = run
	t.mu.Unlock()

	err := handle()

	t.mu.Lock()
	delete(t.inflight, key)
	if err == nil {
		t.settleLocked(key)
	}
	waiters := run.waiters
	t.mu.Unlock()
	for _, waiter := range waiters {
		sendDurableDeliveryAck(waiter, deliveryID, err)
	}
}

func (t *durableDeliveryTracker) settleLocked(key string) {
	now := t.now()
	if len(t.settled) >= durableDeliverySettledCap {
		for k, at := range t.settled {
			if now.Sub(at) >= durableDeliverySettledTTL {
				delete(t.settled, k)
			}
		}
		// Every entry is still fresh: forget arbitrary ones. A forgotten
		// delivery that is resent runs its replay-safe handler again.
		for k := range t.settled {
			if len(t.settled) < durableDeliverySettledCap {
				break
			}
			delete(t.settled, k)
		}
	}
	t.settled[key] = now
}

func sendDurableDeliveryAck(stream ipcpb.HelmsmanControl_ConnectServer, deliveryID string, handleErr error) {
	ack := &ipcpb.DurableDeliveryAck{DeliveryId: deliveryID, Success: handleErr == nil}
	if handleErr != nil {
		ack.Error = handleErr.Error()
	}
	if err := stream.Send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DurableDeliveryAck{DurableDeliveryAck: ack}}); err != nil {
		// Helmsman resends the row on its next connection.
		controlLogger().WithError(err).WithFields(logging.Fields{
			"delivery_id": deliveryID,
			"success":     ack.GetSuccess(),
		}).Debug("Could not send durable delivery ack")
	}
}
