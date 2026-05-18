package relay

import (
	"sync"
)

// blockFetchCoalescer coalesces concurrent cold-block fetches that
// share the same (asset, block_idx) — i.e., multiple viewers hitting
// the same uncached range within the same Helmsman process. The leader
// runs the actual S3 fetch + disk write + its own client stream; late
// arrivals wait for the leader's signal, then re-attempt a warm read
// of the now-cached block (or fall back to their own S3 fetch when the
// leader's cache write failed).
//
// Coalescing only applies when admission allows CacheToDisk — that's
// the regime where the leader will leave a warm file for late arrivals
// to read. Under CacheMemoryOnly there's no shared warm file to wait
// for, so callers bypass the coalescer and each issues its own S3
// fetch.
//
// Without coalescing, N concurrent viewers on the same cold block fan
// out into N S3 range GETs and N tmpfiles. On busy edges this
// multiplies S3 cost and disk churn for no playback benefit.
type blockFetchCoalescer struct {
	mu       sync.Mutex
	inflight map[string]*coldFetch
}

// coldFetch holds the rendezvous channel for a single (asset, block)
// fetch in flight. diskOk records whether the leader's disk-side write
// completed cleanly — late arrivals read this after done closes and
// decide whether to expect a warm file or re-fetch from S3.
type coldFetch struct {
	done   chan struct{}
	diskOk bool
}

func newBlockFetchCoalescer() *blockFetchCoalescer {
	return &blockFetchCoalescer{inflight: make(map[string]*coldFetch)}
}

// claim returns (leader, fetch). leader=true means this caller must
// execute the fetch and call finish() when done. leader=false means
// this caller joined an inflight fetch; wait on <-fetch.done before
// reading the now-warm block (or falling back to its own fetch when
// fetch.diskOk is false).
func (c *blockFetchCoalescer) claim(key string) (bool, *coldFetch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.inflight[key]; ok {
		return false, existing
	}
	cf := &coldFetch{done: make(chan struct{})}
	c.inflight[key] = cf
	return true, cf
}

// finish records the leader's outcome and wakes every late arrival
// waiting on the fetch's done channel. diskOk=true means the warm
// block file is on disk and late arrivals can read it directly;
// diskOk=false means they must re-fetch from S3.
func (c *blockFetchCoalescer) finish(key string, diskOk bool) {
	c.mu.Lock()
	cf, ok := c.inflight[key]
	if ok {
		delete(c.inflight, key)
	}
	c.mu.Unlock()
	if cf == nil {
		return
	}
	cf.diskOk = diskOk
	close(cf.done)
}
