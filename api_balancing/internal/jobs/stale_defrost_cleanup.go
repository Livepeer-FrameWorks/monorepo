package jobs

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"frameworks/pkg/logging"
)

// StaleDefrostCleanupJob resets artifacts stuck in defrosting state.
type StaleDefrostCleanupJob struct {
	db         *sql.DB
	logger     logging.Logger
	interval   time.Duration
	staleAfter time.Duration
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// StaleDefrostCleanupConfig holds configuration for the cleanup job.
type StaleDefrostCleanupConfig struct {
	DB         *sql.DB
	Logger     logging.Logger
	Interval   time.Duration // How often to run (default: 1 minute)
	StaleAfter time.Duration // Reset defrosting artifacts older than this (default: 10 minutes)
}

// NewStaleDefrostCleanupJob creates a new stale defrost cleanup job.
func NewStaleDefrostCleanupJob(cfg StaleDefrostCleanupConfig) *StaleDefrostCleanupJob {
	interval := cfg.Interval
	if interval == 0 {
		interval = 1 * time.Minute
	}
	staleAfter := cfg.StaleAfter
	if staleAfter == 0 {
		staleAfter = 10 * time.Minute
	}
	return &StaleDefrostCleanupJob{
		db:         cfg.DB,
		logger:     cfg.Logger,
		interval:   interval,
		staleAfter: staleAfter,
		stopCh:     make(chan struct{}),
	}
}

// Start begins the background cleanup loop.
func (j *StaleDefrostCleanupJob) Start() {
	j.wg.Add(1)
	go j.run()
	j.logger.Info("Stale defrost cleanup job started")
}

// Stop gracefully stops the job.
func (j *StaleDefrostCleanupJob) Stop() {
	close(j.stopCh)
	j.wg.Wait()
	j.logger.Info("Stale defrost cleanup job stopped")
}

func (j *StaleDefrostCleanupJob) run() {
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

func (j *StaleDefrostCleanupJob) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := j.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		SET storage_location = 's3',
		    defrost_node_id = NULL,
		    defrost_started_at = NULL,
		    updated_at = NOW()
		WHERE storage_location = 'defrosting'
		  AND defrost_started_at IS NOT NULL
		  AND defrost_started_at < NOW() - $1::interval
	`, j.staleAfter.String())
	if err != nil {
		j.logger.WithError(err).Warn("Failed to reset stale defrosting artifacts")
		return
	}

	if result == nil {
		return
	}
	rows, err := result.RowsAffected()
	if err != nil {
		j.logger.WithError(err).Warn("Failed to read stale defrost cleanup count")
		return
	}
	if rows > 0 {
		j.logger.WithField("count", rows).Warn("Reset stale defrosting artifacts")
	}
}
