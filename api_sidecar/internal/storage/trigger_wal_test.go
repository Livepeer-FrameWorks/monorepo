package storage

import (
	"os"
	"path/filepath"
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/proto"
)

func newTrigger(t *testing.T, nodeID, triggerType string, body []byte) *pb.MistTrigger {
	t.Helper()
	return &pb.MistTrigger{
		TriggerType: triggerType,
		NodeId:      nodeID,
		Timestamp:   1700000000123,
		Blocking:    false,
		RequestId:   ComputeSourceEventID(nodeID, triggerType, body),
	}
}

func TestComputeSourceEventIDStable(t *testing.T) {
	id1 := ComputeSourceEventID("node-1", "USER_END", []byte("payload"))
	id2 := ComputeSourceEventID("node-1", "USER_END", []byte("payload"))
	if id1 != id2 {
		t.Fatalf("same inputs must hash identically, got %q vs %q", id1, id2)
	}
	id3 := ComputeSourceEventID("node-2", "USER_END", []byte("payload"))
	if id1 == id3 {
		t.Fatalf("different node_id must produce different hash")
	}
	id4 := ComputeSourceEventID("node-1", "STREAM_END", []byte("payload"))
	if id1 == id4 {
		t.Fatalf("different trigger_type must produce different hash")
	}
}

