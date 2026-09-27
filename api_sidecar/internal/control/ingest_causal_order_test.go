package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// orderingFoghorn answers the control stream: durable triggers are acknowledged (a PUSH_INPUT_CLOSE only
// once hold is closed), PUSH_REWRITE is admitted into the runtime named in admit. It records the
// order in which Foghorn received each trigger.
type orderingFoghorn struct {
	mu       sync.Mutex
	received []string
	hold     chan struct{}
	admit    map[string]string // stream key -> runtime
	stop     chan struct{}
}

func startOrderingFoghorn(t *testing.T, stream *fakeControlStream, admit map[string]string, holdCloses bool) *orderingFoghorn {
	t.Helper()
	f := &orderingFoghorn{admit: admit, stop: make(chan struct{}), hold: make(chan struct{})}
	if !holdCloses {
		close(f.hold)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-f.stop:
				return
			case msg := <-stream.sendCh:
				trigger := msg.GetMistTrigger()
				if trigger == nil {
					continue
				}
				switch {
				case trigger.GetPushRewrite() != nil:
					key := trigger.GetPushRewrite().GetStreamName()
					f.record("PUSH_REWRITE:" + key)
					handleMistTriggerResponse(&ipcpb.MistTriggerResponse{
						RequestId: trigger.GetRequestId(), Response: f.admit[key],
						IngestGeneration: "gen-" + key, IngestConnectorPid: 4242,
					})
				case trigger.GetPushInputClose() != nil:
					f.record("PUSH_INPUT_CLOSE:" + trigger.GetPushInputClose().GetStreamName())
					go func(id string) {
						select {
						case <-f.hold:
							handleMistTriggerAck(&ipcpb.MistTriggerAck{RequestId: id, Success: true})
						case <-f.stop:
						}
					}(trigger.GetRequestId())
				default:
					f.record(trigger.GetTriggerType())
					handleMistTriggerAck(&ipcpb.MistTriggerAck{RequestId: trigger.GetRequestId(), Success: true})
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(f.stop)
		wg.Wait()
	})
	return f
}

func (f *orderingFoghorn) record(event string) {
	f.mu.Lock()
	f.received = append(f.received, event)
	f.mu.Unlock()
}

func (f *orderingFoghorn) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.received...)
}

func (f *orderingFoghorn) index(event string) int {
	for i, got := range f.events() {
		if got == event {
			return i
		}
	}
	return -1
}

// runForwarder runs the forwarder's drain on every wakeup, as triggerForwarderLoop does.
func runForwarder(t *testing.T) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-triggerForwarderWakeup:
				drainTriggerWAL(testLogger())
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
	})
}

func admitRuntimeForKey(t *testing.T, runtime, key string) {
	t.Helper()
	if err := recordAdmittedIngestGeneration(runtime, "gen-previous-"+runtime, 100, pushAdmissionKey(key)); err != nil {
		t.Fatalf("record admission of %s: %v", runtime, err)
	}
	t.Cleanup(func() { forgetAdmittedRuntimes([]string{runtime}) })
}

func appendClose(t *testing.T, wal *storage.TriggerWAL, id, runtime string, receivedAtMillis int64) {
	t.Helper()
	if _, err := wal.Append(&ipcpb.MistTrigger{
		RequestId: id, TriggerType: "PUSH_INPUT_CLOSE", Timestamp: receivedAtMillis,
		TriggerPayload: &ipcpb.MistTrigger_PushInputClose{PushInputClose: &ipcpb.PushInputCloseTrigger{StreamName: runtime, Pid: 100}},
	}); err != nil {
		t.Fatalf("append close: %v", err)
	}
}

func pushRewrite(requestID, key string) *ipcpb.MistTrigger {
	return &ipcpb.MistTrigger{
		RequestId: requestID, TriggerType: "PUSH_REWRITE", Blocking: true,
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{StreamName: key, PushUrl: "rtmp://edge/live/" + key}},
	}
}

