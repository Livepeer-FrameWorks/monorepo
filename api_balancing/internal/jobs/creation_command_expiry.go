package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Bounded-work defaults for one sweep pass. The worker never issues a single unbounded
// UPDATE/DELETE: it processes rows in LIMIT-bounded batches with SKIP LOCKED and loops
// until a pass returns fewer than the batch (the set strictly shrinks each pass, so it
// converges). maxPassesPerSweep caps one tick's total work so a continuously-fed table
// defers the remainder to the next tick rather than running the loop unboundedly.
const (
	creationCommandExpiryBatchDefault    = 500
	creationCommandRetentionBatchDefault = 1000
	creationCommandMaxPassesPerSweep     = 100
)

// CreationCommandExpiryJob terminalizes artifact-creation command-ledger rows left
// stranded 'accepted' and enforces retention on terminal rows.
//
// A create handler durably records 'accepted' before any fallible work, then either
// commits (accepted→committed with the artifact row) or its deferred finalizer rejects
// (accepted→rejected). A row stuck 'accepted' means the handler crashed between the
// accept and its terminal write. Past the hard deadline (deliberately far longer than
// any create handler's own runtime, so a still-running create is never terminalized) an
// 'accepted' row with no matching artifact is such a strand, and the job flips it to
// 'rejected' so the intent converges. The flip is a compare-and-set on status='accepted'
// guarded by NOT EXISTS(<artifact row>): a create that commits concurrently always wins,
// because either its artifact row is now present (the guard fails) or its own commit
// already moved the row off 'accepted' (the CAS matches nothing). A committed create is
// therefore never rejected.
//
// Terminal ('committed'/'rejected') rows are kept as the status ledger the Commodore
// convergence sweep reads. A terminal row is deleted ONLY once Commodore has durably
// consumed it (consumed_at IS NOT NULL, set by AckArtifactCreationCommand) AND its
// consumption is older than the retention horizon (the window is anchored on consumed_at,
// not the terminal transition). An UNCONSUMED terminal outcome is NEVER time-deleted:
// deleting a committed outcome Commodore has not yet read would make it read as MISSING
// and trip the bounded abort against a live artifact — the exact data-loss path this
// ledger exists to prevent. The invariant is retention until a durable ack consumes the
// row, not that the ack succeeds: Commodore's ack-drain may be unable to reach Foghorn, and
// a persistently-failing ack backs off indefinitely without converging, so an unconsumed
// backlog past the horizon is an alertable operational condition (warned each sweep), never
// a reason to delete data.
type CreationCommandExpiryJob struct {
	db               *sql.DB
	logger           logging.Logger
	interval         time.Duration
	deadline         time.Duration
	retentionHorizon time.Duration
	expiryBatch      int
	retentionBatch   int
	stopCh           chan struct{}
	wg               sync.WaitGroup
}

// CreationCommandExpiryConfig holds configuration for the expiry job.
type CreationCommandExpiryConfig struct {
	DB       *sql.DB
	Logger   logging.Logger
	Interval time.Duration // How often to sweep (default: 1 minute)
	Deadline time.Duration // Reject 'accepted' rows older than this (default: 15 minutes)
	// RetentionHorizon deletes CONSUMED 'committed'/'rejected' rows older than this
	// (default: 7 days). Must stay far past any convergence window. It also gates the
	// unconsumed-backlog warning: an unconsumed terminal row older than this horizon is a
	// stuck ack the operator should investigate (it is warned, never deleted).
	RetentionHorizon time.Duration
	// ExpiryBatch / RetentionBatch bound one pass's row count (defaults 500 / 1000).
	ExpiryBatch    int
	RetentionBatch int
}

// NewCreationCommandExpiryJob creates a new creation-command expiry job.
func NewCreationCommandExpiryJob(cfg CreationCommandExpiryConfig) *CreationCommandExpiryJob {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	deadline := cfg.Deadline
	if deadline <= 0 {
		deadline = 15 * time.Minute
	}
	retentionHorizon := cfg.RetentionHorizon
	if retentionHorizon <= 0 {
		retentionHorizon = 7 * 24 * time.Hour
	}
	expiryBatch := cfg.ExpiryBatch
	if expiryBatch <= 0 {
		expiryBatch = creationCommandExpiryBatchDefault
	}
	retentionBatch := cfg.RetentionBatch
	if retentionBatch <= 0 {
		retentionBatch = creationCommandRetentionBatchDefault
	}
	return &CreationCommandExpiryJob{
		db:               cfg.DB,
		logger:           cfg.Logger,
		interval:         interval,
		deadline:         deadline,
		retentionHorizon: retentionHorizon,
		expiryBatch:      expiryBatch,
		retentionBatch:   retentionBatch,
		stopCh:           make(chan struct{}),
	}
}

// Start begins the background expiry loop.
func (j *CreationCommandExpiryJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Creation command expiry job started")
}

