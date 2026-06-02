package relay

import (
	"sync"
	"time"

	"frameworks/api_sidecar/internal/control"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// defrostFlushInterval bounds how long a single asset's cold read-through
// bytes accumulate before the relay emits one ACTION_CACHED storage-lifecycle
// event for it. Coalescing the many per-block fetches of a cold fill into one
// event per asset per window keeps the serialized control stream from being
// flooded — in steady state an asset is read cold once (the first viewer warms
// it), so emitted events track distinct cold-asset reads, not range requests.
const defrostFlushInterval = 30 * time.Second

type defrostEntry struct {
	assetType string
	hash      string
	bytes     int64
	reads     int64
	firstSeen time.Time
}

func (e *defrostEntry) lifecycle() *pb.StorageLifecycleData {
	return &pb.StorageLifecycleData{
		Action:    pb.StorageLifecycleData_ACTION_CACHED,
		AssetType: e.assetType,
		AssetHash: e.hash,
		SizeBytes: uint64(e.bytes),
	}
}

// defrostAggregator coalesces cold S3 read-through bytes per asset and emits
// them as ACTION_CACHED events (Foghorn enriches tenant/stream; the ingest
// pipeline writes them to ClickHouse storage_events with action='cached').
type defrostAggregator struct {
	mu            sync.Mutex
	acc           map[string]*defrostEntry
	flushInterval time.Duration
	timer         *time.Timer
	emit          func(*pb.StorageLifecycleData) error
}

func newDefrostAggregator() *defrostAggregator {
	return newDefrostAggregatorWithInterval(defrostFlushInterval, control.SendStorageLifecycle)
}

func newDefrostAggregatorWithInterval(interval time.Duration, emit func(*pb.StorageLifecycleData) error) *defrostAggregator {
	if interval <= 0 {
		interval = defrostFlushInterval
	}
	if emit == nil {
		emit = control.SendStorageLifecycle
	}
	return &defrostAggregator{
		acc:           make(map[string]*defrostEntry),
		flushInterval: interval,
		emit:          emit,
	}
}

// defrostRecorderFor returns a closure that records cold-read bytes for one
// asset. Returns nil when aggregation is unavailable (no aggregator or no
// hash), which the hot path treats as a no-op.
func (s *Server) defrostRecorderFor(kind, hash string) func(int64) {
	if s.defrost == nil || hash == "" {
		return nil
	}
	agg := s.defrost
	return func(n int64) { agg.record(kind, hash, n) }
}

func (a *defrostAggregator) record(kind, hash string, n int64) {
	if n <= 0 {
		return
	}
	now := time.Now()
	key := kind + "|" + hash

	a.mu.Lock()
	e := a.acc[key]
	if e == nil {
		e = &defrostEntry{assetType: kind, hash: hash, firstSeen: now}
		a.acc[key] = e
	}
	e.bytes += n
	e.reads++

	a.scheduleLocked(now)
	a.mu.Unlock()
}

func (a *defrostAggregator) flushDue() {
	now := time.Now()
	var due []*pb.StorageLifecycleData

	a.mu.Lock()
	for k, entry := range a.acc {
		if now.Sub(entry.firstSeen) >= a.flushInterval {
			due = append(due, entry.lifecycle())
			delete(a.acc, k)
		}
	}
	a.scheduleLocked(now)
	a.mu.Unlock()

	for _, d := range due {
		_ = a.emit(d) //nolint:errcheck // best-effort analytics emit; freeze reconciler is the backstop
	}
}

func (a *defrostAggregator) scheduleLocked(now time.Time) {
	if len(a.acc) == 0 {
		if a.timer != nil {
			a.timer.Stop()
			a.timer = nil
		}
		return
	}

	var next time.Time
	for _, entry := range a.acc {
		dueAt := entry.firstSeen.Add(a.flushInterval)
		if next.IsZero() || dueAt.Before(next) {
			next = dueAt
		}
	}
	delay := next.Sub(now)
	if delay < 0 {
		delay = 0
	}
	if a.timer == nil {
		a.timer = time.AfterFunc(delay, a.flushDue)
		return
	}
	a.timer.Reset(delay)
}
