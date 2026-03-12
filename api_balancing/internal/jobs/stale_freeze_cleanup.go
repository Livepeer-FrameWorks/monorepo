package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/pkg/logging"
)

// StaleFreezeCleanupJob resets artifacts stuck in freezing state.
type StaleFreezeCleanupJob struct {
	db         *sql.DB
	logger     logging.Logger
	interval   time.Duration
	staleAfter time.Duration
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// StaleFreezeCleanupConfig holds configuration for the cleanup job.
type StaleFreezeCleanupConfig struct {
	DB         *sql.DB
	Logger     logging.Logger
	Interval   time.Duration // How often to run (default: 1 minute)
	StaleAfter time.Duration // Reset freezing artifacts older than this (default: 30 minutes)
}

// NewStaleFreezeCleanupJob creates a new stale freeze cleanup job.
func NewStaleFreezeCleanupJob(cfg StaleFreezeCleanupConfig) *StaleFreezeCleanupJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Minute
	}
	staleAfter := cfg.StaleAfter
	if staleAfter == 0 {
		staleAfter = 30 * time.Minute
	}
	return &StaleFreezeCleanupJob{
		db:         cfg.DB,
		logger:     cfg.Logger,
		interval:   interval,
		staleAfter: staleAfter,
		stopCh:     make(chan struct{}),
	}
}

// Start begins the background cleanup loop.
func (j *StaleFreezeCleanupJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Stale freeze cleanup job started")
}

// Stop gracefully stops the job.
func (j *StaleFreezeCleanupJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Stale freeze cleanup job stopped")
}

func (j *StaleFreezeCleanupJob) run() {
	defer j.wg.Done()
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.cleanup()

	for {
		select {
		case <-ticker.C:
			j.cleanup()
		case <-j.stopCh:
			return
		}
	}
}

func (j *StaleFreezeCleanupJob) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	staleAfterSeconds := int64(j.staleAfter.Seconds())
	if staleAfterSeconds <= 0 {
		staleAfterSeconds = 1
	}

	result, err := j.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET storage_location = 'local',
		    sync_status = 'pending',
		    updated_at = NOW()
		WHERE storage_location = 'freezing'
		  AND updated_at < NOW() - ($1 * INTERVAL '1 second')
	`, staleAfterSeconds)
	if err != nil {
		j.logger.WithError(err).Warn("Failed to reset stale freezing artifacts")
		return
	}

	if result == nil {
		return
	}
	rows, err := result.RowsAffected()
	if err != nil {
		j.logger.WithError(err).Warn("Failed to read stale freeze cleanup count")
		return
	}
	if rows > 0 {
		j.logger.WithField("count", rows).Warn("Reset stale freezing artifacts")
	}
}
