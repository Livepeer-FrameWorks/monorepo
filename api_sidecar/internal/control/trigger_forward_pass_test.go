package control

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/proto"
)

// latencyFoghorn acknowledges every durable trigger after a fixed or random delay, the way
// Foghorn acks after its downstream commit. It records, per order key, the order in which entries
// arrived and flags any key that had two entries in flight at once.
type latencyFoghorn struct {
	latency func() time.Duration

	mu        sync.Mutex
	arrivals  map[string][]string
	inflight  map[string]int
	overlaps  []string
	firstSeen map[string]time.Time
	stop      chan struct{}
}

func startLatencyFoghorn(t *testing.T, stream *fakeControlStream, latency func() time.Duration) *latencyFoghorn {
	t.Helper()
	f := &latencyFoghorn{
		latency: latency, arrivals: make(map[string][]string), inflight: make(map[string]int),
		firstSeen: make(map[string]time.Time), stop: make(chan struct{}),
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
				key := triggerOrderKey(trigger)
				f.mu.Lock()
				f.arrivals[key] = append(f.arrivals[key], trigger.GetRequestId())
				if _, ok := f.firstSeen[trigger.GetRequestId()]; !ok {
					f.firstSeen[trigger.GetRequestId()] = time.Now()
				}
				if key != "" {
					f.inflight[key]++
					if f.inflight[key] > 1 {
						f.overlaps = append(f.overlaps, key)
					}
				}
				f.mu.Unlock()
				wg.Add(1)
				go func(id, key string, delay time.Duration) {
					defer wg.Done()
					select {
					case <-time.After(delay):
					case <-f.stop:
						return
					}
					f.mu.Lock()
					if key != "" {
						f.inflight[key]--
					}
					f.mu.Unlock()
					handleMistTriggerAck(&ipcpb.MistTriggerAck{RequestId: id, Success: true})
				}(trigger.GetRequestId(), key, f.latency())
			}
		}
	}()
	t.Cleanup(func() {
		close(f.stop)
		wg.Wait()
	})
	return f
}

func (f *latencyFoghorn) seenAt(id string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	at, ok := f.firstSeen[id]
	return at, ok
}

func fixedLatency(d time.Duration) func() time.Duration { return func() time.Duration { return d } }

// runForwarderLoop runs the production forwarder loop against the test WAL and connection.
func runForwarderLoop(t *testing.T) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			drainTriggerWAL(testLogger())
			select {
			case <-triggerForwarderWakeup:
			case <-ticker.C:
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() {
		// Dropping the connection ends a pass that is still draining a backlog.
		clearConn()
		close(stop)
		<-done
	})
}

func connectFakeBuffered(t *testing.T) *fakeControlStream {
	t.Helper()
	stream := &fakeControlStream{sendCh: make(chan *ipcpb.ControlMessage, 4*triggerForwardWindow)}
	storeConn(stream, "test-node")
	t.Cleanup(clearConn)
	return stream
}

func sampleTrigger(id string, receivedAtMillis int64) *ipcpb.MistTrigger {
	return &ipcpb.MistTrigger{
		RequestId: id, TriggerType: "PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", Timestamp: receivedAtMillis,
		TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{ProcessBilling: &ipcpb.ProcessBillingEvent{
			StreamName: "live+demo", ProcessType: "AV", DurationMs: 1000,
		}},
	}
}

