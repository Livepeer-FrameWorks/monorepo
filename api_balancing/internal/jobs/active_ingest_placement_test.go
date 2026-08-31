package jobs

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

type syncCall struct {
	clusterID string
	renew     []*commodorepb.ActiveIngestStream
	release   []*commodorepb.ActiveIngestStream
}

type recordingSyncer struct {
	mu    sync.Mutex
	calls []syncCall
	err   error
}

func (r *recordingSyncer) SyncActiveIngestPlacement(_ context.Context, clusterID string, renew, release []*commodorepb.ActiveIngestStream) (*commodorepb.SyncActiveIngestPlacementResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, syncCall{clusterID: clusterID, renew: renew, release: release})
	if r.err != nil {
		return nil, r.err
	}
	return &commodorepb.SyncActiveIngestPlacementResponse{Renewed: int32(len(renew))}, nil
}

func (r *recordingSyncer) snapshot() []syncCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]syncCall(nil), r.calls...)
}

func placementJob(syncer ActiveIngestPlacementSyncer, sources func(context.Context) []LiveIngest) *ActiveIngestPlacementJob {
	return NewActiveIngestPlacementJob(ActiveIngestPlacementConfig{
		Logger:  logging.NewLogger(),
		Syncer:  syncer,
		Sources: func(ctx context.Context) ([]LiveIngest, error) { return sources(ctx), nil },
	})
}

// renewAndWait ticks once and waits for that tick's per-cluster work. The job
// itself never waits — a tick must not be held up by any one cluster.
func renewAndWait(j *ActiveIngestPlacementJob) {
	j.renew()
	j.wg.Wait()
}

// Each entry names the cluster PUSH_REWRITE claimed under, so one call carries
// every cluster. Renewing everything under the Foghorn's own cluster would
// match no row and let every claim lapse.
func TestActiveIngestPlacementJob_EntriesCarryTheirPublishingCluster(t *testing.T) {
	syncer := &recordingSyncer{}
	job := placementJob(syncer, func(context.Context) []LiveIngest {
		return []LiveIngest{
			{TenantID: "t1", InternalName: "stream-a", ClusterID: "demo-media", ClaimToken: "c1"},
			{TenantID: "t2", InternalName: "stream-b", ClusterID: "demo-media", ClaimToken: "c2"},
			{TenantID: "t1", InternalName: "stream-c", ClusterID: "other-media", ClaimToken: "c3"},
		}
	})

	renewAndWait(job)

	calls := syncer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected a single bulk sync, got %d", len(calls))
	}
	if calls[0].clusterID != "" {
		t.Fatalf("request-level cluster = %q; entries carry their own", calls[0].clusterID)
	}
	byCluster := map[string]int{}
	for _, s := range calls[0].renew {
		byCluster[s.GetClusterId()]++
	}
	if byCluster["demo-media"] != 2 || byCluster["other-media"] != 1 {
		t.Fatalf("unexpected per-entry clusters: %v", byCluster)
	}
}

func TestActiveIngestPlacementJob_EnumerationFailureSkipsRenewal(t *testing.T) {
	syncer := &recordingSyncer{}
	job := NewActiveIngestPlacementJob(ActiveIngestPlacementConfig{
		Logger: logging.NewLogger(),
		Syncer: syncer,
		Sources: func(context.Context) ([]LiveIngest, error) {
			return nil, context.DeadlineExceeded
		},
	})

	job.renew()
	if calls := syncer.snapshot(); len(calls) != 0 {
		t.Fatalf("enumeration failure produced renewal calls: %+v", calls)
	}
}

// A tick costs a fixed number of calls regardless of how many virtual clusters
// this Foghorn serves. A call per cluster could not re-assert a large fleet
// inside one 30-second lease window, which is the failure this job prevents.
func TestActiveIngestPlacementJob_CallCountIsIndependentOfClusterCount(t *testing.T) {
	syncer := &recordingSyncer{}
	const clusters = 300
	job := placementJob(syncer, func(context.Context) []LiveIngest {
		live := make([]LiveIngest, 0, clusters)
		for i := range clusters {
			id := strconv.Itoa(i)
			live = append(live, LiveIngest{
				TenantID: "t1", InternalName: "stream-" + id,
				ClusterID: "media-" + id, ClaimToken: "conn-" + id,
			})
		}
		return live
	})

	renewAndWait(job)

	calls := syncer.snapshot()
	if len(calls) != 1 {
		t.Fatalf("%d clusters produced %d calls; want one batch", clusters, len(calls))
	}
	if len(calls[0].renew) != clusters {
		t.Fatalf("renewed %d of %d publishers", len(calls[0].renew), clusters)
	}
}