func TestTriggerWALAppendIdempotent(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewTriggerWAL(dir)
	if err != nil {
		t.Fatalf("NewTriggerWAL: %v", err)
	}

	trigger := newTrigger(t, "node-1", "USER_END", []byte("body-A"))

	created, err := wal.Append(trigger)
	if err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if !created {
		t.Fatal("first Append should report created=true")
	}

	created, err = wal.Append(trigger)
	if err != nil {
		t.Fatalf("second Append: %v", err)
	}
	if created {
		t.Fatal("duplicate Append (same source_event_id) should report created=false")
	}

	depth, err := wal.PendingDepth()
	if err != nil {
		t.Fatalf("PendingDepth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("PendingDepth = %d, want 1 after duplicate Append", depth)
	}
}

func TestTriggerWALAppendIdempotentAcrossTimestamp(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewTriggerWAL(dir)
	if err != nil {
		t.Fatalf("NewTriggerWAL: %v", err)
	}

	trigger := newTrigger(t, "node-1", "USER_END", []byte("body-A"))
	if _, appendErr := wal.Append(trigger); appendErr != nil {
		t.Fatalf("first Append: %v", appendErr)
	}
	redelivery := protoClone(t, trigger)
	redelivery.Timestamp += 5000
	created, err := wal.Append(redelivery)
	if err != nil {
		t.Fatalf("redelivery Append: %v", err)
	}
	if created {
		t.Fatal("duplicate source_event_id with a different timestamp should not create a second WAL row")
	}
	depth, err := wal.PendingDepth()
	if err != nil {
		t.Fatalf("PendingDepth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("PendingDepth = %d, want 1", depth)
	}
}

func TestTriggerWALAckRemoves(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewTriggerWAL(dir)
	if err != nil {
		t.Fatalf("NewTriggerWAL: %v", err)
	}

	trigger := newTrigger(t, "node-1", "USER_END", []byte("body-A"))
	if _, appendErr := wal.Append(trigger); appendErr != nil {
		t.Fatalf("Append: %v", appendErr)
	}

	if ackErr := wal.Ack(trigger.RequestId); ackErr != nil {
		t.Fatalf("Ack: %v", ackErr)
	}

	depth, err := wal.PendingDepth()
	if err != nil {
		t.Fatalf("PendingDepth: %v", err)
	}
	if depth != 0 {
		t.Fatalf("PendingDepth after Ack = %d, want 0", depth)
	}

	// Idempotent
	if ackErr := wal.Ack(trigger.RequestId); ackErr != nil {
		t.Fatalf("double Ack should be idempotent, got %v", ackErr)
	}
}

func TestTriggerWALDeadLetterRemovesFromPending(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewTriggerWAL(dir)
	if err != nil {
		t.Fatalf("NewTriggerWAL: %v", err)
	}

	trigger := newTrigger(t, "node-1", "USER_END", []byte("body-A"))
	if _, appendErr := wal.Append(trigger); appendErr != nil {
		t.Fatalf("Append: %v", appendErr)
	}
	if deadLetterErr := wal.DeadLetter(trigger.RequestId); deadLetterErr != nil {
		t.Fatalf("DeadLetter: %v", deadLetterErr)
	}
	depth, err := wal.PendingDepth()
	if err != nil {
		t.Fatalf("PendingDepth: %v", err)
	}
	if depth != 0 {
		t.Fatalf("PendingDepth after DeadLetter = %d, want 0", depth)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.dead"))
	if err != nil {
		t.Fatalf("glob dead files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("dead-letter files = %d, want 1", len(matches))
	}
}

func TestTriggerWALPendingOrderedByTimestamp(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewTriggerWAL(dir)
	if err != nil {
		t.Fatalf("NewTriggerWAL: %v", err)
	}

	older := newTrigger(t, "node-1", "USER_END", []byte("a"))
	older.Timestamp = 1000
	newer := newTrigger(t, "node-1", "USER_END", []byte("b"))
	newer.Timestamp = 2000

	// Append newer first to verify ordering is by timestamp prefix, not insert order.
	if _, appendErr := wal.Append(newer); appendErr != nil {
		t.Fatalf("Append newer: %v", appendErr)
	}
	if _, appendErr := wal.Append(older); appendErr != nil {
		t.Fatalf("Append older: %v", appendErr)
	}

	pending, err := wal.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("Pending len = %d, want 2", len(pending))
	}
	if pending[0].RequestId != older.RequestId {
		t.Fatal("Pending[0] should be the older entry")
	}
	if pending[1].RequestId != newer.RequestId {
		t.Fatal("Pending[1] should be the newer entry")
	}
}

func TestTriggerWALRecoversAcrossInstances(t *testing.T) {
	dir := t.TempDir()

	wal1, err := NewTriggerWAL(dir)
	if err != nil {
		t.Fatalf("NewTriggerWAL #1: %v", err)
	}
	trigger := newTrigger(t, "node-1", "USER_END", []byte("body"))
	if _, appendErr := wal1.Append(trigger); appendErr != nil {
		t.Fatalf("Append: %v", appendErr)
	}

	// Simulate restart: drop wal1, open a fresh handle on the same dir.
	wal2, err := NewTriggerWAL(dir)
	if err != nil {
		t.Fatalf("NewTriggerWAL #2: %v", err)
	}
	pending, err := wal2.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 || pending[0].RequestId != trigger.RequestId {
		t.Fatalf("restart did not surface persisted trigger, got %+v", pending)
	}
}

func TestTriggerWALSkipsTmpFiles(t *testing.T) {
	dir := t.TempDir()
	wal, err := NewTriggerWAL(dir)
	if err != nil {
		t.Fatalf("NewTriggerWAL: %v", err)
	}

	trigger := newTrigger(t, "node-1", "USER_END", []byte("body"))
	if _, appendErr := wal.Append(trigger); appendErr != nil {
		t.Fatalf("Append: %v", appendErr)
	}

	// Drop a stray .tmp file alongside the real entry; Pending must ignore it.
	stray := filepath.Join(dir, "1234-deadbeef.pb.tmp")
	if writeErr := writeFile(t, stray, []byte("not-a-real-trigger")); writeErr != nil {
		t.Fatalf("seed tmp: %v", writeErr)
	}

	pending, err := wal.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("Pending len = %d, want 1 (stray .tmp must be ignored)", len(pending))
	}
}

func writeFile(t *testing.T, path string, data []byte) error {
	t.Helper()
	return os.WriteFile(path, data, 0o600)
}

func protoClone(t *testing.T, trigger *pb.MistTrigger) *pb.MistTrigger {
	t.Helper()
	cloned, ok := proto.Clone(trigger).(*pb.MistTrigger)
	if !ok {
		t.Fatal("clone did not return MistTrigger")
	}
	return cloned
}
