package triggers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/prometheus/client_golang/prometheus"
)

// Client-lifecycle batching constants. QoE samples from Helmsman's clients-API
// poll are coalesced per (tenant, stream, node) before forwarding to Decklog so
// the downstream pipeline (Decklog gRPC, Kafka, Periscope, ClickHouse) handles
// one record per batch instead of one per active viewer per poll cycle.
const (
	clientBatchFlushAge     = 1 * time.Second
	clientBatchFlushSamples = 1000
	clientBatchHardCap      = 5000
	clientBatchTickInterval = 250 * time.Millisecond
	clientBatchRetryBackoff = 100 * time.Millisecond
	clientBatchSendTimeout  = 2 * time.Second
)

type clientBatchKey struct {
	tenantID string
	streamID string
	nodeID   string
}

type clientBatchBucket struct {
	samples   []*pb.ClientLifecycleUpdate
	firstSeen time.Time
}

// clientLifecycleBatcher buffers enriched ClientLifecycleUpdate samples
// per (tenant, stream, node) and flushes them as ClientLifecycleBatch
// MistTriggers. Send failures are treated as lossy QoE telemetry: log,
// metric, single retry, drop. The trigger processor is never blocked on
// a flush — a stuck Decklog must not back-pressure into MistServer.
type clientLifecycleBatcher struct {
	mu      sync.Mutex
	buckets map[clientBatchKey]*clientBatchBucket

	send   func(*pb.MistTrigger) error
	logger logging.Logger
	drops  *prometheus.CounterVec // labels: reason

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	wg       sync.WaitGroup
}

func newClientLifecycleBatcher(send func(*pb.MistTrigger) error, logger logging.Logger, drops *prometheus.CounterVec) *clientLifecycleBatcher {
	b := &clientLifecycleBatcher{
		buckets: make(map[clientBatchKey]*clientBatchBucket),
		send:    send,
		logger:  logger,
		drops:   drops,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *clientLifecycleBatcher) run() {
	defer close(b.doneCh)
	ticker := time.NewTicker(clientBatchTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			b.flushAll()
			return
		case <-ticker.C:
			b.flushDue()
		}
	}
}

// Add buffers an enriched ClientLifecycleUpdate for batched forwarding.
// Never blocks the caller; if a key has reached the hard cap it flushes
// that key asynchronously and the new sample seeds the next batch.
func (b *clientLifecycleBatcher) Add(clu *pb.ClientLifecycleUpdate) {
	if clu == nil {
		return
	}
	key := clientBatchKey{
		tenantID: clu.GetTenantId(),
		streamID: clu.GetStreamId(),
		nodeID:   clu.GetNodeId(),
	}

	var toFlush []*pb.ClientLifecycleUpdate
	var toFlushKey clientBatchKey
	var toFlushStart time.Time

	b.mu.Lock()
	bucket, ok := b.buckets[key]
	if !ok {
		bucket = &clientBatchBucket{firstSeen: time.Now()}
		b.buckets[key] = bucket
	}
	bucket.samples = append(bucket.samples, clu)
	if len(bucket.samples) >= clientBatchHardCap {
		toFlush = bucket.samples
		toFlushKey = key
		toFlushStart = bucket.firstSeen
		delete(b.buckets, key)
	}
	b.mu.Unlock()

	if toFlush != nil {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.sendBatch(toFlushKey, toFlush, toFlushStart, time.Now())
		}()
	}
}

func (b *clientLifecycleBatcher) flushDue() {
	now := time.Now()
	var pending []flushItem

	b.mu.Lock()
	for k, bucket := range b.buckets {
		if len(bucket.samples) >= clientBatchFlushSamples || now.Sub(bucket.firstSeen) >= clientBatchFlushAge {
			pending = append(pending, flushItem{key: k, samples: bucket.samples, start: bucket.firstSeen, end: now})
			delete(b.buckets, k)
		}
	}
	b.mu.Unlock()

	for _, item := range pending {
		b.sendBatch(item.key, item.samples, item.start, item.end)
	}
}

func (b *clientLifecycleBatcher) flushAll() {
	now := time.Now()
	var pending []flushItem

	b.mu.Lock()
	for k, bucket := range b.buckets {
		pending = append(pending, flushItem{key: k, samples: bucket.samples, start: bucket.firstSeen, end: now})
		delete(b.buckets, k)
	}
	b.mu.Unlock()

	for _, item := range pending {
		b.sendBatch(item.key, item.samples, item.start, item.end)
	}
}

type flushItem struct {
	key     clientBatchKey
	samples []*pb.ClientLifecycleUpdate
	start   time.Time
	end     time.Time
}

func (b *clientLifecycleBatcher) sendBatch(key clientBatchKey, samples []*pb.ClientLifecycleUpdate, start, end time.Time) {
	if len(samples) == 0 {
		return
	}

	batch := &pb.ClientLifecycleBatch{
		InternalName: deriveInternalName(samples),
		NodeId:       key.nodeID,
		WindowStart:  start.Unix(),
		WindowEnd:    end.Unix(),
		Samples:      samples,
	}
	if key.tenantID != "" {
		t := key.tenantID
		batch.TenantId = &t
	}
	if key.streamID != "" {
		s := key.streamID
		batch.StreamId = &s
	}

	trigger := &pb.MistTrigger{
		TriggerType: "CLIENT_LIFECYCLE_BATCH",
		NodeId:      key.nodeID,
		Timestamp:   end.Unix(),
		Blocking:    false,
		TriggerPayload: &pb.MistTrigger_ClientLifecycleBatch{
			ClientLifecycleBatch: batch,
		},
	}
	if key.tenantID != "" {
		t := key.tenantID
		trigger.TenantId = &t
	}
	if key.streamID != "" {
		s := key.streamID
		trigger.StreamId = &s
	}

	err := b.send(trigger)
	if err == nil {
		return
	}

	time.Sleep(clientBatchRetryBackoff)
	if err2 := b.send(trigger); err2 == nil {
		b.bumpDrops("retry_succeeded")
		return
	} else {
		err = err2
	}

	b.bumpDrops("send_failed")
	b.logger.WithFields(logging.Fields{
		"tenant_id":    key.tenantID,
		"stream_id":    key.streamID,
		"node_id":      key.nodeID,
		"sample_count": len(samples),
		"error":        err,
	}).Warn("Dropped client lifecycle batch after retry; QoE samples lost")
}

func (b *clientLifecycleBatcher) bumpDrops(reason string) {
	if b.drops == nil {
		return
	}
	b.drops.WithLabelValues(reason).Inc()
}

// Shutdown drains pending batches synchronously. Returns when all in-flight
// sends have returned or ctx is done, whichever first.
func (b *clientLifecycleBatcher) Shutdown(ctx context.Context) error {
	b.stopOnce.Do(func() { close(b.stopCh) })
	select {
	case <-b.doneCh:
		done := make(chan struct{})
		go func() {
			b.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("client lifecycle batcher shutdown: %w", ctx.Err())
		}
	case <-ctx.Done():
		return fmt.Errorf("client lifecycle batcher shutdown: %w", ctx.Err())
	}
}

func deriveInternalName(samples []*pb.ClientLifecycleUpdate) string {
	for _, s := range samples {
		if name := s.GetInternalName(); name != "" {
			return name
		}
	}
	return ""
}
