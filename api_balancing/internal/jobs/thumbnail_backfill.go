package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// A clip or VOD gets its thumbnails from the one processing run that publishes it. When that run's thumbnail is
// lost (the Thumbs process is inconsequential to the run, a mint is refused, an upload never lands) the asset is
// ready without thumbnails and nothing else would produce them. The backfill queues a thumbnail-only processing
// job for such an asset from its stored media.
const (
	thumbnailBackfillInterval = 10 * time.Minute
	// An asset younger than this may still be publishing the thumbnail of its own run.
	thumbnailBackfillMinAge = time.Hour
	// A thumbnail job remuxes the whole asset on a processing node, so attempts are few and spaced, and only a
	// couple run cluster-wide at a time.
	thumbnailBackfillRetryAfter  = 6 * time.Hour
	thumbnailBackfillMaxAttempts = 3
	thumbnailBackfillMaxActive   = 2
	thumbnailBackfillBatch       = 10
)

// ThumbnailBackfillJob queues thumbnail jobs for published assets that have no thumbnail.
type ThumbnailBackfillJob struct {
	db     *sql.DB
	logger logging.Logger
	// resolveProcesses returns the process policy Commodore resolves for the asset's lifecycle.
	resolveProcesses func(ctx context.Context, tenantID, lifecycle, clusterID, streamID string) (string, error)
	stopCh           chan struct{}
	wg               sync.WaitGroup
}

// NewThumbnailBackfillJob creates the backfill job. Process policy comes from Commodore, the policy authority.
func NewThumbnailBackfillJob(db *sql.DB, logger logging.Logger) *ThumbnailBackfillJob {
	return &ThumbnailBackfillJob{
		db:     db,
		logger: logger,
		resolveProcesses: func(ctx context.Context, tenantID, lifecycle, clusterID, streamID string) (string, error) {
			if control.CommodoreClient == nil {
				return "", fmt.Errorf("commodore client not configured")
			}
			resp, err := control.CommodoreClient.GetTenantProcessesJSONForLifecycle(ctx, tenantID, lifecycle, clusterID, streamID)
			if err != nil {
				return "", err
			}
			return resp.GetProcessesJson(), nil
		},
		stopCh: make(chan struct{}),
	}
}

// Start begins the background backfill loop.
func (j *ThumbnailBackfillJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Thumbnail backfill job started")
}

// Stop gracefully stops the job.
func (j *ThumbnailBackfillJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Thumbnail backfill job stopped")
}

func (j *ThumbnailBackfillJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(thumbnailBackfillInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := stoppable(j.stopCh, 2*time.Minute)
			if _, err := j.backfill(ctx); err != nil {
				j.logger.WithError(err).Warn("Thumbnail backfill pass failed")
			}
			cancel()
		case <-j.stopCh:
			return
		}
	}
}

// thumbnailLifecycle names the Commodore process lifecycle whose Thumbs policy the asset was published under.
func thumbnailLifecycle(row foghorndb.ListArtifactsMissingThumbnailsRow) string {
	switch {
	case row.ArtifactType == "clip":
		return "clip"
	case row.IsChapter:
		return "dvr_finalize"
	default:
		return "vod"
	}
}

// backfill runs one pass and returns how many thumbnail jobs it queued.
func (j *ThumbnailBackfillJob) backfill(ctx context.Context) (int, error) {
	if j.db == nil {
		return 0, nil
	}
	q := foghorndb.New(j.db)
	if orphaned, err := q.FailOrphanedThumbnailJobs(ctx); err != nil {
		return 0, fmt.Errorf("fail orphaned thumbnail jobs: %w", err)
	} else if orphaned > 0 {
		j.logger.WithField("count", orphaned).Info("Thumbnail backfill: failed queued thumbnail jobs whose asset is no longer published")
	}
	active, err := q.CountActiveThumbnailJobs(ctx)
	if err != nil {
		return 0, fmt.Errorf("count active thumbnail jobs: %w", err)
	}
	room := thumbnailBackfillMaxActive - int(active)
	if room <= 0 {
		return 0, nil
	}
	rows, err := q.ListArtifactsMissingThumbnails(ctx, foghorndb.ListArtifactsMissingThumbnailsParams{
		MinAgeSeconds:     int64(thumbnailBackfillMinAge.Seconds()),
		RetryAfterSeconds: int64(thumbnailBackfillRetryAfter.Seconds()),
		MaxAttempts:       thumbnailBackfillMaxAttempts,
		BatchLimit:        thumbnailBackfillBatch,
	})
	if err != nil {
		return 0, fmt.Errorf("list artifacts missing thumbnails: %w", err)
	}
	queued := 0
	for _, row := range rows {
		if queued >= room {
			break
		}
		lifecycle := thumbnailLifecycle(row)
		policy, err := j.resolveProcesses(ctx, row.TenantID, lifecycle, row.OriginClusterID, row.StreamID)
		if err != nil {
			// Commodore unreachable: nothing is recorded, the asset is reconsidered next pass.
			return queued, fmt.Errorf("resolve %s process policy for %s: %w", lifecycle, row.ArtifactHash, err)
		}
		fields := logging.Fields{"artifact_hash": row.ArtifactHash, "tenant_id": row.TenantID, "lifecycle": lifecycle}
		processes := mist.ThumbnailProcesses(policy)
		if processes == "[]" {
			if skipErr := q.RecordSkippedThumbnailJob(ctx, foghorndb.RecordSkippedThumbnailJobParams{
				TenantID:     row.TenantID,
				ArtifactHash: sql.NullString{String: row.ArtifactHash, Valid: true},
				Reason:       "process policy has no thumbnail process for lifecycle " + lifecycle,
			}); skipErr != nil {
				return queued, fmt.Errorf("record skipped thumbnail job for %s: %w", row.ArtifactHash, skipErr)
			}
			j.logger.WithFields(fields).Info("Thumbnail backfill: asset's process policy produces no thumbnails; recorded as skipped")
			continue
		}
		jobID, err := InsertProcessingJob(ctx, j.db, row.TenantID, row.ArtifactHash, mist.ProcessingJobTypeThumbnail, nil, processes)
		if err != nil {
			return queued, fmt.Errorf("queue thumbnail job for %s: %w", row.ArtifactHash, err)
		}
		queued++
		j.logger.WithFields(fields).WithField("job_id", jobID).Info("Thumbnail backfill: queued thumbnail job for a published asset without thumbnails")
	}
	return queued, nil
}
