package control

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/grpc"
)

// sequenceFoghorn records the order of Register and Mist triggers on each connection and denies
// every PUSH_REWRITE, which is enough for Helmsman's side of the exchange to complete.
type sequenceFoghorn struct {
	ipcpb.UnimplementedHelmsmanControlServer
	addr   string
	server *grpc.Server

	mu       sync.Mutex
	received []string
}

func (f *sequenceFoghorn) Connect(stream ipcpb.HelmsmanControl_ConnectServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		switch {
		case msg.GetRegister() != nil:
			f.record("Register")
		case msg.GetMistTrigger() != nil:
			trigger := msg.GetMistTrigger()
			f.record(trigger.GetTriggerType())
			if trigger.GetPushRewrite() != nil {
				if sendErr := stream.Send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_MistTriggerResponse{
					MistTriggerResponse: &ipcpb.MistTriggerResponse{
						RequestId: trigger.GetRequestId(), Abort: true, Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY,
					},
				}}); sendErr != nil {
					return sendErr
				}
			}
		}
	}
}

func (f *sequenceFoghorn) record(event string) {
	f.mu.Lock()
	f.received = append(f.received, event)
	f.mu.Unlock()
}

func (f *sequenceFoghorn) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.received)
}

func startSequenceFoghorn(t *testing.T) *sequenceFoghorn {
	t.Helper()
	lis := listenLoopback(t)
	f := &sequenceFoghorn{addr: lis.Addr().String(), server: grpc.NewServer()}
	ipcpb.RegisterHelmsmanControlServer(f.server, f)
	go func() { _ = f.server.Serve(lis) }()
	t.Cleanup(f.server.Stop)
	return f
}

// A new control connection carries the node's lifecycle report before anything else, and a
// PUSH_REWRITE that arrives while that report is still being built waits for it instead of failing,
// so Foghorn has the node's telemetry when it places the publisher.
func TestNewConnectionReportsNodeLifecycleBeforePushRewrite(t *testing.T) {
	withTestTriggerWAL(t)
	resetPendingAcks(t)
	reporting := make(chan struct{})
	release := make(chan struct{})
	SetConnectionNodeLifecycleReporter(func(context.Context) (*ipcpb.MistTrigger, error) {
		close(reporting)
		<-release
		return &ipcpb.MistTrigger{
			TriggerType:    "NODE_LIFECYCLE_UPDATE",
			TriggerPayload: &ipcpb.MistTrigger_NodeLifecycleUpdate{NodeLifecycleUpdate: &ipcpb.NodeLifecycleUpdate{EventType: "node_lifecycle_update"}},
		}, nil
	})
	t.Cleanup(func() { SetConnectionNodeLifecycleReporter(nil) })

	foghorn := startSequenceFoghorn(t)
	runControlDialer(t, []string{foghorn.addr}, foghorn.addr)
	// Cleanups run last-registered first: this ends the connection before the dialer is awaited.
	t.Cleanup(foghorn.server.Stop)

	select {
	case <-reporting:
	case <-time.After(10 * time.Second):
		t.Fatal("the new connection never built its node lifecycle report")
	}
	type outcome struct {
		result *MistTriggerResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := SendMistTriggerContext(context.Background(), pushRewrite("rewrite-fresh", "key-fresh"), testLogger())
		done <- outcome{result, err}
	}()
	time.Sleep(300 * time.Millisecond)
	close(release)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("PUSH_REWRITE on the new connection failed: %v", got.err)
		}
		if got.result == nil || got.result.Action != ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY {
			t.Fatalf("PUSH_REWRITE result = %+v, want Foghorn's answer", got.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PUSH_REWRITE never completed")
	}
	events := foghorn.events()
	want := []string{"Register", "NODE_LIFECYCLE_UPDATE", "PUSH_REWRITE"}
	if len(events) < len(want) || !slices.Equal(events[:len(want)], want) {
		t.Fatalf("Foghorn received %v; want %v first", events, want)
	}
}

// When the connection an unacknowledged durable trigger was sent on ends, the forwarder stops
// waiting for that ack at once and delivers the entry on the next connection, ahead of a PUSH_REWRITE
// that waits for it, instead of holding the WAL for the full ack timeout.
func TestAckWaitEndsWithItsConnection(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	runForwarder(t)

	// The first connection receives the close and never answers.
	first := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 4)}
	firstConn := storeConn(first, "test-node")
	t.Cleanup(clearConn)
	admitRuntimeForKey(t, "live+lost-ack", "key-lost-ack")
	appendClose(t, wal, "close-lost-ack", "live+lost-ack", time.Now().UnixMilli())
	wakeupTriggerForwarder()
	waitForControlMessage(t, first.sendCh, "close on the first connection")

	// That connection ends and a new one comes up, as runClient does across a reconnect.
	clearConn()
	close(firstConn.ended)
	second := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 4)}
	storeConn(second, "test-node")
	foghorn := startOrderingFoghorn(t, second, map[string]string{"key-lost-ack": "live+lost-ack"}, false)
	wakeupTriggerForwarder()

	started := time.Now()
	result, err := SendMistTriggerContext(context.Background(), pushRewrite("rewrite-lost-ack", "key-lost-ack"), testLogger())
	if err != nil || result.Abort {
		t.Fatalf("PUSH_REWRITE after the reconnect: result=%+v err=%v", result, err)
	}
	if waited := time.Since(started); waited > 2*time.Second {
		t.Fatalf("PUSH_REWRITE waited %s for a close whose connection had already ended", waited)
	}
	closeAt, rewriteAt := foghorn.index("PUSH_INPUT_CLOSE:live+lost-ack"), foghorn.index("PUSH_REWRITE:key-lost-ack")
	if closeAt < 0 || rewriteAt < 0 || closeAt > rewriteAt {
		t.Fatalf("second connection received %v; want the resent close before the admission", foghorn.events())
	}
	if pending, _ := wal.PendingForRuntime("live+lost-ack"); pending != 0 {
		t.Fatalf("runtime still has %d pending end triggers", pending)
	}
}
