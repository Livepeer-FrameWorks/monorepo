package control

import (
	"testing"

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
	t.Cleanup(func() { pendingTriggerAcks = prev })
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

func TestSendDurableTriggerAndAwaitAckEmptyRequestID(t *testing.T) {
	// No request_id means we can't correlate an ack — refuse without sending.
	if sendDurableTriggerAndAwaitAck(&ipcpb.MistTrigger{TriggerType: "USER_END"}, testLogger()) {
		t.Fatal("trigger without request_id must not be reported as forwarded")
	}
}

func TestSendDurableTriggerAndAwaitAckDisconnected(t *testing.T) {
	withTestTriggerWAL(t)
	resetPendingAcks(t)
	clearConn()
	if sendDurableTriggerAndAwaitAck(&ipcpb.MistTrigger{RequestId: "evt-dc", TriggerType: "USER_END"}, testLogger()) {
		t.Fatal("no stream must not be reported as forwarded")
	}
}

// A positive ack truncates the WAL row and reports the trigger forwarded.
func TestSendDurableTriggerAndAwaitAckSuccessTruncatesWAL(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFake(t)

	trig := &ipcpb.MistTrigger{RequestId: "evt-ok", TriggerType: "USER_END"}
	if _, err := wal.Append(trig); err != nil {
		t.Fatalf("append: %v", err)
	}

	done := make(chan struct{})
	var ok bool
	go func() {
		defer close(done)
		ok = sendDurableTriggerAndAwaitAck(trig, testLogger())
	}()

	deliverAckAfterSend(t, stream, true, false)
	waitForTestDone(t, done, "success ack")

	if !ok {
		t.Fatal("positive ack should report forwarded=true")
	}
	if depth, _ := wal.PendingDepth(); depth != 0 {
		t.Fatalf("WAL not truncated after positive ack: depth=%d", depth)
	}
}

// A retryable negative ack leaves the entry on the WAL for the next pass.
func TestSendDurableTriggerAndAwaitAckRetryableKeepsWAL(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFake(t)

	trig := &ipcpb.MistTrigger{RequestId: "evt-retry", TriggerType: "USER_END"}
	if _, err := wal.Append(trig); err != nil {
		t.Fatalf("append: %v", err)
	}

	done := make(chan struct{})
	var ok bool
	go func() {
		defer close(done)
		ok = sendDurableTriggerAndAwaitAck(trig, testLogger())
	}()

	deliverAckAfterSend(t, stream, false, true)
	waitForTestDone(t, done, "retryable ack")

	if ok {
		t.Fatal("retryable negative ack must report forwarded=false")
	}
	if depth, _ := wal.PendingDepth(); depth != 1 {
		t.Fatalf("retryable ack must keep the WAL entry: depth=%d", depth)
	}
}

// A non-retryable negative ack moves the entry to the dead-letter store and
// reports done (true) so the forwarder stops re-sending a poison entry.
func TestSendDurableTriggerAndAwaitAckNonRetryableDeadLetters(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFake(t)

	trig := &ipcpb.MistTrigger{RequestId: "evt-poison", TriggerType: "USER_END"}
	if _, err := wal.Append(trig); err != nil {
		t.Fatalf("append: %v", err)
	}

	done := make(chan struct{})
	var ok bool
	go func() {
		defer close(done)
		ok = sendDurableTriggerAndAwaitAck(trig, testLogger())
	}()

	deliverAckAfterSend(t, stream, false, false)
	waitForTestDone(t, done, "non-retryable ack")

	if !ok {
		t.Fatal("non-retryable ack must report done=true so the forwarder stops re-sending")
	}
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
