package control

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// durableFoghorn is a control-stream server that records the durable outbox
// messages it receives. announce makes it open every stream with
// ControlCapabilities the way Foghorn does; ack makes it answer each
// durable_delivery_id after "processing" it. Without announce it opens with
// an unrelated message, as an older Foghorn's ConfigSeed does.
type durableFoghorn struct {
	ipcpb.UnimplementedHelmsmanControlServer
	announce bool
	ack      bool
	addr     string
	server   *grpc.Server

	registered chan time.Time
	received   chan *ipcpb.ControlMessage
}

func startDurableFoghorn(t *testing.T, announce, ack bool) *durableFoghorn {
	t.Helper()
	lis := listenLoopback(t)
	f := &durableFoghorn{
		announce:   announce,
		ack:        ack,
		addr:       lis.Addr().String(),
		server:     grpc.NewServer(),
		registered: make(chan time.Time, 16),
		received:   make(chan *ipcpb.ControlMessage, 64),
	}
	ipcpb.RegisterHelmsmanControlServer(f.server, f)
	go func() { _ = f.server.Serve(lis) }()
	t.Cleanup(f.server.Stop)
	return f
}

func (f *durableFoghorn) Connect(stream ipcpb.HelmsmanControl_ConnectServer) error {
	var sendMu sync.Mutex
	send := func(msg *ipcpb.ControlMessage) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(msg)
	}
	first := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_MistTriggerAck{MistTriggerAck: &ipcpb.MistTriggerAck{RequestId: "unrelated"}}}
	if f.announce {
		first = &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ControlCapabilities{
			ControlCapabilities: &ipcpb.ControlCapabilities{DurableDeliveryAcks: true},
		}}
	}
	if err := send(first); err != nil {
		return err
	}
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	if msg.GetRegister() == nil {
		return errors.New("first message is not Register")
	}
	select {
	case f.registered <- time.Now():
	default:
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if msg.GetArtifactDeleted() == nil {
			continue
		}
		f.received <- msg
		if f.ack && msg.GetDurableDeliveryId() != "" {
			if err := send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_DurableDeliveryAck{
				DurableDeliveryAck: &ipcpb.DurableDeliveryAck{DeliveryId: msg.GetDurableDeliveryId(), Success: true},
			}}); err != nil {
				return err
			}
		}
	}
}

func awaitDurableRegistration(t *testing.T, f *durableFoghorn, within time.Duration) {
	t.Helper()
	select {
	case <-f.registered:
	case <-time.After(within):
		t.Fatalf("no registration at %s within %s", f.addr, within)
	}
}

func awaitArtifactDeleted(t *testing.T, f *durableFoghorn, hash string, within time.Duration) *ipcpb.ControlMessage {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case msg := <-f.received:
			if msg.GetArtifactDeleted().GetArtifactHash() == hash {
				return msg
			}
		case <-deadline:
			return nil
		}
	}
}

