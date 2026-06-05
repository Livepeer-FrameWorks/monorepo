package triggers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

func testLogger() logging.Logger {
	l := logrus.New()
	l.SetOutput(devNull{})
	return l
}

type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

func sampleClu(tenantID, streamID, nodeID, sessionID string) *ipcpb.ClientLifecycleUpdate {
	t := tenantID
	s := streamID
	clu := &ipcpb.ClientLifecycleUpdate{NodeId: nodeID}
	if tenantID != "" {
		clu.TenantId = &t
	}
	if streamID != "" {
		clu.StreamId = &s
	}
	if sessionID != "" {
		clu.SessionId = &sessionID
	}
	return clu
}

// recordingSender captures every batch the batcher tries to send.
type recordingSender struct {
	mu       sync.Mutex
	batches  []*ipcpb.ClientLifecycleBatch
	failNext atomic.Int32 // each call decrements; when > 0 the call returns an error
}

func (r *recordingSender) send(trigger *ipcpb.MistTrigger) error {
	if r.failNext.Load() > 0 {
		r.failNext.Add(-1)
		return fmt.Errorf("synthetic send failure")
	}
	clb, ok := trigger.GetTriggerPayload().(*ipcpb.MistTrigger_ClientLifecycleBatch)
	if !ok {
		return fmt.Errorf("not a ClientLifecycleBatch trigger: %T", trigger.GetTriggerPayload())
	}
	r.mu.Lock()
	r.batches = append(r.batches, clb.ClientLifecycleBatch)
	r.mu.Unlock()
	return nil
}

func (r *recordingSender) snapshot() []*ipcpb.ClientLifecycleBatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*ipcpb.ClientLifecycleBatch, len(r.batches))
	copy(out, r.batches)
	return out
}

func TestBatcherFlushOnAge(t *testing.T) {
	sender := &recordingSender{}
	b := newClientLifecycleBatcher(sender.send, testLogger(), nil)
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })

	b.Add(sampleClu("tenant-1", "stream-1", "node-1", "sess-1"))

	// Flush age is 1s, ticker is 250ms — wait comfortably past one flush age.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sender.snapshot()) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	got := sender.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 batch flushed on age, got %d", len(got))
	}
	if len(got[0].Samples) != 1 {
		t.Fatalf("expected 1 sample in batch, got %d", len(got[0].Samples))
	}
}

func TestBatcherFlushOnSize(t *testing.T) {
	sender := &recordingSender{}
	b := newClientLifecycleBatcher(sender.send, testLogger(), nil)
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })

	// Submit exactly flushSamples (1000) — the next ticker pass should flush.
	for i := range clientBatchFlushSamples {
		b.Add(sampleClu("tenant-1", "stream-1", "node-1", fmt.Sprintf("sess-%d", i)))
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sender.snapshot()) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	got := sender.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 batch flushed on size, got %d", len(got))
	}
	if len(got[0].Samples) != clientBatchFlushSamples {
		t.Fatalf("expected %d samples in batch, got %d", clientBatchFlushSamples, len(got[0].Samples))
	}
}

func TestBatcherHardCapForceFlush(t *testing.T) {
	sender := &recordingSender{}
	b := newClientLifecycleBatcher(sender.send, testLogger(), nil)
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })

	// Submit exactly hardCap samples. The Add call that reaches the cap should
	// dispatch the batch asynchronously without waiting for the ticker.
	for i := range clientBatchHardCap {
		b.Add(sampleClu("tenant-1", "stream-1", "node-1", fmt.Sprintf("sess-%d", i)))
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := sender.snapshot()
		if len(got) >= 1 && len(got[0].Samples) == clientBatchHardCap {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	got := sender.snapshot()
	if len(got) < 1 || len(got[0].Samples) != clientBatchHardCap {
		t.Fatalf("expected hard-cap force-flush of %d samples, got batches=%d first_size=%d", clientBatchHardCap, len(got), func() int {
			if len(got) == 0 {
				return 0
			}
			return len(got[0].Samples)
		}())
	}
}

func TestBatcherMultipleKeysIndependent(t *testing.T) {
	sender := &recordingSender{}
	b := newClientLifecycleBatcher(sender.send, testLogger(), nil)

	b.Add(sampleClu("tenant-A", "stream-1", "node-1", "sess-1"))
	b.Add(sampleClu("tenant-A", "stream-2", "node-1", "sess-2")) // different stream → different key
	b.Add(sampleClu("tenant-B", "stream-1", "node-1", "sess-3")) // different tenant → different key

	// Shutdown drains; assert three separate batches surfaced, one per key.
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	got := sender.snapshot()
	if len(got) != 3 {
		t.Fatalf("expected 3 independent batches across (tenant, stream, node) keys, got %d", len(got))
	}
}

func TestBatcherShutdownDrains(t *testing.T) {
	sender := &recordingSender{}
	b := newClientLifecycleBatcher(sender.send, testLogger(), nil)

	const n = 50
	for i := range n {
		b.Add(sampleClu("tenant-1", "stream-1", "node-1", fmt.Sprintf("sess-%d", i)))
	}

	// Shutdown must flush remaining samples even though neither flushAge nor
	// flushSamples thresholds have been hit.
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	got := sender.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected single drained batch, got %d", len(got))
	}
	if len(got[0].Samples) != n {
		t.Fatalf("expected %d drained samples, got %d", n, len(got[0].Samples))
	}
}

func TestBatcherRetryThenDrop(t *testing.T) {
	sender := &recordingSender{}
	sender.failNext.Store(3) // both initial send and retry fail; second batch starts clean
	b := newClientLifecycleBatcher(sender.send, testLogger(), nil)
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })

	b.Add(sampleClu("tenant-1", "stream-1", "node-1", "sess-1"))

	// Wait past flushAge so the batcher tries to send (and both attempts fail).
	time.Sleep(clientBatchFlushAge + 2*clientBatchTickInterval + clientBatchRetryBackoff)

	// Drop happened; sender recorded zero successful batches. Adding more samples
	// after the drop still works — the batcher is not in a broken state.
	if len(sender.snapshot()) != 0 {
		t.Fatalf("expected zero successful batches after retry exhaustion, got %d", len(sender.snapshot()))
	}

	b.Add(sampleClu("tenant-1", "stream-1", "node-1", "sess-2"))
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if len(sender.snapshot()) != 1 {
		t.Fatalf("expected one batch after recovery, got %d", len(sender.snapshot()))
	}
}

func TestBatcherAddNeverBlocks(t *testing.T) {
	// A send that hangs forever must not block Add(). The batcher uses a
	// dedicated goroutine for the hard-cap path; Add() should return promptly.
	hang := make(chan struct{})
	defer close(hang)

	hangingSender := func(*ipcpb.MistTrigger) error {
		<-hang
		return nil
	}
	b := newClientLifecycleBatcher(hangingSender, testLogger(), nil)
	t.Cleanup(func() {
		// Best-effort: don't wait forever for the hanging send to finish.
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_ = b.Shutdown(ctx)
	})

	done := make(chan struct{})
	go func() {
		for i := range clientBatchHardCap + 10 {
			b.Add(sampleClu("tenant-1", "stream-1", "node-1", fmt.Sprintf("sess-%d", i)))
		}
		close(done)
	}()

	select {
	case <-done:
		// Add() loop completed without being blocked by the hanging send.
	case <-time.After(2 * time.Second):
		t.Fatalf("Add() blocked on hung send — back-pressure leaked into caller")
	}
}