// writeWALBacklog lays entries down in the WAL directory the way an earlier Helmsman left them,
// without an fsync per file, and opens the WAL over them as a restart would.
func writeWALBacklog(t *testing.T, triggers []*ipcpb.MistTrigger) *storage.TriggerWAL {
	t.Helper()
	dir := t.TempDir()
	for _, trigger := range triggers {
		payload, err := proto.Marshal(trigger)
		if err != nil {
			t.Fatal(err)
		}
		name := strconv.FormatInt(trigger.GetTimestamp(), 10) + "-" + trigger.GetRequestId() + ".pb"
		if err := os.WriteFile(filepath.Join(dir, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wal, err := storage.NewTriggerWAL(dir)
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

// With Foghorn taking 250 ms per ack, a steady 40 triggers/s (ten times the one-at-a-time
// capacity) keeps the pending depth bounded and drains as soon as intake stops.
func TestTriggerForwarderKeepsUpWithSlowAcks(t *testing.T) {
	wal := withTestTriggerWAL(t)
	resetPendingAcks(t)
	stream := connectFakeBuffered(t)
	startLatencyFoghorn(t, stream, fixedLatency(250*time.Millisecond))
	runForwarderLoop(t)

	const intakePerSecond = 40
	deadline := time.Now().Add(3 * time.Second)
	maxDepth := 0
	for i := 0; time.Now().Before(deadline); i++ {
		if err := SendDurableMistTrigger(sampleTrigger(fmt.Sprintf("steady-%05d", i), time.Now().UnixMilli())); err != nil {
			t.Fatalf("append: %v", err)
		}
		if depth, _ := wal.PendingDepth(); depth > maxDepth {
			maxDepth = depth
		}
		time.Sleep(time.Second / intakePerSecond)
	}
	if maxDepth > 20 {
		t.Fatalf("pending depth reached %d under a 40/s intake with 250 ms acks; want it bounded near latency*intake (10)", maxDepth)
	}
	waitFor(t, func() bool { depth, _ := wal.PendingDepth(); return depth == 0 }, "pending entries drained after intake stopped")
}

// A backlog of 10,000 entries left by a restart drains in seconds rather than one round trip at
// a time.
func TestTriggerForwarderDrainsLargeBacklog(t *testing.T) {
	base := time.Now().Add(-time.Hour).UnixMilli()
	backlog := make([]*ipcpb.MistTrigger, 0, 10000)
	for i := range 10000 {
		backlog = append(backlog, sampleTrigger(fmt.Sprintf("backlog-%05d", i), base+int64(i)))
	}
	wal := writeWALBacklog(t, backlog)
	resetPendingAcks(t)
	stream := connectFakeBuffered(t)
	startLatencyFoghorn(t, stream, fixedLatency(25*time.Millisecond))

	started := time.Now()
	runForwarderLoop(t)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if depth, _ := wal.PendingDepth(); depth == 0 {
			t.Logf("drained 10000 entries in %s", time.Since(started))
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	depth, _ := wal.PendingDepth()
	t.Fatalf("backlog not drained after 30s: %d entries pending (one-at-a-time delivery needs 250s)", depth)
}

// Entries that share an order key never overlap in flight and reach Foghorn in WAL order, while
// entries of different keys are delivered concurrently.
func TestTriggerForwarderPreservesPerRuntimeOrderUnderWindow(t *testing.T) {
	base := time.Now().Add(-time.Minute).UnixMilli()
	runtimes := []string{"live+order-a", "live+order-b", "live+order-c"}
	var backlog []*ipcpb.MistTrigger
	want := make(map[string][]string)
	for i := range 300 {
		runtime := runtimes[i%len(runtimes)]
		id := fmt.Sprintf("life-%04d", i)
		at := base + int64(i)
		var trigger *ipcpb.MistTrigger
		switch i % 4 {
		case 0:
			trigger = &ipcpb.MistTrigger{RequestId: id, TriggerType: "PUSH_INPUT_CLOSE", Timestamp: at,
				TriggerPayload: &ipcpb.MistTrigger_PushInputClose{PushInputClose: &ipcpb.PushInputCloseTrigger{StreamName: runtime}}}
		case 1:
			trigger = &ipcpb.MistTrigger{RequestId: id, TriggerType: "STREAM_END", Timestamp: at,
				TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{StreamName: runtime}}}
		case 2:
			trigger = &ipcpb.MistTrigger{RequestId: id, TriggerType: "PUSH_END", Timestamp: at,
				TriggerPayload: &ipcpb.MistTrigger_PushEnd{PushEnd: &ipcpb.PushEndTrigger{StreamName: runtime}}}
		default:
			trigger = &ipcpb.MistTrigger{RequestId: id, TriggerType: "USER_END", Timestamp: at,
				TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{StreamName: runtime, SessionId: "s-" + id}}}
		}
		backlog = append(backlog, trigger)
		key := triggerOrderKey(trigger)
		want[key] = append(want[key], id)
	}
	wal := writeWALBacklog(t, backlog)
	resetPendingAcks(t)
	stream := connectFakeBuffered(t)
	rng := rand.New(rand.NewSource(7))
	var rngMu sync.Mutex
	foghorn := startLatencyFoghorn(t, stream, func() time.Duration {
		rngMu.Lock()
		defer rngMu.Unlock()
		return time.Duration(rng.Intn(15)) * time.Millisecond
	})
	runForwarderLoop(t)
	waitFor(t, func() bool { depth, _ := wal.PendingDepth(); return depth == 0 }, "ordered backlog drained")

	foghorn.mu.Lock()
	defer foghorn.mu.Unlock()
	if len(foghorn.overlaps) > 0 {
		t.Fatalf("entries sharing an order key were in flight together: %v", foghorn.overlaps)
	}
	for key, ids := range want {
		got := foghorn.arrivals[key]
		if fmt.Sprint(got) != fmt.Sprint(ids) {
			t.Fatalf("key %s arrived as %v, want WAL order %v", key, got, ids)
		}
	}
}

// A lifecycle trigger appended behind 10,000 undelivered billing samples reaches Foghorn within
// about one ack, not after the sample backlog.
func TestTriggerForwarderDeliversLifecycleAheadOfSampleBacklog(t *testing.T) {
	base := time.Now().Add(-time.Hour).UnixMilli()
	backlog := make([]*ipcpb.MistTrigger, 0, 10000)
	for i := range 10000 {
		backlog = append(backlog, sampleTrigger(fmt.Sprintf("sample-%05d", i), base+int64(i)))
	}
	wal := writeWALBacklog(t, backlog)
	resetPendingAcks(t)
	stream := connectFakeBuffered(t)
	foghorn := startLatencyFoghorn(t, stream, fixedLatency(250*time.Millisecond))
	runForwarderLoop(t)
	waitFor(t, func() bool { _, ok := foghorn.seenAt("sample-00000"); return ok }, "sample backlog draining")

	appended := time.Now()
	userEnd := &ipcpb.MistTrigger{
		RequestId: "lifecycle-user-end", TriggerType: "USER_END", Timestamp: appended.UnixMilli(),
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{StreamName: "live+demo", SessionId: "viewer-1"}},
	}
	if err := SendDurableMistTrigger(userEnd); err != nil {
		t.Fatalf("append USER_END: %v", err)
	}
	waitFor(t, func() bool { return !wal.IsPending("lifecycle-user-end") }, "USER_END acknowledged")
	seen, _ := foghorn.seenAt("lifecycle-user-end")
	if delay := seen.Sub(appended); delay > 2*time.Second {
		t.Fatalf("USER_END reached Foghorn %s after append behind the sample backlog; want within about one ack", delay)
	}
	if depth, _ := wal.PendingDepth(); depth < 9000 {
		t.Fatalf("sample backlog drained to %d before the check; the USER_END was not behind it", depth)
	}
}
