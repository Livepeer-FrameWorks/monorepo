package state

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"

	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
)

// The startup consistent cut: EnableRedisSync captures the changelog tail,
// rehydrates from keys, then replays from the captured tail. A change logged
// before the capture must be covered by the key snapshot; a change logged
// after it must arrive via the changelog alone — even when no write-through
// key backs it, which is exactly the window the old pub/sub transport lost
// (RFC defect L3).
func TestChangelogSync_ConsistentCutOnStartup(t *testing.T) {
	mr := miniredis.RunT(t)
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	clientA := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientA.Close() })
	storeA := NewRedisStateStore(clientA, "test-cluster")

	// Before B exists: a normal write-through (key + changelog entry).
	s1 := &StreamState{InternalName: "s1", StreamName: "s1", NodeID: "node-1", TenantID: "tenant-1", Status: "live"}
	payload1, _ := json.Marshal(s1)
	if err := storeA.SetStream("s1", s1); err != nil {
		t.Fatalf("SetStream s1: %v", err)
	}
	if _, err := storeA.PublishStateChange(StateChange{InstanceID: "instance-a", Entity: StateEntityStream, Operation: StateOpUpsert, StreamName: "s1", Payload: payload1}); err != nil {
		t.Fatalf("publish s1: %v", err)
	}

	// Instance B starts: tail capture -> key rehydrate -> replay.
	smB := NewStreamStateManager()
	clientB := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientB.Close() })
	storeB := NewRedisStateStore(clientB, "test-cluster")
	if err := smB.EnableRedisSync(context.Background(), storeB, "instance-b", logger); err != nil {
		t.Fatalf("EnableRedisSync B: %v", err)
	}
	t.Cleanup(smB.Shutdown)

	// The pre-startup change is covered by the key snapshot.
	if ss := smB.GetStreamState("s1"); ss == nil || ss.TenantID != "tenant-1" {
		t.Fatalf("pre-startup change not rehydrated from keys: %+v", ss)
	}

	// After B is following: a change appended to the changelog ONLY (no
	// backing key) must still reach B — proves the log is the live
	// transport, not the key scan.
	s2 := &StreamState{InternalName: "s2", StreamName: "s2", NodeID: "node-2", TenantID: "tenant-2", Status: "live"}
	payload2, _ := json.Marshal(s2)
	if _, err := storeA.PublishStateChange(StateChange{InstanceID: "instance-a", Entity: StateEntityStream, Operation: StateOpUpsert, StreamName: "s2", Payload: payload2}); err != nil {
		t.Fatalf("publish s2: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if ss := smB.GetStreamState("s2"); ss != nil && ss.TenantID == "tenant-2" && ss.NodeID == "node-2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("changelog-only change did not reach instance B via replay")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A reader whose cursor falls behind the stream's retention window during
// an error/backoff period must get ErrChangelogGap (so the consumer re-runs
// its consistent cut) instead of silently skipping the trimmed range.
func TestChangelogSync_GapDetectedAfterErrorWindow(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	log := pkgredis.NewChangelog[StateChange](client, "test:gaplog", 3)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	first, err := log.Append(ctx, StateChange{StreamName: "s0"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	// Push the retention window well past the first entry.
	for i := 0; i < 10; i++ {
		if _, err := log.Append(ctx, StateChange{StreamName: "sN"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// Reader anchored below retention; an error window arms the gap check.
	mr.SetError("simulated outage")
	done := make(chan error, 1)
	go func() {
		done <- log.Read(ctx, first, func(string, StateChange) {})
	}()
	time.Sleep(200 * time.Millisecond)
	mr.SetError("")

	select {
	case readErr := <-done:
		if !errors.Is(readErr, pkgredis.ErrChangelogGap) {
			t.Fatalf("expected ErrChangelogGap, got %v", readErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Read did not detect the retention gap")
	}
}

// The same error window with a cursor still inside retention must NOT flag
// a gap — the reader resumes and keeps delivering.
func TestChangelogSync_NoGapFalsePositiveAfterError(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	log := pkgredis.NewChangelog[StateChange](client, "test:gaplog2", 100)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	tail, err := log.Append(ctx, StateChange{StreamName: "s0"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	got := make(chan string, 8)
	done := make(chan error, 1)
	mr.SetError("simulated outage")
	go func() {
		done <- log.Read(ctx, tail, func(id string, _ StateChange) { got <- id })
	}()
	time.Sleep(200 * time.Millisecond)
	mr.SetError("")

	want, err := log.Append(ctx, StateChange{StreamName: "s1"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	select {
	case id := <-got:
		if id != want {
			t.Fatalf("delivered %q, want %q", id, want)
		}
	case readErr := <-done:
		t.Fatalf("Read stopped unexpectedly: %v", readErr)
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not resume after the error window")
	}
}

// A failed write-through key delete is retried in the background until the
// key is gone, so a restart's rehydrate can't resurrect it.
func TestRetryRedisDeleteAsync_RetriesUntilSuccess(t *testing.T) {
	old := redisDeleteRetryBackoff
	redisDeleteRetryBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { redisDeleteRetryBackoff = old })

	var calls atomic.Int32
	done := make(chan struct{})
	retryRedisDeleteAsync("stream", "s1", func() error {
		if calls.Add(1) < 2 {
			return errors.New("redis still down")
		}
		close(done)
		return nil
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("delete retry never succeeded")
	}
}

// Replay after restart: changes made while an instance is down are fully
// reflected when it comes back (key snapshot + changelog cut), including
// deletes — the case at-most-once pub/sub lost forever.
func TestChangelogSync_RestartConvergesIncludingDeletes(t *testing.T) {
	mr := miniredis.RunT(t)
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	clientA := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientA.Close() })
	storeA := NewRedisStateStore(clientA, "test-cluster")

	smA := NewStreamStateManager()
	if err := smA.EnableRedisSync(context.Background(), storeA, "instance-a", logger); err != nil {
		t.Fatalf("EnableRedisSync A: %v", err)
	}
	t.Cleanup(smA.Shutdown)

	// B's first life.
	smB1 := NewStreamStateManager()
	clientB := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientB.Close() })
	storeB := NewRedisStateStore(clientB, "test-cluster")
	if err := smB1.EnableRedisSync(context.Background(), storeB, "instance-b", logger); err != nil {
		t.Fatalf("EnableRedisSync B1: %v", err)
	}

	if err := smA.UpdateStreamFromBuffer("gone", "gone", "node-1", "tenant-1", "FULL", ""); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	if err := smA.UpdateStreamFromBuffer("kept", "kept", "node-1", "tenant-1", "FULL", ""); err != nil {
		t.Fatalf("seed stream: %v", err)
	}

	// B goes down.
	smB1.Shutdown()

	// While B is down: one stream ends (delete), key removed.
	smA.RemoveStream("gone")

	// B restarts (fresh process: empty memory, fresh watermarks).
	smB2 := NewStreamStateManager()
	clientB2 := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientB2.Close() })
	storeB2 := NewRedisStateStore(clientB2, "test-cluster")
	if err := smB2.EnableRedisSync(context.Background(), storeB2, "instance-b", logger); err != nil {
		t.Fatalf("EnableRedisSync B2: %v", err)
	}
	t.Cleanup(smB2.Shutdown)

	if ss := smB2.GetStreamState("kept"); ss == nil || ss.TenantID != "tenant-1" {
		t.Fatalf("surviving stream not present after restart: %+v", ss)
	}
	if ss := smB2.GetStreamState("gone"); ss != nil {
		t.Fatalf("delete made while down was not honored after restart: %+v", ss)
	}
}