// A publisher whose cluster, tenant, or owning connection could not be
// attributed is skipped: PUSH_REWRITE would not have claimed placement for it
// either, and an unowned entry is refused by Commodore anyway.
func TestActiveIngestPlacementJob_SkipsUnattributedStreams(t *testing.T) {
	syncer := &recordingSyncer{}
	job := placementJob(syncer, func(context.Context) []LiveIngest {
		return []LiveIngest{
			{TenantID: "t1", InternalName: "no-cluster", ClaimToken: "c1"},
			{InternalName: "no-tenant", ClusterID: "demo-media", ClaimToken: "c2"},
			{TenantID: "t1", ClusterID: "demo-media", ClaimToken: "c3"},
			{TenantID: "t1", InternalName: "no-owner", ClusterID: "demo-media"},
		}
	})

	renewAndWait(job)

	if calls := syncer.snapshot(); len(calls) != 0 {
		t.Fatalf("unattributed publishers were synced: %+v", calls)
	}
}

// Nothing being published means no call at all; this ticks constantly.
func TestActiveIngestPlacementJob_NoLivePublishersIsNoCall(t *testing.T) {
	syncer := &recordingSyncer{}
	job := placementJob(syncer, func(context.Context) []LiveIngest { return nil })

	renewAndWait(job)

	if calls := syncer.snapshot(); len(calls) != 0 {
		t.Fatalf("empty source set still synced: %+v", calls)
	}
}

// A failed sync is recoverable on the next tick: placement lapses on the lease
// clock rather than corrupting, so the job neither retries in-tick nor aborts.
func TestActiveIngestPlacementJob_FailedSyncIsNotFatal(t *testing.T) {
	syncer := &recordingSyncer{err: errors.New("commodore down")}
	var fenced atomic.Int32
	job := NewActiveIngestPlacementJob(ActiveIngestPlacementConfig{
		Logger: logging.NewLogger(),
		Syncer: syncer,
		Sources: func(context.Context) ([]LiveIngest, error) {
			return []LiveIngest{
				{TenantID: "t1", InternalName: "stream-a", ClusterID: "demo-media", ClaimToken: "c1"},
				{TenantID: "t1", InternalName: "stream-b", ClusterID: "other-media", ClaimToken: "c2"},
			}, nil
		},
		ClaimLost: func(context.Context, *commodorepb.ActiveIngestStream) error {
			fenced.Add(1)
			return nil
		},
	})

	renewAndWait(job)

	if got := len(syncer.snapshot()); got != 1 {
		t.Fatalf("attempted %d calls, want one", got)
	}
	if got := fenced.Load(); got != 0 {
		t.Fatalf("control-plane transport failure fenced %d existing publishers; want zero", got)
	}
}

// More live publishers in one cluster than a payload allows are split, not
// truncated: a silently dropped tail would expire those publishers' claims.
func TestActiveIngestPlacementJob_BatchesLargeClusters(t *testing.T) {
	syncer := &recordingSyncer{}
	total := activeIngestPlacementBatch + 3
	job := placementJob(syncer, func(context.Context) []LiveIngest {
		live := make([]LiveIngest, 0, total)
		for i := range total {
			live = append(live, LiveIngest{
				TenantID:     "t1",
				InternalName: string(rune('a'+i%26)) + string(rune('0'+i/26)),
				ClusterID:    "demo-media",
				ClaimToken:   "conn-" + strconv.Itoa(i),
			})
		}
		return live
	})

	renewAndWait(job)

	calls := syncer.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(calls))
	}
	if got := len(calls[0].renew) + len(calls[1].renew); got != total {
		t.Fatalf("renewed %d streams across batches, want %d", got, total)
	}
}

