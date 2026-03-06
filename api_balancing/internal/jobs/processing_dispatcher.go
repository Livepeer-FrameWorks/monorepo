package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/pkg/logging"

	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
)

type ProcessingDispatcherConfig struct {
	DB         *sql.DB
	Logger     logging.Logger
	Interval   time.Duration // Poll interval (default: 5s)
	MaxRetries int           // Max retry attempts per job (default: 3)
	JobTTL     time.Duration // Max time before dispatched job is stale (default: 30m)
}

type ProcessingDispatcher struct {
	db         *sql.DB
	logger     logging.Logger
	interval   time.Duration
	maxRetries int
	jobTTL     time.Duration
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

type processingJob struct {
	JobID          string
	TenantID       string
	ArtifactHash   sql.NullString
	JobType        string
	InputCodec     sql.NullString
	OutputProfiles json.RawMessage
	Status         string
	RetryCount     int
	S3URL          sql.NullString
}

func NewProcessingDispatcher(cfg ProcessingDispatcherConfig) *ProcessingDispatcher {
	interval := cfg.Interval
	if interval == 0 {
		interval = 5 * time.Second
	}
	maxRetries := cfg.MaxRetries
	if maxRetries == 0 {
		maxRetries = 3
	}
	jobTTL := cfg.JobTTL
	if jobTTL == 0 {
		jobTTL = 30 * time.Minute
	}
	return &ProcessingDispatcher{
		db:         cfg.DB,
		logger:     cfg.Logger,
		interval:   interval,
		maxRetries: maxRetries,
		jobTTL:     jobTTL,
		stopCh:     make(chan struct{}),
	}
}

func (d *ProcessingDispatcher) Start() {
	d.wg.Add(1)
	go d.run()
	d.logger.Info("Processing dispatcher started")
}

func (d *ProcessingDispatcher) Stop() {
	close(d.stopCh)
	d.wg.Wait()
	d.logger.Info("Processing dispatcher stopped")
}

func (d *ProcessingDispatcher) run() {
	defer d.wg.Done()
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.dispatch()
			d.recoverStale()
		case <-d.stopCh:
			return
		}
	}
}

func (d *ProcessingDispatcher) dispatch() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := d.db.QueryContext(ctx, `
		SELECT j.job_id, j.tenant_id, j.artifact_hash, j.job_type, j.input_codec,
		       j.output_profiles, j.status, j.retry_count, a.s3_url
		FROM foghorn.processing_jobs j
		LEFT JOIN foghorn.artifacts a ON j.artifact_hash = a.artifact_hash
		WHERE j.status = 'queued'
		ORDER BY j.created_at
		LIMIT 20
	`)
	if err != nil {
		d.logger.WithError(err).Error("Failed to query queued processing jobs")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var job processingJob
		if err := rows.Scan(
			&job.JobID, &job.TenantID, &job.ArtifactHash, &job.JobType,
			&job.InputCodec, &job.OutputProfiles, &job.Status, &job.RetryCount,
			&job.S3URL,
		); err != nil {
			d.logger.WithError(err).Warn("Failed to scan processing job")
			continue
		}
		d.dispatchJob(ctx, &job)
	}
}

func (d *ProcessingDispatcher) dispatchJob(ctx context.Context, job *processingJob) {
	nodeID, reason := routeProcessingJob(job)
	if nodeID == "" {
		d.logger.WithFields(logging.Fields{
			"job_id":   job.JobID,
			"job_type": job.JobType,
			"reason":   reason,
		}).Debug("No processing node available for job")
		return
	}

	sourceURL := ""
	if job.S3URL.Valid {
		// Foghorn generates a presigned URL from the S3 key for the source
		presigned, err := control.GeneratePresignedGETForArtifact(ctx, job.S3URL.String)
		if err != nil {
			d.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to generate presigned URL for processing job")
			return
		}
		sourceURL = presigned
	}

	// Build params based on job type
	params := map[string]string{}
	if job.OutputProfiles != nil {
		params["output_profiles"] = string(job.OutputProfiles)
	}
	if job.InputCodec.Valid {
		params["input_codec"] = job.InputCodec.String
	}

	artifactHash := ""
	if job.ArtifactHash.Valid {
		artifactHash = job.ArtifactHash.String
	}

	req := &pb.ProcessingJobRequest{
		JobId:        job.JobID,
		TenantId:     job.TenantID,
		ArtifactHash: artifactHash,
		SourceUrl:    sourceURL,
		JobType:      job.JobType,
		Params:       params,
	}

	if err := control.SendProcessingJob(nodeID, req); err != nil {
		d.logger.WithError(err).WithFields(logging.Fields{
			"job_id":  job.JobID,
			"node_id": nodeID,
		}).Warn("Failed to dispatch processing job")
		return
	}

	_, err := d.db.ExecContext(ctx, `
		UPDATE foghorn.processing_jobs
		SET status = 'dispatched',
		    processing_node_id = $2,
		    routing_reason = $3,
		    started_at = NOW(),
		    updated_at = NOW()
		WHERE job_id = $1
	`, job.JobID, nodeID, reason)
	if err != nil {
		d.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to update job status to dispatched")
		return
	}

	d.logger.WithFields(logging.Fields{
		"job_id":   job.JobID,
		"job_type": job.JobType,
		"node_id":  nodeID,
		"reason":   reason,
	}).Info("Dispatched processing job")
}

func (d *ProcessingDispatcher) recoverStale() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Reset stale dispatched jobs back to queued (or failed if retries exhausted)
	ttlCutoff := time.Now().Add(-d.jobTTL)

	// Requeue jobs that haven't exceeded max retries
	result, err := d.db.ExecContext(ctx, `
		UPDATE foghorn.processing_jobs
		SET status = 'queued',
		    processing_node_id = NULL,
		    retry_count = retry_count + 1,
		    updated_at = NOW()
		WHERE status IN ('dispatched', 'processing')
		  AND updated_at < $1
		  AND retry_count < $2
	`, ttlCutoff, d.maxRetries)
	if err != nil {
		d.logger.WithError(err).Warn("Failed to recover stale processing jobs")
		return
	}
	if n, _ := result.RowsAffected(); n > 0 {
		d.logger.WithField("count", n).Info("Recovered stale processing jobs (requeued)")
	}

	// Fail jobs that exceeded max retries
	result, err = d.db.ExecContext(ctx, `
		UPDATE foghorn.processing_jobs
		SET status = 'failed',
		    error_message = 'max retries exceeded',
		    updated_at = NOW()
		WHERE status IN ('dispatched', 'processing')
		  AND updated_at < $1
		  AND retry_count >= $2
	`, ttlCutoff, d.maxRetries)
	if err != nil {
		d.logger.WithError(err).Warn("Failed to mark exhausted processing jobs as failed")
		return
	}
	if n, _ := result.RowsAffected(); n > 0 {
		d.logger.WithField("count", n).Warn("Marked exhausted processing jobs as failed")
	}
}

// InsertProcessingJob creates a new processing job. Exported for use by vod_pipeline.
func InsertProcessingJob(ctx context.Context, db *sql.DB, tenantID, artifactHash, jobType string, parentJobID *string) (string, error) {
	jobID := uuid.New().String()
	var parentID *string
	if parentJobID != nil && *parentJobID != "" {
		parentID = parentJobID
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, parent_job_id)
		VALUES ($1, $2, $3, $4, 'queued', $5)
	`, jobID, tenantID, artifactHash, jobType, parentID)
	return jobID, err
}
