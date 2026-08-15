package jobs

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

// Renewal's schedule is derived from the lease it defends.
//
// The binding deadline belongs to a publisher that has just been ADMITTED, not
// to one already in the rotation. Its claim starts ageing at PUSH_REWRITE, and
// the worst case is: it arrives moments after a tick took its source snapshot,
// so it waits out that whole tick, then lands in the final wave of the next —
// two full tick budgets before its first renewal. An already-renewed claim's
// gap is smaller (interval plus one tick), so sizing against the new publisher
// covers both.
//
// A tick is enumeration followed by batches, and the budget covers both. The
// enumeration queries ingest sessions, so it is not free and a tick that spent
// its whole budget there would renew nothing; it gets a fixed slice, and the
// batches get what remains.
//
// A tick sends ceil(publishers/batch) calls — entries carry their own cluster,
// so the count follows publisher count, not how many virtual clusters this
// Foghorn serves. Those run at most concurrency at a time, each bounded by
// callTimeout, so the batches take at worst ceil(batches/concurrency) *
// callTimeout and capacity is what fits in the batch budget.
//
// Batches renew in a stable order so a claim holds its position across ticks
// while the set is unchanged. Arrivals and departures still shift positions,
// which is exactly why the bound is derived from the worst case rather than
// from observed placement.
//
// The concurrency bound matters as much as the budget: unbounded fan-out would
// put one RPC and UPDATE per 500 publishers on Commodore simultaneously, and a
// saturated control plane fails them together — every claim in the tick lapsing
// at once is the outage this job exists to prevent.
const (
	activeIngestPlacementBatch       = 500
	activeIngestPlacementConcurrency = 64
	activeIngestPlacementCallTimeout = 5 * time.Second

	// The tick budget is a third of the lease, so the two-budget worst case
	// above leaves the remaining third as margin. Ticks coalesce when a tick
	// overruns the interval, so the interval only shortens the gap.
	activeIngestPlacementInterval   = streamident.ActiveIngestLease / 6 // 5s
	activeIngestPlacementTickBudget = streamident.ActiveIngestLease / 3 // 10s

	// activeIngestPlacementEnumerationBudget is the tick budget's share for
	// listing live publishers. The job sets this deadline itself rather than
	// leaving it to the enumerator, so the two halves of a tick cannot each
	// assume the whole budget.
	activeIngestPlacementEnumerationBudget = 2 * time.Second
	activeIngestPlacementBatchBudget       = activeIngestPlacementTickBudget - activeIngestPlacementEnumerationBudget

	// activeIngestPlacementMaxFirstRenewal is how long a just-admitted claim can
	// wait for its first renewal.
	activeIngestPlacementMaxFirstRenewal = 2 * activeIngestPlacementTickBudget

	// activeIngestPlacementCapacity is the live-publisher count one Foghorn can
	// re-assert inside the budget: waves that fit in the batch budget, times
	// publishers per wave. This is per replica, and renewal is partitioned by
	// control-connection ownership, so fleet capacity is this times the number
	// of Foghorns the nodes are spread across.
	activeIngestPlacementCapacity = activeIngestPlacementBatch * activeIngestPlacementConcurrency *
		int(activeIngestPlacementBatchBudget/activeIngestPlacementCallTimeout)
)

// LiveIngest is one stream with a publisher connected to a node this Foghorn
// owns, and the media cluster that node belongs to.
type LiveIngest struct {
	TenantID     string
	InternalName string
	ClusterID    string
	// ClaimToken owns the claim being re-asserted, so an acquired claim is
	// stamped with the live session rather than left ownerless.
	ClaimToken string
}

// ActiveIngestPlacementSyncer is the Commodore call this job makes.
type ActiveIngestPlacementSyncer interface {
	SyncActiveIngestPlacement(ctx context.Context, clusterID string, renew, release []*commodorepb.ActiveIngestStream) (*commodorepb.SyncActiveIngestPlacementResponse, error)
}

