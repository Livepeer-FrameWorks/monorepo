package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/pkg/logging"
)

// RetentionJob identifies expired assets and marks them as deleted
// This triggers the standard deletion flow:
// 1. Mark as deleted in DB
// 2. OrphanCleanupJob detects deleted record with artifacts
// 3. OrphanCleanupJob sends delete request to storage node (Helmsman)
// 4. Helmsman deletes local files (and notifies Foghorn)
// 5. PurgeDeletedJob eventually hard-deletes the DB record
type RetentionJob struct {
	db            *sql.DB
	logger        logging.Logger
	interval      time.Duration
	retentionDays int // Default retention in days
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// RetentionConfig holds configuration for the retention job
type RetentionConfig struct {
	DB            *sql.DB
	Logger        logging.Logger
	Interval      time.Duration // How often to run (default: 1 hour)
	RetentionDays int           // Default retention (default: 30 days)
}

// NewRetentionJob creates a new retention job
func NewRetentionJob(cfg RetentionConfig) *RetentionJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Hour
	}
	retentionDays := cfg.RetentionDays
	if retentionDays == 0 {
		retentionDays = 30
	}
	return &RetentionJob{
		db:            cfg.DB,
		logger:        cfg.Logger,
		interval:      interval,
		retentionDays: retentionDays,
		stopCh:        make(chan struct{}),
	}
}

// Start begins the background retention loop
func (j *RetentionJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Retention job started")
}

// Stop gracefully stops the job
func (j *RetentionJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Retention job stopped")
}

func (j *RetentionJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	// Run once at startup (staggered by 5 minutes)
	time.AfterFunc(5*time.Minute, func() {
		j.scan()
	})

	for {
		select {
		case <-ticker.C:
			j.scan()
		case <-j.stopCh:
			return
		}
	}
}

func (j *RetentionJob) scan() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	j.logger.Info("Starting retention scan")

	// Mark expired artifacts as deleted
	// Uses retention_until when set, falls back to created_at + default retention days
	// This supports both new artifacts (with retention_until) and legacy artifacts (without)
	result, err := j.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET status = 'deleted', updated_at = NOW()
		WHERE status NOT IN ('deleted', 'failed')
		  AND (
			-- Use retention_until if set
			(retention_until IS NOT NULL AND retention_until < NOW())
			OR
			-- Fallback to created_at + default retention for legacy artifacts
			(retention_until IS NULL AND created_at < NOW() - make_interval(days => $1))
		  )
	`, j.retentionDays)

	if err != nil {
		j.logger.WithError(err).Error("Failed to expire artifacts")
	} else if affected, _ := result.RowsAffected(); affected > 0 {
		j.logger.WithField("count", affected).Info("Marked expired artifacts as deleted")
	}
}