// Stop gracefully stops the job.
func (j *CreationCommandExpiryJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Creation command expiry job stopped")
}

func (j *CreationCommandExpiryJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.sweep()

	for {
		select {
		case <-ticker.C:
			j.sweep()
		case <-j.stopCh:
			return
		}
	}
}

// sweep runs one pass: terminalize stranded accepts, enforce terminal-row retention on
// CONSUMED rows, then warn on any unconsumed terminal backlog. All run in bounded work.
func (j *CreationCommandExpiryJob) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	j.expire(ctx)
	j.enforceRetention(ctx)
	j.warnUnconsumedBacklog(ctx)
}

func deadlineSeconds(d time.Duration) int64 {
	s := int64(d.Seconds())
	if s <= 0 {
		return 1
	}
	return s
}

// expire CAS-rejects stranded 'accepted' rows in bounded batches. Each batch locks up to
// expiryBatch matching rows with FOR UPDATE SKIP LOCKED (so concurrent sweepers never
// contend) and flips only those still 'accepted' with no matching artifact. kind and
// foghorn.artifacts.artifact_type share the same domain ('clip'|'dvr'|'vod'), so the
// artifact-presence guard joins on artifact_type = kind. The loop converges because each
// pass removes its rows from the 'accepted' set; it stops when a pass rejects fewer than
// the batch (or hits the per-sweep cap, deferring the rest to the next tick).
func (j *CreationCommandExpiryJob) expire(ctx context.Context) {
	secs := deadlineSeconds(j.deadline)
	total := int64(0)
	for pass := 0; pass < creationCommandMaxPassesPerSweep; pass++ {
		rows, err := foghorndb.New(j.db).ExpireStrandedCreationCommands(ctx, foghorndb.ExpireStrandedCreationCommandsParams{
			DeadlineSeconds: secs, BatchLimit: int32(j.expiryBatch),
		})
		if err != nil {
			j.logger.WithError(err).Warn("Failed to expire stranded accepted creation commands")
			return
		}
		total += rows
		if rows < int64(j.expiryBatch) {
			break
		}
	}
	if total > 0 {
		j.logger.WithField("count", total).Warn("Rejected stranded accepted creation commands past deadline")
	}
}

// enforceRetention deletes CONSUMED terminal ('committed'/'rejected') rows in bounded
// batches (FOR UPDATE SKIP LOCKED + LIMIT, looping until a pass deletes fewer than the
// batch), so the ledger cannot grow unbounded and no single unbounded DELETE degrades
// with historical volume. Only a row Commodore has durably consumed (consumed_at IS NOT
// NULL, set by AckArtifactCreationCommand) is eligible, and the retention window is anchored
// on consumed_at, NOT the terminal transition: a row terminalized long ago but only just
// acked is retained a full horizon past its consumption, so a lost ack RESPONSE (Commodore
// crashes before clearing its obligation) cannot leave the row already GC-eligible and drive
// the ack retry to MISSING. An UNCONSUMED terminal outcome is NEVER time-deleted: erasing an
// outcome Commodore has not yet read would make it read as MISSING and trip the bounded abort
// against a live artifact.
func (j *CreationCommandExpiryJob) enforceRetention(ctx context.Context) {
	secs := deadlineSeconds(j.retentionHorizon)
	total := int64(0)
	for pass := 0; pass < creationCommandMaxPassesPerSweep; pass++ {
		rows, err := foghorndb.New(j.db).DeleteConsumedCreationCommands(ctx, foghorndb.DeleteConsumedCreationCommandsParams{
			RetentionSeconds: secs, BatchLimit: int32(j.retentionBatch),
		})
		if err != nil {
			j.logger.WithError(err).Warn("Failed to delete retained terminal creation commands")
			return
		}
		total += rows
		if rows < int64(j.retentionBatch) {
			break
		}
	}
	if total > 0 {
		j.logger.WithField("count", total).Info("Deleted consumed terminal creation commands past retention horizon")
	}
}

// warnUnconsumedBacklog counts terminal ('committed'/'rejected') rows still unconsumed
// (consumed_at IS NULL) past the retention horizon and WARNs when any exist. These rows
// are never deleted — a stuck ack is an operational condition to investigate, not data to
// discard — so a nonzero count is an alertable signal that Commodore's durable ack-drain
// is not converging.
func (j *CreationCommandExpiryJob) warnUnconsumedBacklog(ctx context.Context) {
	secs := deadlineSeconds(j.retentionHorizon)
	count, err := foghorndb.New(j.db).CountStaleUnconsumedCreationCommands(ctx, secs)
	if err != nil {
		j.logger.WithError(err).Warn("Failed to count unconsumed terminal creation commands")
		return
	}
	if count > 0 {
		j.logger.WithField("count", count).Warn("Unconsumed terminal creation commands past retention horizon; ack backlog is an operational condition, rows are never time-deleted")
	}
}
