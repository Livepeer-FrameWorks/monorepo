package control

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func testLogger() logging.Logger { return logging.NewLogger() }

// resetPendingAcks isolates the package-global ack waiter map so the drain
// tests don't see each other's (or other suites') in-flight entries.
func resetPendingAcks(t *testing.T) {
	t.Helper()
	prev := pendingTriggerAcks
	pendingTriggerAcks = make(map[string]chan *ipcpb.MistTriggerAck)
	triggerRetryNotBefore.Lock()
	prevRetries := triggerRetryNotBefore.at
	triggerRetryNotBefore.at = make(map[string]time.Time)
	triggerRetryNotBefore.Unlock()
	t.Cleanup(func() {
		pendingTriggerAcks = prev
		triggerRetryNotBefore.Lock()
		triggerRetryNotBefore.at = prevRetries
		triggerRetryNotBefore.Unlock()
	})
}

// deliverAckAfterSend waits for the forwarder to put a control message on the
// wire, then routes the given ack back to the blocked pass keyed by the sent
// request_id. Returns the trigger that was sent.
func deliverAckAfterSend(t *testing.T, stream *fakeControlStream, success, retryable bool) *ipcpb.MistTrigger {
	t.Helper()
	sent := waitForControlMessage(t, stream.sendCh, "durable trigger send")
	trig := sent.GetMistTrigger()
	if trig == nil {
		t.Fatalf("expected MistTrigger payload, got %T", sent.GetPayload())
	}
	handleMistTriggerAck(&ipcpb.MistTriggerAck{
		RequestId: trig.GetRequestId(),
		Success:   success,
		Retryable: retryable,
	})
	return trig
}

// drainAsync runs one forwarder pass and returns a channel closed when it ends.
func drainAsync() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainTriggerWAL(testLogger())
	}()
	return done
}

// A retryable negative ack leaves the entry in the WAL and holds it back, so the next pass,
// started by the wakeup of an unrelated append, does not resend it at once.
func TestDrainTriggerWALRetryableAckKeepsAndDefersEntry(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFake(t)

	if _, err := wal.Append(&ipcpb.MistTrigger{RequestId: "evt-retry", TriggerType: "USER_END"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	done := drainAsync()
	deliverAckAfterSend(t, stream, false, true)
	waitForTestDone(t, done, "retryable ack")
	if depth, _ := wal.PendingDepth(); depth != 1 {
		t.Fatalf("retryable ack must keep the WAL entry: depth=%d", depth)
	}

	waitForTestDone(t, drainAsync(), "deferred pass")
	select {
	case msg := <-stream.sendCh:
		t.Fatalf("entry resent before its retry delay: %v", msg.GetMistTrigger().GetRequestId())
	default:
	}
}

// A non-retryable negative ack moves the entry to the dead-letter store so the forwarder stops
// re-sending a poison entry.
func TestDrainTriggerWALNonRetryableAckDeadLetters(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFake(t)

	if _, err := wal.Append(&ipcpb.MistTrigger{RequestId: "evt-poison", TriggerType: "USER_END"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	done := drainAsync()
	deliverAckAfterSend(t, stream, false, false)
	waitForTestDone(t, done, "non-retryable ack")
	if depth, _ := wal.PendingDepth(); depth != 0 {
		t.Fatalf("non-retryable ack must clear the pending entry: depth=%d", depth)
	}
}

func TestDrainTriggerWALNoStreamIsNoop(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	clearConn()

	if _, err := wal.Append(&ipcpb.MistTrigger{RequestId: "evt-stay", TriggerType: "USER_END"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	drainTriggerWAL(testLogger()) // no stream: must leave the entry on disk

	if depth, _ := wal.PendingDepth(); depth != 1 {
		t.Fatalf("drain with no stream must not touch the WAL: depth=%d", depth)
	}
}

// End-to-end drain: a pending entry is forwarded and truncated once Foghorn
// acks it.
func TestDrainTriggerWALForwardsAndTruncates(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFake(t)

	if _, err := wal.Append(&ipcpb.MistTrigger{RequestId: "evt-drain", TriggerType: "STREAM_END"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		drainTriggerWAL(testLogger())
	}()

	deliverAckAfterSend(t, stream, true, false)
	waitForTestDone(t, done, "drain")

	if depth, _ := wal.PendingDepth(); depth != 0 {
		t.Fatalf("drain should have truncated the acked entry: depth=%d", depth)
	}
}
