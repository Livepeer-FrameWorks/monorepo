package control

import (
	"testing"
	"time"

	"frameworks/api_sidecar/internal/storage"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newTestAggregator(clock *fakeClock) *processBillingAggregator {
	a := newProcessBillingAggregator()
	a.now = clock.Now
	return a
}

func int64p(v int64) *int64 { return &v }

func avSample(durationMs, sourceAdvancedMs, outFrames, outBytes int64, final bool) *ipcpb.MistTrigger {
	return &ipcpb.MistTrigger{
		TriggerType: "PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", NodeId: "edge-1", Timestamp: time.Now().UnixMilli(),
		TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{ProcessBilling: &ipcpb.ProcessBillingEvent{
			NodeId: "edge-1", StreamName: "live+demo", ProcessType: "AV", DurationMs: durationMs,
			SourceAdvancedMs: int64p(sourceAdvancedMs), SinkAdvancedMs: int64p(sourceAdvancedMs),
			OutputFramesDelta: int64p(outFrames), InputFramesDelta: int64p(outFrames),
			OutputBytesDelta: int64p(outBytes), InputBytesDelta: int64p(outBytes * 2),
			OutputFrames: int64p(outFrames * 10), IsFinal: &final,
		}},
	}
}

func pendingBilling(t *testing.T, wal *storage.TriggerWAL) []*ipcpb.ProcessBillingEvent {
	t.Helper()
	pending, err := wal.PendingBatch(0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]*ipcpb.ProcessBillingEvent, 0, len(pending))
	for _, trigger := range pending {
		out = append(out, trigger.GetProcessBilling())
	}
	return out
}

// Samples of one process within a window are summed exactly into one staged event, which is
// sealed for delivery as soon as the process reports its final sample.
func TestProcessBillingWindowSumsAndSealsOnFinal(t *testing.T) {
	wal := withTestTriggerWAL(t)
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	agg := newTestAggregator(clock)

	samples := []*ipcpb.MistTrigger{
		avSample(1000, 1001, 30, 4000, false),
		avSample(1000, 998, 29, 4100, false),
		avSample(2000, 2003, 61, 8300, true),
	}
	var ids []string
	for i, sample := range samples {
		id, err := agg.add(wal, "pid-7\x00live+demo\x00video", sample)
		if err != nil {
			t.Fatalf("add sample %d: %v", i, err)
		}
		ids = append(ids, id)
		clock.now = clock.now.Add(time.Second)
		if i < len(samples)-1 {
			if depth, _ := wal.PendingDepth(); depth != 0 {
				t.Fatalf("open window visible to the forwarder after sample %d: depth %d", i, depth)
			}
		}
	}
	if ids[0] != ids[1] || ids[1] != ids[2] {
		t.Fatalf("samples of one window got different ids: %v", ids)
	}
	events := pendingBilling(t, wal)
	if len(events) != 1 {
		t.Fatalf("pending events = %d, want the one sealed window", len(events))
	}
	got := events[0]
	if got.GetDurationMs() != 4000 || got.GetSourceAdvancedMs() != 4002 || got.GetSinkAdvancedMs() != 4002 ||
		got.GetOutputFramesDelta() != 120 || got.GetInputFramesDelta() != 120 ||
		got.GetOutputBytesDelta() != 16400 || got.GetInputBytesDelta() != 32800 {
		t.Fatalf("window totals = %+v", got)
	}
	if !got.GetIsFinal() || got.GetOutputFrames() != 610 {
		t.Fatalf("window final=%v cumulative output frames=%d, want final and the last sample's 610", got.GetIsFinal(), got.GetOutputFrames())
	}
	if rtf := got.GetRtfIn(); rtf < 1.0004 || rtf > 1.0006 {
		t.Fatalf("window rtf_in = %v, want 4002/4000", rtf)
	}
}

