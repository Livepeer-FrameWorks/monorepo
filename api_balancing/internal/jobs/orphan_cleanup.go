package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
)

// OrphanCleanupJob reconciles soft-deleted clips/DVRs that still have storage artifacts
type OrphanCleanupJob struct {
	db       *sql.DB
	logger   logging.Logger
	interval time.Duration
	maxAge   time.Duration // How old a deleted record must be before reconciliation
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// OrphanCleanupConfig holds configuration for the cleanup job
type OrphanCleanupConfig struct {
	DB       *sql.DB
	Logger   logging.Logger
	Interval time.Duration // How often to run (default: 5 minutes)
	MaxAge   time.Duration // Min age of deleted records to process (default: 30 minutes)
}

// NewOrphanCleanupJob creates a new orphan cleanup job
func NewOrphanCleanupJob(cfg OrphanCleanupConfig) *OrphanCleanupJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 5 * time.Minute
	}
	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = 30 * time.Minute
	}
	return &OrphanCleanupJob{
		db:       cfg.DB,
		logger:   cfg.Logger,
		interval: interval,
		maxAge:   maxAge,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the background reconciliation loop
func (j *OrphanCleanupJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Orphan cleanup job started")
}

// Stop gracefully stops the job
func (j *OrphanCleanupJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Orphan cleanup job stopped")
}

func (j *OrphanCleanupJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	// Run once at startup
	j.reconcile()

	for {
		select {
		case <-ticker.C:
			j.reconcile()
		case <-j.stopCh:
			return
		}
	}
}

func (j *OrphanCleanupJob) reconcile() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Reconcile orphaned clips
	clipOrphans, err := j.findOrphanedClips(ctx)
	if err != nil {
		j.logger.WithError(err).Error("Failed to find orphaned clips")
	} else {
		for _, orphan := range clipOrphans {
			j.retryClipDeletion(ctx, orphan)
		}
	}

	// Reconcile orphaned DVRs
	dvrOrphans, err := j.findOrphanedDVRs(ctx)
	if err != nil {
		j.logger.WithError(err).Error("Failed to find orphaned DVRs")
	} else {
		for _, orphan := range dvrOrphans {
			j.retryDVRDeletion(ctx, orphan)
		}
	}

	// Reconcile orphaned VODs (uploads)
	vodOrphans, err := j.findOrphanedVODs(ctx)
	if err != nil {
		j.logger.WithError(err).Error("Failed to find orphaned VODs")
	} else {
		for _, orphan := range vodOrphans {
			j.retryVODDeletion(ctx, orphan)
		}
	}

	// Clean up stale orphaned registry entries (storage confirmed gone)
	j.cleanupStaleRegistryEntries(ctx)
}

type orphanedClip struct {
	ClipHash string
	NodeID   string
}

type orphanedDVR struct {
	DVRHash string
	NodeID  string
}

type orphanedVOD struct {
	VODHash string
	NodeID  string
}

// findOrphanedClips finds soft-deleted clips that still have storage artifacts
func (j *OrphanCleanupJob) findOrphanedClips(ctx context.Context) ([]orphanedClip, error) {
	var rows []foghorndb.ListDeletedClipNodesRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		rows, err = foghorndb.New(j.db).ListDeletedClipNodes(ctx, j.maxAge.String())
		return err
	})
	if err != nil {
		return nil, err
	}
	orphans := make([]orphanedClip, 0, len(rows))
	for _, row := range rows {
		orphans = append(orphans, orphanedClip{ClipHash: row.ArtifactHash, NodeID: row.NodeID})
	}
	return orphans, nil
}

// findOrphanedDVRs finds soft-deleted DVRs that still have storage artifacts
func (j *OrphanCleanupJob) findOrphanedDVRs(ctx context.Context) ([]orphanedDVR, error) {
	var rows []foghorndb.ListDeletedDVRNodesRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		rows, err = foghorndb.New(j.db).ListDeletedDVRNodes(ctx, j.maxAge.String())
		return err
	})
	if err != nil {
		return nil, err
	}
	orphans := make([]orphanedDVR, 0, len(rows))
	for _, row := range rows {
		orphans = append(orphans, orphanedDVR{DVRHash: row.ArtifactHash, NodeID: row.NodeID})
	}
	return orphans, nil
}

