package control

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func sentDurableRow(t *testing.T, dir string, conn *streamConn) string {
	t.Helper()
	msg := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_SyncComplete{
		SyncComplete: &ipcpb.SyncComplete{RequestId: "ack-sync", Status: "synced"},
	}}
	if err := enqueueDurableOutbox(msg); err != nil {
		t.Fatal(err)
	}
	publishConn(conn)
	if err := drainDurableOutboxForConnection(conn); err != nil {
		t.Fatal(err)
	}
	stream, ok := conn.stream.(*fakeControlStream)
	if !ok || len(stream.sent) == 0 {
		t.Fatal("row was not sent")
	}
	id := stream.sent[len(stream.sent)-1].GetDurableDeliveryId()
	if _, err := os.Stat(filepath.Join(dir, id+".pb.sent."+conn.epoch)); err != nil {
		t.Fatalf("sent row %q not in flight: %v", id, err)
	}
	return id
}

func ackConn(epoch string) *streamConn {
	conn := &streamConn{stream: &fakeControlStream{}, epoch: epoch, delivery: newDurableDeliveryContract()}
	conn.delivery.observe(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ControlCapabilities{
		ControlCapabilities: &ipcpb.ControlCapabilities{DurableDeliveryAcks: true},
	}})
	return conn
}

func TestDurableDeliveryContractIsSettledByFoghornsFirstMessage(t *testing.T) {
	capable := newDurableDeliveryContract()
	capable.observe(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ControlCapabilities{
		ControlCapabilities: &ipcpb.ControlCapabilities{DurableDeliveryAcks: true},
	}})
	capable.observe(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ConfigSeed{ConfigSeed: &ipcpb.ConfigSeed{}}})
	if got := capable.current(); got != durableContractAck {
		t.Fatalf("contract after ControlCapabilities = %d, want ack", got)
	}

	older := newDurableDeliveryContract()
	older.observe(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ConfigSeed{ConfigSeed: &ipcpb.ConfigSeed{}}})
	older.observe(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ControlCapabilities{
		ControlCapabilities: &ipcpb.ControlCapabilities{DurableDeliveryAcks: true},
	}})
	if got := older.current(); got != durableContractHeartbeat {
		t.Fatalf("contract after a first ConfigSeed = %d, want heartbeat", got)
	}
}

// Until Foghorn's first message arrives, a heartbeat confirms nothing.
func TestHeartbeatBeforeContractIsKnownKeepsSentRows(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	appconfigtest.Setenv(t, "FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	conn := &streamConn{stream: &fakeControlStream{}, epoch: "undecided", delivery: newDurableDeliveryContract()}
	id := sentDurableRow(t, dir, conn)
	if err := sendHeartbeatAndConfirmDurableOutbox(conn, &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_Heartbeat{Heartbeat: &ipcpb.Heartbeat{}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".pb.sent.undecided")); err != nil {
		t.Fatalf("heartbeat before the contract was known removed the row: %v", err)
	}
}

// A refused delivery goes back to pending, is held back for the retry delay,
// and is then resent under the same delivery id.
func TestRefusedDurableDeliveryIsResentAfterRetryDelay(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	appconfigtest.Setenv(t, "FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	t.Cleanup(func() {
		durableOutboxMu.Lock()
		clear(durableOutboxRetryNotBefore)
		durableOutboxMu.Unlock()
	})
	conn := ackConn("refused")
	id := sentDurableRow(t, dir, conn)

	handleDurableDeliveryAck(conn, &ipcpb.DurableDeliveryAck{DeliveryId: id, Error: "database unavailable"})
	if _, err := os.Stat(filepath.Join(dir, id+".pb")); err != nil {
		t.Fatalf("refused row not returned to pending: %v", err)
	}
	stream := conn.stream.(*fakeControlStream)
	sentBefore := len(stream.sent)
	if err := drainDurableOutboxForConnection(conn); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != sentBefore {
		t.Fatal("refused row was resent before its retry delay")
	}

	durableOutboxMu.Lock()
	durableOutboxRetryNotBefore[id] = time.Now().Add(-time.Second)
	durableOutboxMu.Unlock()
	if err := drainDurableOutboxForConnection(conn); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != sentBefore+1 || stream.sent[len(stream.sent)-1].GetDurableDeliveryId() != id {
		t.Fatalf("refused row not resent under its delivery id after the delay")
	}

	handleDurableDeliveryAck(conn, &ipcpb.DurableDeliveryAck{DeliveryId: id, Success: true})
	if rows := durableOutboxRows(t, dir); len(rows) != 0 {
		t.Fatalf("acknowledged row not removed: %v", rows)
	}
}

// A late success ack from an earlier connection removes the row even after
// a reconnect returned it to pending: Foghorn has handled it.
func TestDurableDeliveryAckRemovesRowRequeuedByReconnect(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	dir := t.TempDir()
	appconfigtest.Setenv(t, "FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	first := ackConn("first")
	id := sentDurableRow(t, dir, first)
	if err := prepareDurableOutboxForEpoch("second"); err != nil {
		t.Fatal(err)
	}
	handleDurableDeliveryAck(first, &ipcpb.DurableDeliveryAck{DeliveryId: id, Success: true})
	if rows := durableOutboxRows(t, dir); len(rows) != 0 {
		t.Fatalf("row handled by Foghorn kept for resend: %v", rows)
	}
}

func TestDurableDeliveryAckIgnoresIDsOutsideTheOutbox(t *testing.T) {
	resetControlState(t)
	resetTestOutbox(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "outbox")
	appconfigtest.Setenv(t, "FRAMEWORKS_CONTROL_OUTBOX_DIR", dir)
	outside := filepath.Join(parent, "victim.pb")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	handleDurableDeliveryAck(ackConn("e"), &ipcpb.DurableDeliveryAck{DeliveryId: "../victim", Success: true})
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("ack with a path-escaping id removed a file outside the outbox: %v", err)
	}
}
