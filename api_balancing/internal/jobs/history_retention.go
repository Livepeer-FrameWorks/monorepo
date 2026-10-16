package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Ended ingest generations and terminal processing jobs are history once nothing references them: an ended
// generation is resolved by id only within seconds of its end, and a terminal job is read only while it is the
// newest job of its artifact. Thirty days keeps them for incident inspection. Delivered artifact events are kept a
// week, like the domain event outbox.
const (
	historyRetention         = 30 * 24 * time.Hour
	deliveredEventRetention  = 7 * 24 * time.Hour
	historyRetentionInterval = time.Hour
	historyPurgeBatch        = 1000
	historyPurgeMaxBatches   = 100
)

// HistoryRetentionJob deletes ended ingest sessions, terminal processing jobs and delivered artifact events past their
// retention. Every replica runs it; each batch locks its rows with SKIP LOCKED, so concurrent replicas delete
// disjoint rows.
type HistoryRetentionJob struct {
	db     *sql.DB
	logger logging.Logger
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewHistoryRetentionJob creates the history retention job.
func NewHistoryRetentionJob(db *sql.DB, logger logging.Logger) *HistoryRetentionJob {
	return &HistoryRetentionJob{db: db, logger: logger, stopCh: make(chan struct{})}
}

// Start begins the background retention loop.
func (j *HistoryRetentionJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("History retention job started")
}

// Stop gracefully stops the job.
func (j *HistoryRetentionJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("History retention job stopped")
}

func (j *HistoryRetentionJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(historyRetentionInterval)
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

func (j *HistoryRetentionJob) sweep() {
	if j.db == nil {
		return
	}
	ctx, cancel := stoppable(j.stopCh, 5*time.Minute)
	defer cancel()
	purged, err := purgeRetainedHistory(ctx, foghorndb.New(j.db))
	if err != nil {
		j.logger.WithError(err).Warn("History retention pass failed")
	}
	if purged != (retentionPurge{}) {
		j.logger.WithFields(logging.Fields{
			"ingest_sessions": purged.sessions,
			"processing_jobs": purged.jobs,
			"artifact_events": purged.events,
		}).Info("Deleted retained history past its horizon")
	}
}

type retentionPurge struct {
	sessions, jobs, events int64
}

// purgeRetainedHistory runs one retention pass over every table, each capped at historyPurgeMaxBatches batches so a
// large first backlog drains over several passes. A failing table does not stop the others.
func purgeRetainedHistory(ctx context.Context, q *foghorndb.Queries) (retentionPurge, error) {
	var purged retentionPurge
	var firstErr error
	keep := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	history := int64(historyRetention.Seconds())
	var err error
	purged.sessions, err = drainBatches(func() (int64, error) {
		return q.PurgeEndedIngestSessions(ctx, foghorndb.PurgeEndedIngestSessionsParams{
			RetentionSeconds: history, BatchLimit: historyPurgeBatch,
		})
	})
	keep(err)
	purged.jobs, err = drainBatches(func() (int64, error) {
		return q.PurgeTerminalProcessingJobs(ctx, foghorndb.PurgeTerminalProcessingJobsParams{
			RetentionSeconds: history, BatchLimit: historyPurgeBatch,
		})
	})
	keep(err)
	purged.events, err = drainBatches(func() (int64, error) {
		return q.PurgeCompletedArtifactEvents(ctx, foghorndb.PurgeCompletedArtifactEventsParams{
			RetentionSeconds: int64(deliveredEventRetention.Seconds()), BatchLimit: historyPurgeBatch,
		})
	})
	keep(err)
	return purged, firstErr
}

func drainBatches(batch func() (int64, error)) (int64, error) {
	var total int64
	for range historyPurgeMaxBatches {
		n, err := batch()
		total += n
		if err != nil {
			return total, err
		}
		if n < historyPurgeBatch {
			break
		}
	}
	return total, nil
}