// findOrphanedVODs finds soft-deleted VODs (uploads) that still have storage artifacts
func (j *OrphanCleanupJob) findOrphanedVODs(ctx context.Context) ([]orphanedVOD, error) {
	var rows []foghorndb.ListDeletedVODNodesRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var err error
		rows, err = foghorndb.New(j.db).ListDeletedVODNodes(ctx, j.maxAge.String())
		return err
	})
	if err != nil {
		return nil, err
	}
	orphans := make([]orphanedVOD, 0, len(rows))
	for _, row := range rows {
		orphans = append(orphans, orphanedVOD{VODHash: row.ArtifactHash, NodeID: row.NodeID})
	}
	return orphans, nil
}

func (j *OrphanCleanupJob) retryClipDeletion(_ctx context.Context, orphan orphanedClip) {
	requestID := uuid.NewString()
	deleteReq := &ipcpb.ClipDeleteRequest{
		ClipHash:  orphan.ClipHash,
		RequestId: requestID,
	}

	if err := control.SendClipDelete(orphan.NodeID, deleteReq); err != nil {
		j.logger.WithFields(logging.Fields{
			"clip_hash":  orphan.ClipHash,
			"node_id":    orphan.NodeID,
			"request_id": requestID,
			"error":      err,
		}).Warn("Orphan cleanup: failed to retry clip deletion")
		return
	}

	j.logger.WithFields(logging.Fields{
		"clip_hash":  orphan.ClipHash,
		"node_id":    orphan.NodeID,
		"request_id": requestID,
	}).Info("Orphan cleanup: retried clip deletion")
}

func (j *OrphanCleanupJob) retryDVRDeletion(_ctx context.Context, orphan orphanedDVR) {
	requestID := uuid.NewString()
	deleteReq := &ipcpb.DVRDeleteRequest{
		DvrHash:   orphan.DVRHash,
		RequestId: requestID,
	}

	if err := control.SendDVRDelete(orphan.NodeID, deleteReq); err != nil {
		j.logger.WithFields(logging.Fields{
			"dvr_hash":   orphan.DVRHash,
			"node_id":    orphan.NodeID,
			"request_id": requestID,
			"error":      err,
		}).Warn("Orphan cleanup: failed to retry DVR deletion")
		return
	}

	j.logger.WithFields(logging.Fields{
		"dvr_hash":   orphan.DVRHash,
		"node_id":    orphan.NodeID,
		"request_id": requestID,
	}).Info("Orphan cleanup: retried DVR deletion")
}

func (j *OrphanCleanupJob) retryVODDeletion(_ctx context.Context, orphan orphanedVOD) {
	requestID := uuid.NewString()
	deleteReq := &ipcpb.VodDeleteRequest{
		VodHash:   orphan.VODHash,
		RequestId: requestID,
	}

	if err := control.SendVodDelete(orphan.NodeID, deleteReq); err != nil {
		j.logger.WithFields(logging.Fields{
			"vod_hash":   orphan.VODHash,
			"node_id":    orphan.NodeID,
			"request_id": requestID,
			"error":      err,
		}).Warn("Orphan cleanup: failed to retry VOD deletion")
		return
	}

	j.logger.WithFields(logging.Fields{
		"vod_hash":   orphan.VODHash,
		"node_id":    orphan.NodeID,
		"request_id": requestID,
	}).Info("Orphan cleanup: retried VOD deletion")
}

// cleanupStaleRegistryEntries removes orphaned artifact_nodes entries that haven't been seen for a long time
// This handles cases where storage was manually deleted or node went away
func (j *OrphanCleanupJob) cleanupStaleRegistryEntries(ctx context.Context) {
	// Remove artifact_nodes entries marked orphaned for more than 24 hours
	affected, err := foghorndb.New(j.db).DeleteStaleOrphanedArtifactNodes(ctx)
	if err != nil {
		j.logger.WithError(err).Error("Failed to clean up stale artifact_nodes entries")
		return
	}

	if affected > 0 {
		j.logger.WithField("count", affected).Info("Cleaned up stale artifact_nodes entries")
	}
}