// A cluster large enough to need several batches must not let one stalled
// batch strand the publishers behind it: at 500 streams to a batch that is
// thousands of claims lapsing while their publishers are still connected.
func TestActiveIngestPlacementJob_StalledBatchDoesNotStrandLaterBatches(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	var mu sync.Mutex
	var renewed int
	first := make(chan struct{}, 1)
	syncer := syncerFunc(func(ctx context.Context, _ string, renew, _ []*commodorepb.ActiveIngestStream) (*commodorepb.SyncActiveIngestPlacementResponse, error) {
		// The batch containing the seeded first stream stalls; the rest proceed.
		stall := false
		for _, s := range renew {
			if s.GetInternalName() == "stream-0" {
				stall = true
				break
			}
		}
		if stall {
			select {
			case first <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, ctx.Err()
		}
		mu.Lock()
		renewed += len(renew)
		mu.Unlock()
		return &commodorepb.SyncActiveIngestPlacementResponse{Renewed: int32(len(renew))}, nil
	})

	total := activeIngestPlacementBatch * 2
	job := placementJob(syncer, func(context.Context) []LiveIngest {
		live := make([]LiveIngest, 0, total)
		for i := range total {
			live = append(live, LiveIngest{
				TenantID:     "t1",
				InternalName: "stream-" + strconv.Itoa(i),
				ClusterID:    "demo-media",
				ClaimToken:   "conn-" + strconv.Itoa(i),
			})
		}
		return live
	})

	job.renew()

	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		got := renewed
		mu.Unlock()
		if got >= activeIngestPlacementBatch {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("only %d streams renewed while an earlier batch stalled", got)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// syncerFunc adapts a function to ActiveIngestPlacementSyncer.
type syncerFunc func(context.Context, string, []*commodorepb.ActiveIngestStream, []*commodorepb.ActiveIngestStream) (*commodorepb.SyncActiveIngestPlacementResponse, error)

func (f syncerFunc) SyncActiveIngestPlacement(ctx context.Context, clusterID string, renew, release []*commodorepb.ActiveIngestStream) (*commodorepb.SyncActiveIngestPlacementResponse, error) {
	return f(ctx, clusterID, renew, release)
}

// Concurrency is capped. Unbounded fan-out would put one RPC and UPDATE per 500
// publishers on Commodore simultaneously, and a saturated control plane fails
// them together — a whole tick's claims lapsing at once is the outage this job
// exists to prevent.
func TestActiveIngestPlacementJob_BoundsConcurrentSyncs(t *testing.T) {
	var mu sync.Mutex
	var inFlight, peak int
	release := make(chan struct{})

	syncer := syncerFunc(func(_ context.Context, _ string, renew, _ []*commodorepb.ActiveIngestStream) (*commodorepb.SyncActiveIngestPlacementResponse, error) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return &commodorepb.SyncActiveIngestPlacementResponse{Renewed: int32(len(renew))}, nil
	})

	// Enough publishers for well over the concurrency cap in batches.
	total := activeIngestPlacementBatch * (activeIngestPlacementConcurrency + 8)
	job := placementJob(syncer, func(context.Context) []LiveIngest {
		live := make([]LiveIngest, 0, total)
		for i := range total {
			id := strconv.Itoa(i)
			live = append(live, LiveIngest{
				TenantID: "t1", InternalName: "stream-" + id,
				ClusterID: "demo-media", ClaimToken: "conn-" + id,
			})
		}
		return live
	})

	done := make(chan struct{})
	go func() {
		renewAndWait(job)
		close(done)
	}()

	// Let the cap fill, then confirm nothing beyond it started.
	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		got := inFlight
		mu.Unlock()
		if got >= activeIngestPlacementConcurrency {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d syncs started; expected the cap to fill", got)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	observed := peak
	mu.Unlock()
	if observed > activeIngestPlacementConcurrency {
		t.Fatalf("%d syncs ran at once, cap is %d", observed, activeIngestPlacementConcurrency)
	}

	close(release)
	<-done
}

// The binding deadline is a JUST-ADMITTED publisher's first renewal: it can
// arrive right after a tick's snapshot, wait out that tick, then land in the
// final wave of the next — two budgets. That must fit the lease with margin,
// since a lease expiring exactly as renewal lands is already lost.
//
// Pinned as a test because the two halves live in different services — the
// lease is Commodore's, the cadence is Foghorn's — and because a cadence sized
// against tick duration rather than against this deadline can exceed the lease
// while every individual tick still finishes comfortably inside its budget.
func TestActiveIngestPlacementSchedule_RenewsWellInsideTheLease(t *testing.T) {
	if activeIngestPlacementCapacity != streamident.MaxPublishersPerFoghorn {
		t.Fatalf("renewal capacity %d and admission ceiling %d diverged",
			activeIngestPlacementCapacity, streamident.MaxPublishersPerFoghorn)
	}
	if activeIngestPlacementMaxFirstRenewal >= streamident.ActiveIngestLease {
		t.Fatalf("worst-case first renewal %s does not fit the %s lease",
			activeIngestPlacementMaxFirstRenewal, streamident.ActiveIngestLease)
	}
	if margin := streamident.ActiveIngestLease - activeIngestPlacementMaxFirstRenewal; margin < streamident.ActiveIngestLease/4 {
		t.Fatalf("renewal margin %s is under a quarter of the %s lease",
			margin, streamident.ActiveIngestLease)
	}
	// An already-rotating claim's gap must also fit, and is the smaller case.
	if gap := activeIngestPlacementInterval + activeIngestPlacementTickBudget; gap >= activeIngestPlacementMaxFirstRenewal {
		t.Fatalf("steady-state gap %s should be under the first-renewal bound %s",
			gap, activeIngestPlacementMaxFirstRenewal)
	}
	// The capacity figure must be reachable within the budget it is derived
	// from, or the over-capacity warning names a number the job cannot meet.
	// Enumeration runs before any batch does, so capacity is bounded by what is
	// left of the tick after it, not by the whole tick.
	waves := (activeIngestPlacementCapacity + activeIngestPlacementBatch*activeIngestPlacementConcurrency - 1) /
		(activeIngestPlacementBatch * activeIngestPlacementConcurrency)
	if got := time.Duration(waves) * activeIngestPlacementCallTimeout; got > activeIngestPlacementBatchBudget {
		t.Fatalf("re-asserting %d publishers takes %s, over the %s left for batches",
			activeIngestPlacementCapacity, got, activeIngestPlacementBatchBudget)
	}
	// Both halves of a tick must fit the budget the lease sizing assumes.
	if total := activeIngestPlacementEnumerationBudget + activeIngestPlacementBatchBudget; total > activeIngestPlacementTickBudget {
		t.Fatalf("enumeration plus batches is %s, over the %s tick budget", total, activeIngestPlacementTickBudget)
	}
}

// Enumeration is bounded by the job, not by the enumerator. An enumerator that
// blocks past its share must not eat the batch budget: the tick would then
// start its calls with no room for them inside the lease window.
func TestActiveIngestPlacementJob_BoundsSourceEnumeration(t *testing.T) {
	syncer := &recordingSyncer{}
	var deadlineIn time.Duration
	job := placementJob(syncer, func(ctx context.Context) []LiveIngest {
		if dl, ok := ctx.Deadline(); ok {
			deadlineIn = time.Until(dl)
		}
		<-ctx.Done()
		return nil
	})
	start := time.Now()
	renewAndWait(job)

	if deadlineIn <= 0 || deadlineIn > activeIngestPlacementEnumerationBudget {
		t.Fatalf("enumeration deadline %s is not the %s budget", deadlineIn, activeIngestPlacementEnumerationBudget)
	}
	if elapsed := time.Since(start); elapsed > activeIngestPlacementTickBudget {
		t.Fatalf("a blocked enumeration held the tick for %s, past the %s budget", elapsed, activeIngestPlacementTickBudget)
	}
}

// Renewal order is stable, so a claim occupies the same batch position every
// tick. Without that the worst-case gap above happens by chance — a claim
// renewed first in one tick and last in the next — rather than under load.
func TestActiveIngestPlacementJob_RenewsInStableOrder(t *testing.T) {
	sources := []LiveIngest{
		{TenantID: "t2", InternalName: "b", ClusterID: "c", ClaimToken: "k1"},
		{TenantID: "t1", InternalName: "z", ClusterID: "c", ClaimToken: "k2"},
		{TenantID: "t1", InternalName: "a", ClusterID: "c", ClaimToken: "k3"},
	}
	var first []string
	for range 5 {
		syncer := &recordingSyncer{}
		// Shuffle the source order the way map iteration would.
		sources = append(sources[1:], sources[0])
		job := placementJob(syncer, func(context.Context) []LiveIngest { return sources })
		renewAndWait(job)

		var order []string
		for _, s := range syncer.snapshot()[0].renew {
			order = append(order, s.GetTenantId()+"/"+s.GetInternalName())
		}
		if first == nil {
			first = order
			continue
		}
		if strings.Join(order, ",") != strings.Join(first, ",") {
			t.Fatalf("renewal order changed between ticks: %v then %v", first, order)
		}
	}
}

// A publisher admitted after a tick took its snapshot must be picked up by the
// very next tick — that is what makes the two-budget bound above the real worst
// case rather than an optimistic one. Nothing about arrival order may defer it
// further.
func TestActiveIngestPlacementJob_RenewsPublishersAdmittedAfterASnapshot(t *testing.T) {
	syncer := &recordingSyncer{}
	var mu sync.Mutex
	live := []LiveIngest{
		{TenantID: "t1", InternalName: "already-live", ClusterID: "demo-media", ClaimToken: "k1"},
	}
	job := placementJob(syncer, func(context.Context) []LiveIngest {
		mu.Lock()
		defer mu.Unlock()
		return append([]LiveIngest(nil), live...)
	})

	renewAndWait(job)

	// Admitted between ticks — sorts BEFORE the existing publisher, so this also
	// covers an arrival displacing others' batch positions.
	mu.Lock()
	live = append(live, LiveIngest{
		TenantID: "t1", InternalName: "aaa-just-admitted", ClusterID: "demo-media", ClaimToken: "k2",
	})
	mu.Unlock()

	renewAndWait(job)

	calls := syncer.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected one call per tick, got %d", len(calls))
	}
	var renewed []string
	for _, s := range calls[1].renew {
		renewed = append(renewed, s.GetInternalName())
	}
	if len(renewed) != 2 || renewed[0] != "aaa-just-admitted" {
		t.Fatalf("second tick renewed %v; the newly admitted publisher must be included", renewed)
	}
}
