package grpc

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

const (
	// mediaAuthoritySweepLock names the session lock whose holder runs the
	// whole-catalog sweeps: hourly reconciliation, retention and the activation
	// backlog. Each covers every authority, so running it on every replica only
	// multiplies the same work.
	mediaAuthoritySweepLock = "commodore-media-authority-sweeps"
	// mediaAuthorityQueueSweepPasses bounds how many batches one queue sweep
	// settles before it leaves the rest to the next minute.
	mediaAuthorityQueueSweepPasses = 20
	// mediaAuthorityRearmAttempts is how many transient attempts a target parked
	// after exhausting them gets when reconciliation re-arms it.
	mediaAuthorityRearmAttempts = 3
	mediaAuthorityRearmBatch    = 500
	mediaAuthorityRearmPasses   = 200
	// mediaAuthorityCellDueBatch and mediaAuthorityCellDuePasses bound what one
	// proof that a cell is reachable makes due.
	mediaAuthorityCellDueBatch  = 1000
	mediaAuthorityCellDuePasses = 10
)

// mediaAuthoritySweepLeadership elects the replica that runs the whole-catalog
// sweeps. The election is a session advisory lock held on one pinned
// connection; a leader that stops or loses its session hands it to the next
// replica that asks. Every sweep asks it, so a check that keeps failing warns
// once for the run and then logs at debug level.
type mediaAuthoritySweepLeadership struct {
	mu      sync.Mutex
	lock    *database.SessionLeader
	leading bool
	failing bool
}

// leadsMediaAuthoritySweeps reports whether this replica runs the sweeps now,
// taking the lock when nobody holds it. Every sweep asks before it runs, so a
// replica that loses its database session stops sweeping at its next tick.
func (s *CommodoreServer) leadsMediaAuthoritySweeps(ctx context.Context) bool {
	if s == nil || s.db == nil {
		return false
	}
	election := &s.mediaAuthoritySweeps
	election.mu.Lock()
	if election.lock == nil {
		election.lock = database.NewSessionLeader(s.db, mediaAuthoritySweepLock)
	}
	lock := election.lock
	election.mu.Unlock()

	leadCtx, cancel := context.WithTimeout(ctx, mediaAuthorityClaimTimeout)
	leading, err := lock.Lead(leadCtx)
	cancel()
	election.mu.Lock()
	defer election.mu.Unlock()
	lost := errors.Is(err, database.ErrLostLeadership)
	if s.logger != nil && ctx.Err() == nil {
		switch {
		case lost:
			s.logger.WithError(err).Warn("Media authority sweeps stopped on this replica: it lost the sweep election lock")
		case err != nil && !election.failing:
			s.logger.WithError(err).Warn("Media authority sweeps skipped: could not check the sweep election")
		case err != nil:
			s.logger.WithError(err).Debug("Media authority sweep election check still failing")
		case election.failing:
			s.logger.Info("Media authority sweep election check recovered")
		}
	}
	election.failing = err != nil && !lost
	if leading != election.leading {
		election.leading = leading
		if s.logger != nil {
			if leading {
				s.logger.Info("Media authority sweeps elected on this replica")
			} else {
				s.logger.Info("Media authority sweeps no longer run on this replica")
			}
		}
	}
	return leading
}

// releaseMediaAuthoritySweeps gives up the sweep election so another replica
// takes it at its next tick.
func (s *CommodoreServer) releaseMediaAuthoritySweeps() {
	election := &s.mediaAuthoritySweeps
	election.mu.Lock()
	lock := election.lock
	election.leading = false
	election.mu.Unlock()
	if lock != nil {
		lock.Release()
	}
}

func (s *CommodoreServer) reconcileMediaAuthoritiesOnLeader(ctx context.Context) {
	if s.leadsMediaAuthoritySweeps(ctx) {
		s.reconcileMediaAuthorities(ctx)
	}
}