func awaitPublishedConnection(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for getConnection() == nil {
		if time.Now().After(deadline) {
			t.Fatal("control connection was not published")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func durableOutboxRows(t *testing.T, dir string) []string {
	t.Helper()
	pending, err := filepath.Glob(filepath.Join(dir, "*.pb"))
	if err != nil {
		t.Fatal(err)
	}
	inflight, err := filepath.Glob(filepath.Join(dir, "*.pb.sent.*"))
	if err != nil {
		t.Fatal(err)
	}
	return append(pending, inflight...)
}

func runDurableControlDialer(t *testing.T, addrs []string, lastDial string, servers ...*durableFoghorn) {
	t.Helper()
	fakes := make([]*fakeFoghorn, 0, len(servers))
	for _, s := range servers {
		fakes = append(fakes, &fakeFoghorn{server: s.server})
	}
	runControlDialer(t, addrs, lastDial, fakes...)
}

// A terminal transition that reached a Foghorn which then went dark before
// handling it stays in the outbox, however many heartbeats the dead
// connection still buffers, and is delivered to the next instance.
func TestDurableOutboxRowSurvivesFoghornGoingDarkBeforeHandlingIt(t *testing.T) {
	prevKeepalive, prevHeartbeat := controlKeepalive, controlHeartbeatInterval
	controlKeepalive = keepalive.ClientParameters{Time: 10 * time.Second, Timeout: 2 * time.Second}
	controlHeartbeatInterval = 200 * time.Millisecond
	t.Cleanup(func() { controlKeepalive, controlHeartbeatInterval = prevKeepalive, prevHeartbeat })
	resetTestOutbox(t)
	outboxDir := t.TempDir()
	appconfigtest.Setenv(t, "FRAMEWORKS_CONTROL_OUTBOX_DIR", outboxDir)

	dying := startDurableFoghorn(t, true, false)
	proxy := startBlackholeProxy(t, dying.addr)
	next := startDurableFoghorn(t, true, true)
	runDurableControlDialer(t, []string{proxy.addr, next.addr}, next.addr, dying, next)

	awaitDurableRegistration(t, dying, 10*time.Second)
	awaitPublishedConnection(t)
	if err := SendArtifactDeleted("artifact-dark", "/data/artifact-dark", "cleanup", "vod", 42); err != nil {
		t.Fatalf("send durable transition: %v", err)
	}
	first := awaitArtifactDeleted(t, dying, "artifact-dark", 5*time.Second)
	if first == nil {
		t.Fatal("the first Foghorn never received the transition")
	}
	proxy.goDark()

	// Several heartbeats land in the dead connection's socket buffer before
	// the keepalive notices.
	time.Sleep(10 * controlHeartbeatInterval)
	if rows := durableOutboxRows(t, outboxDir); len(rows) != 1 {
		t.Fatalf("outbox rows after Foghorn went dark = %v, want the unacknowledged row kept", rows)
	}

	awaitDurableRegistration(t, next, controlKeepalive.Time+controlKeepalive.Timeout+5*time.Second)
	redelivered := awaitArtifactDeleted(t, next, "artifact-dark", 5*time.Second)
	if redelivered == nil {
		t.Fatal("the transition was not delivered to the next Foghorn instance")
	}
	if id := redelivered.GetDurableDeliveryId(); id == "" || id != first.GetDurableDeliveryId() {
		t.Fatalf("redelivery id = %q, first delivery id = %q; want the same non-empty id", id, first.GetDurableDeliveryId())
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(durableOutboxRows(t, outboxDir)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("acknowledged row not deleted: %v", durableOutboxRows(t, outboxDir))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An acknowledging Foghorn's ack deletes the row; heartbeats do not.
func TestDurableOutboxRowDeletedOnAckNotHeartbeat(t *testing.T) {
	prevHeartbeat := controlHeartbeatInterval
	controlHeartbeatInterval = time.Hour
	t.Cleanup(func() { controlHeartbeatInterval = prevHeartbeat })
	resetTestOutbox(t)
	outboxDir := t.TempDir()
	appconfigtest.Setenv(t, "FRAMEWORKS_CONTROL_OUTBOX_DIR", outboxDir)

	foghorn := startDurableFoghorn(t, true, true)
	runDurableControlDialer(t, []string{foghorn.addr}, foghorn.addr, foghorn)
	awaitDurableRegistration(t, foghorn, 10*time.Second)
	awaitPublishedConnection(t)
	if err := SendArtifactDeleted("artifact-acked", "/data/artifact-acked", "cleanup", "vod", 1); err != nil {
		t.Fatalf("send durable transition: %v", err)
	}
	if awaitArtifactDeleted(t, foghorn, "artifact-acked", 5*time.Second) == nil {
		t.Fatal("Foghorn never received the transition")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(durableOutboxRows(t, outboxDir)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("acknowledged row not deleted without a heartbeat: %v", durableOutboxRows(t, outboxDir))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A Foghorn that predates delivery acks never answers them; its rows are
// still confirmed by the next heartbeat, as before.
func TestDurableOutboxFallsBackToHeartbeatConfirmationWithoutAckCapability(t *testing.T) {
	prevHeartbeat := controlHeartbeatInterval
	controlHeartbeatInterval = 200 * time.Millisecond
	t.Cleanup(func() { controlHeartbeatInterval = prevHeartbeat })
	resetTestOutbox(t)
	outboxDir := t.TempDir()
	appconfigtest.Setenv(t, "FRAMEWORKS_CONTROL_OUTBOX_DIR", outboxDir)

	older := startDurableFoghorn(t, false, false)
	runDurableControlDialer(t, []string{older.addr}, older.addr, older)
	awaitDurableRegistration(t, older, 10*time.Second)
	awaitPublishedConnection(t)
	if err := SendArtifactDeleted("artifact-legacy", "/data/artifact-legacy", "cleanup", "vod", 1); err != nil {
		t.Fatalf("send durable transition: %v", err)
	}
	if awaitArtifactDeleted(t, older, "artifact-legacy", 5*time.Second) == nil {
		t.Fatal("Foghorn never received the transition")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(durableOutboxRows(t, outboxDir)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("row not confirmed by heartbeat on a Foghorn without acks: %v", durableOutboxRows(t, outboxDir))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