// A window that turns final after it was opened is sealed once: the window-end sweep leaves
// nothing open or pending a retry.
func TestProcessBillingWindowFinalAfterOpenSealsOnce(t *testing.T) {
	wal := withTestTriggerWAL(t)
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	agg := newTestAggregator(clock)

	for _, final := range []bool{false, true} {
		if _, err := agg.add(wal, "pid-9", avSample(1000, 1000, 30, 4000, final)); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(time.Second)
	}
	if len(agg.open) != 0 {
		t.Fatalf("the sealed final window is still open: %d", len(agg.open))
	}
	clock.now = time.Unix(1_800_000_010, 0)
	agg.sweep(wal)
	agg.sweep(wal)
	if len(agg.open) != 0 || len(agg.unsealed) != 0 {
		t.Fatalf("after the window ended: open=%d unsealed=%d, want the sealed final window gone", len(agg.open), len(agg.unsealed))
	}
	events := pendingBilling(t, wal)
	if len(events) != 1 || events[0].GetDurationMs() != 2000 || !events[0].GetIsFinal() {
		t.Fatalf("sealed windows = %+v, want one final window of 2000 ms", events)
	}
}

// A window with no final sample is sealed when the window ends, and a sample in the next window
// starts a new event.
func TestProcessBillingWindowSealsAtWindowEnd(t *testing.T) {
	wal := withTestTriggerWAL(t)
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	agg := newTestAggregator(clock)

	for range 3 {
		if _, err := agg.add(wal, "pid-8", avSample(1000, 1000, 30, 4000, false)); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(time.Second)
	}
	agg.sweep(wal)
	if depth, _ := wal.PendingDepth(); depth != 0 {
		t.Fatalf("window sealed before it ended: depth %d", depth)
	}
	clock.now = time.Unix(1_800_000_010, 0)
	agg.sweep(wal)
	events := pendingBilling(t, wal)
	if len(events) != 1 || events[0].GetDurationMs() != 3000 {
		t.Fatalf("sealed windows = %+v, want one of 3000 ms", events)
	}
}

// Identical payloads never collapse: within a window they add up, and in different windows they
// are distinct events.
func TestProcessBillingWindowKeepsIdenticalPayloads(t *testing.T) {
	wal := withTestTriggerWAL(t)
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	agg := newTestAggregator(clock)

	silent := func() *ipcpb.MistTrigger { return avSample(1000, 0, 0, 0, false) }
	first, err := agg.add(wal, "pid-9\x00live+demo\x00audio", silent())
	if err != nil {
		t.Fatal(err)
	}
	if _, addErr := agg.add(wal, "pid-9\x00live+demo\x00audio", silent()); addErr != nil {
		t.Fatal(addErr)
	}
	clock.now = clock.now.Add(processBillingWindow)
	second, err := agg.add(wal, "pid-9\x00live+demo\x00audio", silent())
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("identical payloads in different windows share a source_event_id")
	}
	clock.now = clock.now.Add(processBillingWindow)
	agg.sweep(wal)
	events := pendingBilling(t, wal)
	if len(events) != 2 || events[0].GetDurationMs() != 2000 || events[1].GetDurationMs() != 1000 {
		t.Fatalf("windows = %+v, want 2000 ms then 1000 ms", events)
	}

	restarted := newTestAggregator(clock)
	clock.now = time.Unix(1_800_000_000, 0)
	again, err := restarted.add(wal, "pid-9\x00live+demo\x00audio", silent())
	if err != nil {
		t.Fatal(err)
	}
	if again == first {
		t.Fatal("a window after a restart reuses the id of a window delivered before it")
	}
}

// A window still open when Helmsman stops is on disk with every sample, and the next start seals it.
func TestProcessBillingWindowSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	wal, err := storage.NewTriggerWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	agg := newTestAggregator(clock)
	for range 4 {
		if _, addErr := agg.add(wal, "pid-10", avSample(1000, 1000, 30, 4000, false)); addErr != nil {
			t.Fatal(addErr)
		}
	}
	reopened, err := storage.NewTriggerWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	events := pendingBilling(t, reopened)
	if len(events) != 1 || events[0].GetDurationMs() != 4000 {
		t.Fatalf("recovered windows = %+v, want one of 4000 ms", events)
	}
}
