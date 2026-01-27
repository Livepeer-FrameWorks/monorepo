package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/logging"
)

// PurgeDeletedJob hard-deletes old soft-deleted records to prevent table bloat
type PurgeDeletedJob struct {
	db              *sql.DB
	logger          logging.Logger
	interval        time.Duration
	retentionAge    time.Duration // How old a deleted record must be before hard-deletion
	stopCh          chan struct{}
	wg              sync.WaitGroup
	s3Client        S3Client              // Interface for S3 operations
	commodoreClient *commodore.GRPCClient // For tenant context resolution
}

// S3Client interface for dependency injection
type S3Client interface {
	Delete(ctx context.Context, key string) error
	DeletePrefix(ctx context.Context, prefix string) (int, error)
	BuildClipS3Key(tenantID, streamName, clipHash, format string) string
	BuildDVRS3Key(tenantID, internalName, dvrHash string) string
}

// PurgeDeletedConfig holds configuration for the purge job
type PurgeDeletedConfig struct {
	DB              *sql.DB
	Logger          logging.Logger
	Interval        time.Duration // How often to run (default: 24 hours)
	RetentionAge    time.Duration // Min age of deleted records to purge (default: 30 days)
	S3Client        S3Client
	CommodoreClient *commodore.GRPCClient
}

// NewPurgeDeletedJob creates a new purge deleted job
func NewPurgeDeletedJob(cfg PurgeDeletedConfig) *PurgeDeletedJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 24 * time.Hour
	}
	retentionAge := cfg.RetentionAge
	if retentionAge == 0 {
		retentionAge = 30 * 24 * time.Hour // 30 days
	}
	return &PurgeDeletedJob{
		db:              cfg.DB,
		logger:          cfg.Logger,
		interval:        interval,
		retentionAge:    retentionAge,
		stopCh:          make(chan struct{}),
		s3Client:        cfg.S3Client,
		commodoreClient: cfg.CommodoreClient,
	}
}

// Start begins the background purge loop
func (j *PurgeDeletedJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Purge deleted job started")
}

// Stop gracefully stops the job
func (j *PurgeDeletedJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Purge deleted job stopped")
}

func (j *PurgeDeletedJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	// Run once at startup (staggered by 1 hour to avoid startup load)
	time.AfterFunc(1*time.Hour, func() {
		j.purge()
	})

	for {
		select {
		case <-ticker.C:
			j.purge()
		case <-j.stopCh:
			return
		}
	}
}

func (j *PurgeDeletedJob) purge() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 1. Purge deleted artifacts (clips and DVRs)
	// Find artifacts ready for hard deletion (deleted status, no active node copies)
	rows, err := j.db.QueryContext(ctx, `
		SELECT artifact_hash, artifact_type, internal_name, manifest_path, COALESCE(s3_url, '')
		FROM foghorn.artifacts
		WHERE status = 'deleted'
		  AND updated_at < NOW() - $1::interval
		  AND NOT EXISTS (
		      SELECT 1 FROM foghorn.artifact_nodes an
		      WHERE an.artifact_hash = foghorn.artifacts.artifact_hash
		        AND an.is_orphaned = false
		  )
		LIMIT 1000
	`, j.retentionAge.String())

	if err != nil {
		j.logger.WithError(err).Error("Failed to query deleted artifacts for purge")
	} else {
		defer rows.Close()
		var clipCount, dvrCount int
		for rows.Next() {
			var hash, artifactType, internalName, manifestPath, s3URL string
			if err := rows.Scan(&hash, &artifactType, &internalName, &manifestPath, &s3URL); err != nil {
				continue
			}

			// Get tenant_id from Commodore for S3 path building
			var tenantID string
			if j.commodoreClient != nil {
				if artifactType == "clip" {
					if resp, err := j.commodoreClient.ResolveClipHash(ctx, hash); err == nil && resp.Found {
						tenantID = resp.TenantId
					}
				} else if artifactType == "dvr" {
					if resp, err := j.commodoreClient.ResolveDVRHash(ctx, hash); err == nil && resp.Found {
						tenantID = resp.TenantId
					}
				}
			}

			// Delete from S3 if client configured
			if j.s3Client != nil && tenantID != "" {
				if artifactType == "clip" {
					// Infer format from path or default to mp4
					format := "mp4"
					if idx := len(manifestPath) - 4; idx > 0 && manifestPath[idx] == '.' {
						format = manifestPath[idx+1:]
					}
					key := j.s3Client.BuildClipS3Key(tenantID, internalName, hash, format)
					if err := j.s3Client.Delete(ctx, key); err != nil {
						j.logger.WithError(err).WithField("clip_hash", hash).Warn("Failed to delete clip from S3")
					}
				} else if artifactType == "dvr" {
					prefix := j.s3Client.BuildDVRS3Key(tenantID, internalName, hash)
					if _, err := j.s3Client.DeletePrefix(ctx, prefix); err != nil {
						j.logger.WithError(err).WithField("dvr_hash", hash).Warn("Failed to delete DVR from S3")
					}
				}
			}

			// Hard delete from DB (artifacts + artifact_nodes cascade)
			if _, err := j.db.ExecContext(ctx, "DELETE FROM foghorn.artifacts WHERE artifact_hash = $1", hash); err == nil {
				if artifactType == "clip" {
					clipCount++
				} else {
					dvrCount++
				}
			}
		}
		if clipCount > 0 {
			j.logger.WithField("count", clipCount).Info("Purged old deleted clips")
		}
		if dvrCount > 0 {
			j.logger.WithField("count", dvrCount).Info("Purged old deleted DVRs")
		}
	}

	// Clean up stale artifact_nodes entries (storage gone but entry lingered)
	nodesResult, err := j.db.ExecContext(ctx, `
		DELETE FROM foghorn.artifact_nodes
		WHERE is_orphaned = true
		  AND last_seen_at < NOW() - INTERVAL '7 days'
	`)
	if err != nil {
		j.logger.WithError(err).Error("Failed to purge stale artifact_nodes entries")
	} else if affected, _ := nodesResult.RowsAffected(); affected > 0 {
		j.logger.WithField("count", affected).Info("Purged stale artifact_nodes entries")
	}
}
