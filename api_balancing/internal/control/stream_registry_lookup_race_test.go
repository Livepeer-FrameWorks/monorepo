package control

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLookupConcurrentMapMutationDoesNotPanic exercises the race that
// existed when lookup() shallow-copied StreamEntry and then mutated the
// shared Locations map under RLock while writers held Lock. Before the
// fix, `go test -race` reliably reports concurrent map writes / the Go
// runtime fatals with "concurrent map writes" on contention. After the
// fix lookup() deep-copies Locations before enrichment so reads can run
// in parallel with writes safely.
func TestLookupConcurrentMapMutationDoesNotPanic(t *testing.T) {
	r := NewStreamRegistry(&fakeCommodore{resp: nativeResp()}, "cluster-A", time.Minute)
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	// Install a live-presence source so the lookup() enrichment branch
	// runs (the path that mutates Locations).
	r.SetLivePresence(stubLivePresence{nodes: []string{"node-A"}, isLive: true})

	const (
		readers = 16
		writers = 8
		ops     = 2000
	)
	var wg sync.WaitGroup
	stop := atomic.Bool{}

	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_, _ = r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992")
			}
		}()
	}

	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				// Cycles AdmitAndReserve + MarkSourceInactive to keep
				// the local Location churning under the write lock.
				r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-A", nil)
				r.MarkSourceInactive("60546679b497415db2338cd5cae54992", "node-A")
			}
		}(i)
	}

	// Let workers churn long enough to hit a collision window.
	time.Sleep(200 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
}

type stubLivePresence struct {
	nodes  []string
	isLive bool
}

func (s stubLivePresence) LiveSourceNodes(_ string) (nodes []string, live bool) {
	return s.nodes, s.isLive
}
