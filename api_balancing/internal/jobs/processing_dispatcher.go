package jobs

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
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
	JobTTL     time.Duration // Max time before dispatched job is stale (default: 5m)
}

// JobExhaustedHandler is called when a processing job exceeds max retries.
// Used to reconcile artifact status (mark ready as raw fallback).
type JobExhaustedHandler func(ctx context.Context, jobID, artifactHash string)

type ProcessingDispatcher struct {
	db              *sql.DB
	logger          logging.Logger
	interval        time.Duration
	maxRetries      int
	jobTTL          time.Duration
	stopCh          chan struct{}
	wg              sync.WaitGroup
	configCacher    ProcessConfigCacher
	gatewayResolver GatewayResolver
	onJobExhausted  JobExhaustedHandler
}

// ProcessConfigCacher caches process config for STREAM_PROCESS trigger lookup.
// Implemented by triggers.Processor.
type ProcessConfigCacher interface {
	CacheProcessConfig(internalName, processesJSON string)
}

// GatewayResolver substitutes {{gateway_url}} placeholders in process config JSON.
// Implemented by triggers.Processor. Candidates is an ordered list of cluster IDs
// to try; empty candidates falls back to the resolver's local cluster.
type GatewayResolver interface {
	SubstituteGatewayURL(processesJSON string, candidates []string) string
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
	ProcessesJSON  sql.NullString
	InternalName   sql.NullString
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
		// With progress messages every 30s refreshing updated_at, a 5-minute
		// silence means the Helmsman is gone (not just a slow transcode).
		jobTTL = 5 * time.Minute
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

func (d *ProcessingDispatcher) SetProcessConfigCacher(c ProcessConfigCacher) {
	d.configCacher = c
}

func (d *ProcessingDispatcher) SetGatewayResolver(r GatewayResolver) {
	d.gatewayResolver = r
}

func (d *ProcessingDispatcher) SetJobExhaustedHandler(h JobExhaustedHandler) {
	d.onJobExhausted = h
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

	// Atomically claim queued jobs via CTE — prevents double-dispatch when
	// multiple Foghorn instances poll concurrently. FOR UPDATE SKIP LOCKED
	// ensures each instance claims a non-overlapping set.
	rows, err := d.db.QueryContext(ctx, `
		WITH claimed AS (
			UPDATE foghorn.processing_jobs
			SET status = 'dispatched', updated_at = NOW()
			WHERE job_id IN (
				SELECT job_id FROM foghorn.processing_jobs
				WHERE status = 'queued'
				ORDER BY created_at
				LIMIT 20
				FOR UPDATE SKIP LOCKED
			)
			RETURNING job_id, tenant_id, artifact_hash, job_type, input_codec,
			          output_profiles, retry_count, processes_json
		)
		SELECT c.job_id, c.tenant_id, c.artifact_hash, c.job_type, c.input_codec,
		       c.output_profiles, 'dispatched'::text, c.retry_count,
		       a.s3_url, c.processes_json, a.internal_name
		FROM claimed c
		LEFT JOIN foghorn.artifacts a ON c.artifact_hash = a.artifact_hash
	`)
	if err != nil {
		d.logger.WithError(err).Error("Failed to claim queued processing jobs")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var job processingJob
		if err := rows.Scan(
			&job.JobID, &job.TenantID, &job.ArtifactHash, &job.JobType,
			&job.InputCodec, &job.OutputProfiles, &job.Status, &job.RetryCount,
			&job.S3URL, &job.ProcessesJSON, &job.InternalName,
		); err != nil {
			d.logger.WithError(err).Warn("Failed to scan processing job")
			continue
		}
		d.dispatchJob(ctx, &job)
	}
}

// revertToQueued puts a claimed job back for the next dispatch cycle.
func (d *ProcessingDispatcher) revertToQueued(ctx context.Context, jobID string) {
	if _, err := d.db.ExecContext(ctx, `
		UPDATE foghorn.processing_jobs
		SET status = 'queued', processing_node_id = NULL, updated_at = NOW()
		WHERE job_id = $1
	`, jobID); err != nil {
		d.logger.WithError(err).WithField("job_id", jobID).Warn("Failed to revert job to queued")
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
		d.revertToQueued(ctx, job.JobID)
		return
	}

	sourceURL := ""
	if job.S3URL.Valid {
		presigned, err := control.GeneratePresignedGETForArtifact(ctx, job.S3URL.String)
		if err != nil {
			d.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to generate presigned URL for processing job")
			d.revertToQueued(ctx, job.JobID)
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

	// For HLS sources, generate presigned URLs for each segment
	if job.S3URL.Valid && strings.HasSuffix(strings.ToLower(job.S3URL.String), ".m3u8") && sourceURL != "" {
		if segURLs, err := d.resolveHLSSegmentURLs(ctx, job.S3URL.String, sourceURL); err != nil {
			d.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to resolve HLS segment URLs")
		} else if segURLs != "" {
			params["segment_urls"] = segURLs
		}
	}

	artifactHash := ""
	if job.ArtifactHash.Valid {
		artifactHash = job.ArtifactHash.String
	}

	internalName := ""
	if job.InternalName.Valid {
		internalName = job.InternalName.String
	}

	req := &pb.ProcessingJobRequest{
		JobId:        job.JobID,
		TenantId:     job.TenantID,
		ArtifactHash: artifactHash,
		SourceUrl:    sourceURL,
		JobType:      job.JobType,
		Params:       params,
		InternalName: internalName,
	}
	if job.ProcessesJSON.Valid {
		resolved := job.ProcessesJSON.String
		if d.gatewayResolver != nil {
			// Queue jobs do not carry origin/official cluster IDs; nil candidates
			// resolves against the resolver's local cluster.
			resolved = d.gatewayResolver.SubstituteGatewayURL(resolved, nil)
		}
		req.ProcessesJson = resolved

		// Cache process config for STREAM_PROCESS trigger before dispatching
		if d.configCacher != nil && artifactHash != "" && resolved != "" {
			d.configCacher.CacheProcessConfig(artifactHash, resolved)
		}
	}

	if err := control.SendProcessingJob(nodeID, req); err != nil {
		d.logger.WithError(err).WithFields(logging.Fields{
			"job_id":  job.JobID,
			"node_id": nodeID,
		}).Warn("Failed to dispatch processing job")
		d.revertToQueued(ctx, job.JobID)
		return
	}

	// Job was already claimed as 'dispatched' by the CTE; record routing metadata
	_, err := d.db.ExecContext(ctx, `
		UPDATE foghorn.processing_jobs
		SET processing_node_id = $2,
		    routing_reason = $3,
		    started_at = NOW(),
		    updated_at = NOW()
		WHERE job_id = $1
	`, job.JobID, nodeID, reason)
	if err != nil {
		d.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to update job routing metadata")
	}

	d.logger.WithFields(logging.Fields{
		"job_id":   job.JobID,
		"job_type": job.JobType,
		"node_id":  nodeID,
		"reason":   reason,
	}).Info("Dispatched processing job")
}

// resolveHLSSegmentURLs fetches an HLS manifest, parses segment filenames,
// and generates presigned GET URLs for each segment. Returns newline-separated
// "filename=presignedURL" pairs for Helmsman's rewriteHLSManifest.
func (d *ProcessingDispatcher) resolveHLSSegmentURLs(ctx context.Context, s3URL, manifestPresignedURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestPresignedURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest returned %d", resp.StatusCode)
	}

	// S3 key base directory (e.g. "tenant/hash" from "s3://bucket/tenant/hash/index.m3u8")
	s3Key := s3URL
	if strings.HasPrefix(s3URL, "s3://") {
		parts := strings.SplitN(s3URL[5:], "/", 2)
		if len(parts) == 2 {
			s3Key = parts[1]
		}
	}
	s3Dir := path.Dir(s3Key)
	bucket := ""
	if strings.HasPrefix(s3URL, "s3://") {
		parts := strings.SplitN(s3URL[5:], "/", 2)
		if len(parts) >= 1 {
			bucket = parts[0]
		}
	}

	var pairs []string
	presignURI := func(name string) {
		segS3URL := fmt.Sprintf("s3://%s/%s/%s", bucket, s3Dir, name)
		presigned, err := control.GeneratePresignedGETForArtifact(ctx, segS3URL)
		if err != nil {
			d.logger.WithFields(logging.Fields{
				"uri":   name,
				"error": err,
			}).Warn("Failed to presign HLS URI")
			return
		}
		pairs = append(pairs, name+"="+presigned)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			// Presign URIs embedded in HLS tags (#EXT-X-KEY, #EXT-X-MAP, etc.)
			if uri := extractHLSTagURI(line); uri != "" && !strings.HasPrefix(uri, "http") {
				presignURI(uri)
			}
			continue
		}
		presignURI(line)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading manifest: %w", err)
	}

	return strings.Join(pairs, "\n"), nil
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

	// Fail jobs that exceeded max retries and reconcile their artifacts
	rows, err := d.db.QueryContext(ctx, `
		UPDATE foghorn.processing_jobs
		SET status = 'failed',
		    error_message = 'max retries exceeded',
		    updated_at = NOW()
		WHERE status IN ('dispatched', 'processing')
		  AND updated_at < $1
		  AND retry_count >= $2
		RETURNING job_id, artifact_hash
	`, ttlCutoff, d.maxRetries)
	if err != nil {
		d.logger.WithError(err).Warn("Failed to mark exhausted processing jobs as failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var jobID string
		var artifactHash sql.NullString
		if scanErr := rows.Scan(&jobID, &artifactHash); scanErr != nil {
			continue
		}
		d.logger.WithFields(logging.Fields{
			"job_id":        jobID,
			"artifact_hash": artifactHash.String,
		}).Warn("Processing job exhausted max retries")
		if d.onJobExhausted != nil && artifactHash.Valid {
			d.onJobExhausted(ctx, jobID, artifactHash.String)
		}
	}
}

// InsertProcessingJob creates a new processing job. Exported for use by vod_pipeline.
func InsertProcessingJob(ctx context.Context, db *sql.DB, tenantID, artifactHash, jobType string, parentJobID *string, processesJSON string) (string, error) {
	jobID := uuid.New().String()
	var parentID *string
	if parentJobID != nil && *parentJobID != "" {
		parentID = parentJobID
	}
	var pJSON *string
	if processesJSON != "" {
		pJSON = &processesJSON
	}

	// Serialize enqueue per artifact/job-type so retry-after-timeout returns the
	// existing active job instead of creating a duplicate queued job.
	if artifactHash == "" {
		_, err := db.ExecContext(ctx, `
			INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, parent_job_id, processes_json)
			VALUES ($1, $2, $3, $4, 'queued', $5, $6)
		`, jobID, tenantID, artifactHash, jobType, parentID, pJSON)
		return jobID, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() {
		if tx != nil {
			rollbackErr := tx.Rollback()
			if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				_ = rollbackErr
			}
		}
	}()

	if _, lockErr := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, artifactHash, jobType); lockErr != nil {
		return "", lockErr
	}

	var existingJobID string
	err = tx.QueryRowContext(ctx, `
		SELECT job_id
		FROM foghorn.processing_jobs
		WHERE artifact_hash = $1
		  AND job_type = $2
		  AND status IN ('queued', 'dispatched', 'processing')
		ORDER BY created_at
		LIMIT 1
	`, artifactHash, jobType).Scan(&existingJobID)
	switch {
	case err == nil:
		if commitErr := tx.Commit(); commitErr != nil {
			return "", commitErr
		}
		tx = nil
		return existingJobID, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, parent_job_id, processes_json)
		VALUES ($1, $2, $3, $4, 'queued', $5, $6)
	`, jobID, tenantID, artifactHash, jobType, parentID, pJSON); err != nil {
		return "", err
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return "", commitErr
	}
	tx = nil
	return jobID, nil
}

// extractHLSTagURI extracts the URI value from HLS tags like
// #EXT-X-KEY:METHOD=AES-128,URI="key.bin" or #EXT-X-MAP:URI="init.mp4".
func extractHLSTagURI(line string) string {
	idx := strings.Index(line, `URI="`)
	if idx < 0 {
		return ""
	}
	start := idx + 5
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return ""
	}
	return line[start : start+end]
}