// ActiveIngestPlacementJob keeps Commodore's record of where each stream is
// being published current for ordinary push ingest.
//
// A push claims commodore.streams.active_ingest_cluster_id at PUSH_REWRITE
// under a lease shorter than most sessions, so the claim needs re-asserting
// for as long as the publisher is connected — the same job the managed-stream
// reconciler does for mist_native.
//
// Liveness is the registry's projection of the database-confirmed publisher generation. An open
// ingest-session row is not sufficient: it can outlive a crashed node, and renewing from one would
// hold placement for a publisher that is gone, preventing another cluster from accepting it.
//
// Placement is keyed by the publishing node's media cluster, carried per entry,
// so one batch can mix clusters and a tick's cost tracks publishers rather than
// clusters.
type ActiveIngestPlacementJob struct {
	logger    logging.Logger
	syncer    ActiveIngestPlacementSyncer
	sources   func(context.Context) ([]LiveIngest, error)
	interval  time.Duration
	claimLost func(context.Context, *commodorepb.ActiveIngestStream) error
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// ActiveIngestPlacementConfig configures the placement renewal job.
type ActiveIngestPlacementConfig struct {
	Logger logging.Logger
	Syncer ActiveIngestPlacementSyncer
	// Sources enumerates streams whose publisher is connected to a node this
	// Foghorn owns, each with that node's media cluster. Supplied as a
	// function so this package does not import the registry. The job passes a
	// deadline: enumeration spends part of the tick budget, so it is bounded
	// here rather than by whatever the enumerator chooses for itself.
	Sources func(context.Context) ([]LiveIngest, error)
	// Interval overrides the derived cadence; leave it zero outside tests. The
	// default is computed from the shared lease so the two cannot drift.
	Interval time.Duration
	// ClaimLost fences a publisher whose renewal no longer owns placement.
	ClaimLost func(context.Context, *commodorepb.ActiveIngestStream) error
}

func NewActiveIngestPlacementJob(cfg ActiveIngestPlacementConfig) *ActiveIngestPlacementJob {
	interval := cfg.Interval
	if interval <= 0 {
		interval = activeIngestPlacementInterval
	}
	return &ActiveIngestPlacementJob{
		logger:    cfg.Logger,
		syncer:    cfg.Syncer,
		sources:   cfg.Sources,
		interval:  interval,
		claimLost: cfg.ClaimLost,
		stopCh:    make(chan struct{}),
	}
}

func (j *ActiveIngestPlacementJob) Start() {
	if j.syncer == nil || j.sources == nil {
		j.logger.Warn("Active ingest placement job not started: Commodore client or source enumeration missing")
		return
	}
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Active ingest placement job started")
}

func (j *ActiveIngestPlacementJob) Stop() {
	if j.syncer == nil || j.sources == nil {
		return
	}
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Active ingest placement job stopped")
}

func (j *ActiveIngestPlacementJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.renew()

	for {
		select {
		case <-ticker.C:
			j.renew()
		case <-j.stopCh:
			return
		}
	}
}

// renew re-asserts every live publisher's claim in payload-sized batches.
//
// Batches carry their cluster per entry, so a tick costs ceil(publishers/500)
// calls regardless of how many virtual clusters this Foghorn serves. A call per
// cluster could not make that guarantee: at hundreds of clusters, any
// concurrency cap becomes a queue and the clusters behind it start past their
// own lease window.
//
// Concurrency is capped so the tick does not put one RPC per 500 publishers on
// Commodore at once — a saturated control plane fails them together, lapsing a
// whole tick's claims. Batches beyond the cap wait for a slot, which is safe
// only because the wait is bounded: see the sizing above, and the over-capacity
// warning below for when it stops being true.
//
// A failing batch does not stop the others, since placement lapses on the lease
// clock rather than corrupting; the next tick recovers.
func (j *ActiveIngestPlacementJob) renew() {
	enumCtx, cancelEnum := context.WithTimeout(context.Background(), activeIngestPlacementEnumerationBudget)
	sources, err := j.sources(enumCtx)
	cancelEnum()
	if err != nil {
		j.logger.WithError(err).Warn("Active ingest placement: live-publisher enumeration failed")
		return
	}

	streams := make([]*commodorepb.ActiveIngestStream, 0)
	for _, live := range sources {
		if live.ClusterID == "" || live.TenantID == "" || live.InternalName == "" || live.ClaimToken == "" {
			// An unattributed or unowned publisher cannot be placed;
			// PUSH_REWRITE would not have claimed for it either.
			continue
		}
		streams = append(streams, &commodorepb.ActiveIngestStream{
			TenantId:     live.TenantID,
			InternalName: live.InternalName,
			ClaimToken:   live.ClaimToken,
			ClusterId:    live.ClusterID,
		})
	}
	if len(streams) == 0 {
		return
	}
	// Stable order, so a claim lands in the same batch position every tick and
	// the interval + budget bound above actually holds. Sources come from map
	// iteration, which would otherwise let a claim be renewed first in one tick
	// and last in the next.
	sort.Slice(streams, func(i, j int) bool {
		if streams[i].GetTenantId() != streams[j].GetTenantId() {
			return streams[i].GetTenantId() < streams[j].GetTenantId()
		}
		return streams[i].GetInternalName() < streams[j].GetInternalName()
	})
	if len(streams) > activeIngestPlacementCapacity {
		// Past this, a tick cannot attempt every claim inside the lease window,
		// so some publishers lose placement while still connected. The remedy is
		// another Foghorn: renewal is partitioned by control-connection
		// ownership, so nodes that register against a new replica take their
		// publishers' renewal with them.
		j.logger.WithFields(logging.Fields{
			"publishers": len(streams),
			"capacity":   activeIngestPlacementCapacity,
		}).Error("Active ingest placement: live publishers exceed what one tick can re-assert within the lease window")
	}

	slots := make(chan struct{}, activeIngestPlacementConcurrency)
	var wg sync.WaitGroup
	for start := 0; start < len(streams); start += activeIngestPlacementBatch {
		end := min(start+activeIngestPlacementBatch, len(streams))
		wg.Add(1)
		go func(batch []*commodorepb.ActiveIngestStream) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			if err := j.syncBatch(batch); err != nil {
				j.logger.WithError(err).WithField("streams", len(batch)).
					Warn("Active ingest placement: renewal batch failed; those claims may lapse until the next tick")
			}
		}(streams[start:end])
	}
	wg.Wait()
}

func (j *ActiveIngestPlacementJob) syncBatch(streams []*commodorepb.ActiveIngestStream) error {
	ctx, cancel := context.WithTimeout(context.Background(), activeIngestPlacementCallTimeout)
	defer cancel()
	// No request-level cluster: every entry names its own.
	resp, err := j.syncer.SyncActiveIngestPlacement(ctx, "", streams, nil)
	if err != nil {
		return err
	}
	if resp == nil {
		return errors.New("active ingest placement sync returned no response")
	}
	if j.claimLost != nil {
		for _, refused := range resp.GetRenewRefused() {
			if err := j.claimLost(ctx, refused); err != nil {
				j.logger.WithError(err).WithFields(logging.Fields{
					"tenant_id": refused.GetTenantId(), "internal_name": refused.GetInternalName(),
				}).Error("Active ingest placement: failed to fence publisher that lost its claim")
			}
		}
	}
	return nil
}