// sweepMediaAuthorityDeliveryQueue settles queued deliveries that can no longer
// be made, a batch per statement, until a batch comes back short or the pass
// bound is reached. A backlog larger than one batch is cleared in one sweep
// rather than one batch a minute.
func (s *CommodoreServer) sweepMediaAuthorityDeliveryQueue(ctx context.Context, batch int32) {
	queries := commodoredb.New(s.db)
	for _, sweep := range []struct {
		settle  func(context.Context, int32) (int64, error)
		failure string
	}{
		{queries.SupersedeExpiredObsoleteMediaAuthorityDeliveries, "Failed to settle obsolete media authority deliveries"},
		{queries.SettleExpiredMediaAuthorityDeliveries, "Failed to settle expired media authority deliveries"},
	} {
		for pass := 0; pass < mediaAuthorityQueueSweepPasses && ctx.Err() == nil; pass++ {
			settled, err := authorityMetricQuery(ctx, func(ctx context.Context) (int64, error) {
				return sweep.settle(ctx, batch)
			})
			if err != nil {
				s.logger.WithError(err).Warn(sweep.failure)
				break
			}
			if settled < int64(batch) {
				break
			}
		}
	}
}

// rearmParkedMediaAuthorityObligations puts every parked target back in the
// queue, a batch per statement. A target that exhausted its transient attempts
// comes back with mediaAuthorityRearmAttempts left.
func (s *CommodoreServer) rearmParkedMediaAuthorityObligations(ctx context.Context) (int64, error) {
	queries := commodoredb.New(s.db)
	var total int64
	for pass := 0; pass < mediaAuthorityRearmPasses && ctx.Err() == nil; pass++ {
		rearmed, err := queries.RearmParkedMediaAuthorityObligations(ctx, commodoredb.RearmParkedMediaAuthorityObligationsParams{
			RearmedAttempts: mediaAuthorityMaxTransientAttempts - mediaAuthorityRearmAttempts,
			BatchSize:       mediaAuthorityRearmBatch,
		})
		total += rearmed
		if err != nil {
			return total, err
		}
		if rearmed < mediaAuthorityRearmBatch {
			break
		}
	}
	return total, nil
}

// enqueueTenantMediaObjectRefreshesInChunks reads the tenant's held objects
// once and enqueues them chunk per statement, returning how many each statement
// enqueued. A failed chunk fails the fanout, whose retry starts over; an object
// enqueued twice folds into its one pending refresh.
func (s *CommodoreServer) enqueueTenantMediaObjectRefreshesInChunks(ctx context.Context, tenantID string, chunk int) ([]int64, error) {
	if chunk <= 0 {
		chunk = mediaAuthorityFanoutChunk
	}
	queries := commodoredb.New(s.db)
	targets, err := queries.ListTenantMediaObjectRefreshTargets(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list tenant media-object refresh targets: %w", err)
	}
	enqueued := make([]int64, 0, (len(targets)+chunk-1)/chunk)
	for start := 0; start < len(targets); start += chunk {
		end := min(start+chunk, len(targets))
		count, err := queries.EnqueueMediaObjectBulkRefreshes(ctx, commodoredb.EnqueueMediaObjectBulkRefreshesParams{
			TenantID: tenantID, Reason: "tenant_media_objects:fanout", SourceEventID: "tenant-fanout:" + tenantID,
			TargetKeys: targets[start:end],
		})
		if err != nil {
			return enqueued, fmt.Errorf("enqueue tenant media-object refreshes: %w", err)
		}
		enqueued = append(enqueued, count)
	}
	return enqueued, nil
}

// makeCellMediaAuthorityDeliveriesDue makes the deliveries to a cell that are
// waiting out a failure's backoff due now. It is called when the cell has just
// answered, so a backoff that grew while the cell could not be reached does not
// hold back its first deliveries after it can.
func (s *CommodoreServer) makeCellMediaAuthorityDeliveriesDue(ctx context.Context, cellID string) (int64, error) {
	queries := commodoredb.New(s.db)
	var total int64
	for pass := 0; pass < mediaAuthorityCellDuePasses && ctx.Err() == nil; pass++ {
		due, err := queries.MakeCellMediaAuthorityDeliveriesDue(ctx, commodoredb.MakeCellMediaAuthorityDeliveriesDueParams{
			CellID: cellID, BatchSize: mediaAuthorityCellDueBatch,
		})
		total += due
		if err != nil {
			return total, err
		}
		// Each statement reads two partial indexes, a batch from each.
		if due < mediaAuthorityCellDueBatch {
			break
		}
	}
	if total > 0 {
		s.mediaAuthoritySchedule.deadlineWake.notify()
	}
	return total, nil
}