// A PUSH_REWRITE for a runtime whose previous connection's PUSH_INPUT_CLOSE is still in the WAL
// reaches Foghorn only after that close is acknowledged. A PUSH_REWRITE for another runtime is
// forwarded at once while that close is still outstanding.
func TestPushRewriteFollowsRuntimeCloseStillInWAL(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFake(t)
	foghorn := startOrderingFoghorn(t, stream, map[string]string{"key-x": "live+order-x", "key-y": "live+order-y"}, true)
	runForwarder(t)

	admitRuntimeForKey(t, "live+order-x", "key-x")
	admitRuntimeForKey(t, "live+order-y", "key-y")
	appendClose(t, wal, "close-x", "live+order-x", time.Now().UnixMilli())
	wakeupTriggerForwarder()
	waitFor(t, func() bool { return foghorn.index("PUSH_INPUT_CLOSE:live+order-x") >= 0 }, "old close sent")

	// Runtime Y has nothing pending: its admission is not held behind X's unacknowledged close.
	started := time.Now()
	resultY, errY := SendMistTriggerContext(context.Background(), pushRewrite("rewrite-y", "key-y"), testLogger())
	if errY != nil || resultY.Abort {
		t.Fatalf("unrelated PUSH_REWRITE: result=%+v err=%v", resultY, errY)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("unrelated PUSH_REWRITE waited %s behind another runtime's close", waited)
	}

	type outcome struct {
		result *MistTriggerResult
		err    error
	}
	doneX := make(chan outcome, 1)
	go func() {
		result, err := SendMistTriggerContext(context.Background(), pushRewrite("rewrite-x", "key-x"), testLogger())
		doneX <- outcome{result, err}
	}()
	time.Sleep(300 * time.Millisecond)
	if foghorn.index("PUSH_REWRITE:key-x") >= 0 {
		t.Fatalf("PUSH_REWRITE reached Foghorn before the runtime's close was acknowledged: %v", foghorn.events())
	}

	close(foghorn.hold)
	select {
	case got := <-doneX:
		if got.err != nil || got.result.Abort {
			t.Fatalf("PUSH_REWRITE after the close: result=%+v err=%v", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PUSH_REWRITE never completed after the close was acknowledged")
	}
	closeAt, rewriteAt := foghorn.index("PUSH_INPUT_CLOSE:live+order-x"), foghorn.index("PUSH_REWRITE:key-x")
	if closeAt < 0 || rewriteAt < 0 || closeAt > rewriteAt {
		t.Fatalf("Foghorn received %v; want the close before the new admission", foghorn.events())
	}
	if pending, _ := wal.PendingForRuntime("live+order-x"); pending != 0 {
		t.Fatalf("runtime still has %d pending end triggers", pending)
	}
}

// A waiting admission has its runtime's close delivered ahead of older, unrelated WAL entries.
func TestPushRewriteDeliversRuntimeCloseAheadOfBacklog(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFake(t)
	foghorn := startOrderingFoghorn(t, stream, map[string]string{"key-p": "live+priority-p"}, false)
	runForwarder(t)

	admitRuntimeForKey(t, "live+priority-p", "key-p")
	older := time.Now().Add(-time.Minute).UnixMilli()
	if _, err := wal.Append(&ipcpb.MistTrigger{RequestId: "backlog-user-end", TriggerType: "USER_END", Timestamp: older}); err != nil {
		t.Fatalf("append backlog: %v", err)
	}
	appendClose(t, wal, "close-p", "live+priority-p", time.Now().UnixMilli())

	result, err := SendMistTriggerContext(context.Background(), pushRewrite("rewrite-p", "key-p"), testLogger())
	if err != nil || result.Abort {
		t.Fatalf("PUSH_REWRITE: result=%+v err=%v", result, err)
	}
	events := foghorn.events()
	if len(events) == 0 || events[0] != "PUSH_INPUT_CLOSE:live+priority-p" {
		t.Fatalf("Foghorn received %v; want the waiting runtime's close first", events)
	}
	if closeAt, rewriteAt := foghorn.index("PUSH_INPUT_CLOSE:live+priority-p"), foghorn.index("PUSH_REWRITE:key-p"); closeAt > rewriteAt {
		t.Fatalf("Foghorn received %v; want the close before the admission", events)
	}
}

// When the close cannot reach Foghorn within the admission budget the PUSH_REWRITE fails without
// being sent: Foghorn would refuse it as a duplicate of the old connection.
func TestPushRewriteFailsWhenRuntimeCloseCannotBeDelivered(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFake(t)
	foghorn := startOrderingFoghorn(t, stream, map[string]string{"key-u": "live+undelivered-u"}, true)
	runForwarder(t)

	admitRuntimeForKey(t, "live+undelivered-u", "key-u")
	appendClose(t, wal, "close-u", "live+undelivered-u", time.Now().UnixMilli())

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	result, err := SendMistTriggerContext(ctx, pushRewrite("rewrite-u", "key-u"), testLogger())
	if err == nil || result == nil || !result.Abort {
		t.Fatalf("PUSH_REWRITE with an undeliverable close: result=%+v err=%v, want an aborted error", result, err)
	}
	if foghorn.index("PUSH_REWRITE:key-u") >= 0 {
		t.Fatalf("PUSH_REWRITE was sent although the runtime's close was not delivered: %v", foghorn.events())
	}
	close(foghorn.hold)
}

// The runtime index survives a restart: entries read back from disk still hold back an admission.
func TestTriggerWALRuntimeIndexSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	wal, err := storage.NewTriggerWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendClose(t, wal, "close-r", "live+reopened", time.Now().UnixMilli())
	if _, appendErr := wal.Append(&ipcpb.MistTrigger{
		RequestId: "end-r", TriggerType: "STREAM_END",
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+reopened"}},
	}); appendErr != nil {
		t.Fatal(appendErr)
	}
	reopened, err := storage.NewTriggerWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pending, _ := reopened.PendingForRuntime("live+reopened"); pending != 2 {
		t.Fatalf("reopened WAL pending for runtime = %d, want 2", pending)
	}
	if err := reopened.Ack("close-r"); err != nil {
		t.Fatal(err)
	}
	if pending, _ := reopened.PendingForRuntime("live+reopened"); pending != 1 {
		t.Fatalf("after ack pending = %d, want 1", pending)
	}
}

// The runtime a stream key was admitted into is persisted with the generation record, so the first
// PUSH_REWRITE after a Helmsman restart is still ordered behind that runtime's WAL entries.
func TestAdmissionKeyRuntimeSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewIngestGenerationStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if putErr := store.PutAdmission("live+restarted", "gen-restarted", 77, pushAdmissionKey("key-restarted")); putErr != nil {
		t.Fatal(putErr)
	}
	if endErr := store.MarkEnded("live+restarted", "gen-restarted", 77); endErr != nil {
		t.Fatal(endErr)
	}
	reopened, err := storage.NewIngestGenerationStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	records, err := reopened.Load()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { forgetAdmittedRuntimes([]string{"live+restarted"}) })
	rehydrateIngestGenerationFences(records)
	if runtime, ok := admittedRuntimeForKey(pushAdmissionKey("key-restarted")); !ok || runtime != "live+restarted" {
		t.Fatalf("runtime for key after restart = %q (known=%v), want live+restarted", runtime, ok)
	}
}

func waitFor(t *testing.T, cond func() bool, reason string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", reason)
}
