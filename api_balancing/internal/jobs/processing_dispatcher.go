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

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

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
	wakeCh          chan struct{}
	wg              sync.WaitGroup
	configCacher    ProcessConfigCacher
	gatewayResolver GatewayResolver
	onJobExhausted  JobExhaustedHandler
}

var (
	processingWakeMu    sync.Mutex
	processingWakeChans = map[chan struct{}]struct{}{}
)

// ProcessConfigCacher caches process config for STREAM_PROCESS trigger lookup.
// Implemented by triggers.Processor.
type ProcessConfigCacher interface {
	CacheProcessConfig(internalName, processesJSON string)
}

// GatewayResolver fills the Livepeer hardcoded_broadcasters list in process
// config JSON with the registered gateway instances. Implemented by
// triggers.Processor. Candidates is an ordered list of cluster IDs to try;
// empty candidates falls back to the resolver's local cluster.
type GatewayResolver interface {
	ApplyLivepeerBroadcasters(processesJSON string, candidates []string) string
}

type processingJob struct {
	JobID          string
	TenantID       string
	ArtifactHash   sql.NullString
	ArtifactType   sql.NullString
	JobType        string
	InputCodec     sql.NullString
	OutputProfiles sql.NullString
	Status         string
	RetryCount     int
	S3URL          sql.NullString
	SourceURL      sql.NullString
	SourceParams   sql.NullString
	PreferredNode  sql.NullString
	ProcessesJSON  sql.NullString
	InternalName   sql.NullString
	StreamID       sql.NullString
	StreamInternal sql.NullString
}

// updated_at rotates jobs that were claimed but could not be sent, so one
// unavailable source-pinned clip cannot monopolize the dispatcher batch.
const processingJobClaimSQL = `
	WITH claimed AS (
		UPDATE foghorn.processing_jobs
		SET status = 'dispatched', updated_at = NOW()
		WHERE job_id IN (
			SELECT pj.job_id
			FROM foghorn.processing_jobs pj
			LEFT JOIN foghorn.artifacts a ON pj.artifact_hash = a.artifact_hash
			WHERE pj.status = 'queued'
			  AND (a.status IS NULL OR a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted'))
			ORDER BY CASE WHEN a.artifact_type = 'clip' THEN 0 ELSE 1 END, pj.updated_at, pj.created_at
			LIMIT 20
			FOR UPDATE OF pj SKIP LOCKED
		)
		RETURNING job_id, tenant_id, artifact_hash, job_type, input_codec,
		          output_profiles, retry_count, processes_json, source_url,
		          source_params::text, preferred_node_id
	)
	SELECT c.job_id, c.tenant_id, c.artifact_hash, COALESCE(a.artifact_type,''), c.job_type, c.input_codec,
	       c.output_profiles, 'dispatched'::text, c.retry_count,
	       a.s3_url, c.source_url, c.source_params, c.preferred_node_id,
	       c.processes_json, a.internal_name, a.stream_id::text, a.stream_internal_name
	FROM claimed c
	LEFT JOIN foghorn.artifacts a ON c.artifact_hash = a.artifact_hash
`

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
		wakeCh:     make(chan struct{}, 1),
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
	registerProcessingDispatcherWake(d.wakeCh)
	d.wg.Add(1)
	go d.run()
	d.logger.Info("Processing dispatcher started")
}

func (d *ProcessingDispatcher) Stop() {
	unregisterProcessingDispatcherWake(d.wakeCh)
	close(d.stopCh)
	d.wg.Wait()
	d.logger.Info("Processing dispatcher stopped")
}

func (d *ProcessingDispatcher) run() {
	defer d.wg.Done()
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.dispatch()
	d.recoverStale()

	for {
		select {
		case <-ticker.C:
			d.dispatch()
			d.recoverStale()
		case <-d.wakeCh:
			d.dispatch()
		case <-d.stopCh:
			return
		}
	}
}

func registerProcessingDispatcherWake(ch chan struct{}) {
	processingWakeMu.Lock()
	defer processingWakeMu.Unlock()
	processingWakeChans[ch] = struct{}{}
}

func unregisterProcessingDispatcherWake(ch chan struct{}) {
	processingWakeMu.Lock()
	defer processingWakeMu.Unlock()
	delete(processingWakeChans, ch)
}

// NotifyProcessingJobQueued wakes local dispatchers after a durable queue write.
// Polling remains the recovery path for missed notifications and HA peers.
func NotifyProcessingJobQueued() {
	processingWakeMu.Lock()
	defer processingWakeMu.Unlock()
	for ch := range processingWakeChans {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (d *ProcessingDispatcher) dispatch() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Atomically claim queued jobs via CTE — prevents double-dispatch when
	// multiple Foghorn instances poll concurrently. FOR UPDATE SKIP LOCKED
	// ensures each instance claims a non-overlapping set.
	rows, err := d.db.QueryContext(ctx, processingJobClaimSQL)
	if err != nil {
		d.logger.WithError(err).Error("Failed to claim queued processing jobs")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var job processingJob
		if err := rows.Scan(
			&job.JobID, &job.TenantID, &job.ArtifactHash, &job.ArtifactType,
			&job.JobType, &job.InputCodec, &job.OutputProfiles, &job.Status, &job.RetryCount,
			&job.S3URL, &job.SourceURL, &job.SourceParams, &job.PreferredNode,
			&job.ProcessesJSON, &job.InternalName, &job.StreamID, &job.StreamInternal,
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
		d.markArtifactQueued(ctx, job, "no processing node available")
		return
	}

	sourceURL := ""
	if job.SourceURL.Valid && strings.TrimSpace(job.SourceURL.String) != "" {
		sourceURL = strings.TrimSpace(job.SourceURL.String)
	} else if job.S3URL.Valid {
		presigned, err := control.GeneratePresignedGETForArtifact(ctx, job.S3URL.String)
		if err != nil {
			d.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to generate presigned URL for processing job")
			d.revertToQueued(ctx, job.JobID)
			d.markArtifactQueued(ctx, job, "presign failed")
			return
		}
		sourceURL = presigned
	}

	// Build params based on job type
	params := map[string]string{}
	if job.OutputProfiles.Valid && job.OutputProfiles.String != "" {
		params["output_profiles"] = job.OutputProfiles.String
	}
	if job.InputCodec.Valid {
		params["input_codec"] = job.InputCodec.String
	}
	if job.SourceParams.Valid && strings.TrimSpace(job.SourceParams.String) != "" {
		var sourceParams map[string]string
		if err := json.Unmarshal([]byte(job.SourceParams.String), &sourceParams); err != nil {
			d.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to parse processing job source params")
			d.revertToQueued(ctx, job.JobID)
			d.markArtifactQueued(ctx, job, "invalid source params")
			return
		}
		for k, v := range sourceParams {
			params[k] = v
		}
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
	// Foghorn-authoritative runtime name for the processed OUTPUT
	// artifact's DTSH boot post-transcode. Outputs are always vod+.
	if internalName != "" {
		req.OutputRuntimeName = "vod+" + internalName
	}
	if job.ProcessesJSON.Valid {
		resolved := job.ProcessesJSON.String
		if d.gatewayResolver != nil {
			// Queue jobs do not carry origin/official cluster IDs; nil candidates
			// resolves against the resolver's local cluster.
			resolved = d.gatewayResolver.ApplyLivepeerBroadcasters(resolved, nil)
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
		d.markArtifactQueued(ctx, job, "dispatch failed")
		return
	}

	// Job was already claimed as 'dispatched' by the CTE; record routing metadata
	_, err := d.db.ExecContext(ctx, `
		UPDATE foghorn.processing_jobs
		SET status = 'processing',
		    processing_node_id = $2,
		    routing_reason = $3,
		    started_at = NOW(),
		    updated_at = NOW()
		WHERE job_id = $1
	`, job.JobID, nodeID, reason)
	if err != nil {
		d.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to update job routing metadata")
		return
	}
	d.markArtifactProcessing(ctx, job)
	d.emitProcessingStarted(job, nodeID)

	d.logger.WithFields(logging.Fields{
		"job_id":   job.JobID,
		"job_type": job.JobType,
		"node_id":  nodeID,
		"reason":   reason,
	}).Info("Dispatched processing job")
}

func (d *ProcessingDispatcher) markArtifactQueued(ctx context.Context, job *processingJob, reason string) {
	d.markArtifactStatus(ctx, job, "queued", reason)
}

func (d *ProcessingDispatcher) markArtifactProcessing(ctx context.Context, job *processingJob) {
	d.markArtifactStatus(ctx, job, "processing", "")
}

func (d *ProcessingDispatcher) markArtifactStatus(ctx context.Context, job *processingJob, nextStatus, reason string) {
	if job == nil || !job.ArtifactHash.Valid || job.ArtifactHash.String == "" || job.TenantID == "" {
		return
	}
	artifactType := job.ArtifactType.String
	if artifactType != "clip" && artifactType != "vod" {
		return
	}
	if _, err := d.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET status = $3::text,
		       error_message = CASE WHEN $3::text = 'processing' THEN NULL ELSE error_message END,
		       updated_at = NOW()
		 WHERE artifact_hash = $1
		   AND tenant_id::text = $2
		   AND artifact_type IN ('clip', 'vod')
		   AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted')
	`, job.ArtifactHash.String, job.TenantID, nextStatus); err != nil {
		fields := logging.Fields{
			"artifact_hash": job.ArtifactHash.String,
			"status":        nextStatus,
		}
		if reason != "" {
			fields["reason"] = reason
		}
		d.logger.WithError(err).WithFields(fields).Warn("Failed to project processing job status onto artifact")
	}
}

func (d *ProcessingDispatcher) emitProcessingStarted(job *processingJob, nodeID string) {
	if job == nil || !job.ArtifactHash.Valid {
		return
	}
	artifactHash := job.ArtifactHash.String
	tenantID := job.TenantID
	progress := uint32(0)
	vodProgress := int32(0)
	startedAt := time.Now().Unix()

	switch job.ArtifactType.String {
	case "clip":
		data := &pb.ClipLifecycleData{
			Stage:           pb.ClipLifecycleData_STAGE_PROGRESS,
			ClipHash:        artifactHash,
			ProgressPercent: &progress,
			StartedAt:       &startedAt,
		}
		if tenantID != "" {
			data.TenantId = &tenantID
		}
		if job.StreamID.Valid && job.StreamID.String != "" {
			streamID := job.StreamID.String
			data.StreamId = &streamID
		}
		if job.StreamInternal.Valid && job.StreamInternal.String != "" {
			streamInternalName := job.StreamInternal.String
			data.StreamInternalName = &streamInternalName
		}
		if nodeID != "" {
			data.NodeId = &nodeID
		}
		artifactoutbox.EnqueueClipLifecycleLogged(data)
	case "vod":
		data := &pb.VodLifecycleData{
			Status:      pb.VodLifecycleData_STATUS_PROCESSING,
			VodHash:     artifactHash,
			ProgressPct: &vodProgress,
			StartedAt:   &startedAt,
		}
		if tenantID != "" {
			data.TenantId = &tenantID
		}
		if nodeID != "" {
			data.NodeId = &nodeID
		}
		artifactoutbox.EnqueueVodLifecycleLogged(data)
	}
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
		WITH requeued AS (
			UPDATE foghorn.processing_jobs
			SET status = 'queued',
			    processing_node_id = NULL,
			    retry_count = retry_count + 1,
			    updated_at = NOW()
			WHERE status IN ('dispatched', 'processing')
			  AND updated_at < $1
			  AND retry_count < $2
			RETURNING artifact_hash, tenant_id
		)
		UPDATE foghorn.artifacts a
		   SET status = 'queued',
		       updated_at = NOW()
		  FROM requeued r
		 WHERE a.artifact_hash = r.artifact_hash
		   AND a.tenant_id = r.tenant_id
		   AND a.artifact_type IN ('clip', 'vod')
		   AND a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted')
	`, ttlCutoff, d.maxRetries)
	if err != nil {
		d.logger.WithError(err).Warn("Failed to recover stale processing jobs")
		return
	}
	if n, _ := result.RowsAffected(); n > 0 {
		d.logger.WithField("artifacts", n).Info("Recovered stale processing jobs (requeued)")
	}

	// Fail jobs that exceeded max retries, AND the specific class of 'queued'
	// jobs that can never make progress: a node-pinned clip whose source bytes
	// live only on a now-unavailable node. dispatchJob keeps reverting such a
	// job to 'queued' (refreshing updated_at, never incrementing retry_count,
	// never entering dispatched/processing), so the retry-based sweep above can
	// never catch it — without a terminal event it spins forever.
	//
	// The queued-terminal predicate is therefore gated on explicit source-bound
	// intent, NOT on status='queued' alone: it requires a preferred node
	// (preferred_node_id) AND a node-local source (source_kind live / dvr_rolling).
	// A generic load-routed job (no preferred node, e.g. an upload transcode)
	// stays queued and dispatches when capacity returns, so a cluster-wide
	// processing outage no longer permanently fails recoverable jobs. Keyed on
	// created_at, since revert refreshes updated_at every cycle.
	queuedCutoff := time.Now().Add(-d.jobTTL * time.Duration(d.maxRetries+2))
	rows, err := d.db.QueryContext(ctx, `
		WITH failed AS (
			UPDATE foghorn.processing_jobs
			SET status = 'failed',
			    error_message = CASE WHEN status = 'queued'
			        THEN 'stuck queued: node-pinned source unavailable'
			        ELSE 'max retries exceeded' END,
			    updated_at = NOW()
			WHERE (status IN ('dispatched', 'processing') AND updated_at < $1 AND retry_count >= $2)
			   OR (status = 'queued' AND created_at < $3
			       AND preferred_node_id IS NOT NULL
			       AND source_params->>'source_kind' IN ('live', 'dvr_rolling'))
			RETURNING job_id, artifact_hash, tenant_id
		)
		SELECT f.job_id,
		       f.artifact_hash,
		       COALESCE(a.artifact_type, ''),
		       COALESCE(a.tenant_id::text, f.tenant_id::text, ''),
		       COALESCE(a.stream_id::text, ''),
		       COALESCE(a.stream_internal_name, '')
		  FROM failed f
		  LEFT JOIN foghorn.artifacts a ON f.artifact_hash = a.artifact_hash
	`, ttlCutoff, d.maxRetries, queuedCutoff)
	if err != nil {
		d.logger.WithError(err).Warn("Failed to mark exhausted processing jobs as failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var jobID string
		var artifactHash sql.NullString
		var artifactType, tenantID, streamID, streamInternalName string
		if scanErr := rows.Scan(&jobID, &artifactHash, &artifactType, &tenantID, &streamID, &streamInternalName); scanErr != nil {
			continue
		}
		d.logger.WithFields(logging.Fields{
			"job_id":        jobID,
			"artifact_hash": artifactHash.String,
		}).Warn("Processing job exhausted max retries")
		if artifactHash.Valid {
			switch artifactType {
			case "clip":
				d.failClipArtifact(ctx, artifactHash.String, tenantID, streamID, streamInternalName, "max retries exceeded")
			case "vod":
				d.failVODArtifact(ctx, artifactHash.String, tenantID, "max retries exceeded")
			}
		}
		if d.onJobExhausted != nil && artifactHash.Valid {
			d.onJobExhausted(ctx, jobID, artifactHash.String)
		}
	}
}

func (d *ProcessingDispatcher) failVODArtifact(ctx context.Context, artifactHash, tenantID, errorMsg string) {
	if _, err := d.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET status = 'failed',
		       error_message = $3,
		       updated_at = NOW()
		 WHERE artifact_hash = $1
		   AND tenant_id::text = $2
	`, artifactHash, tenantID, errorMsg); err != nil {
		d.logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Failed to mark exhausted VOD artifact failed")
	}
}

func (d *ProcessingDispatcher) failClipArtifact(ctx context.Context, artifactHash, tenantID, streamID, streamInternalName, errorMsg string) {
	if _, err := d.db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET status = 'failed',
		       error_message = $3,
		       updated_at = NOW()
		 WHERE artifact_hash = $1
		   AND tenant_id::text = $2
	`, artifactHash, tenantID, errorMsg); err != nil {
		d.logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Failed to mark exhausted clip artifact failed")
	}

	data := &pb.ClipLifecycleData{
		Stage:    pb.ClipLifecycleData_STAGE_FAILED,
		ClipHash: artifactHash,
		Error:    &errorMsg,
	}
	if tenantID != "" {
		data.TenantId = &tenantID
	}
	if streamID != "" {
		data.StreamId = &streamID
	}
	if streamInternalName != "" {
		data.StreamInternalName = &streamInternalName
	}
	artifactoutbox.EnqueueClipLifecycleLogged(data)
}

// InsertProcessingJob creates a new processing job. Exported for use by vod_pipeline.
func InsertProcessingJob(ctx context.Context, db *sql.DB, tenantID, artifactHash, jobType string, parentJobID *string, processesJSON string) (string, error) {
	return InsertProcessingJobWithSource(ctx, db, tenantID, artifactHash, jobType, parentJobID, processesJSON, "")
}

func InsertProcessingJobWithSource(ctx context.Context, db *sql.DB, tenantID, artifactHash, jobType string, parentJobID *string, processesJSON, sourceURL string) (string, error) {
	return InsertProcessingJobWithSourceParams(ctx, db, tenantID, artifactHash, jobType, parentJobID, processesJSON, sourceURL, nil, "")
}

func InsertProcessingJobWithSourceParams(ctx context.Context, db *sql.DB, tenantID, artifactHash, jobType string, parentJobID *string, processesJSON, sourceURL string, sourceParams map[string]string, preferredNodeID string) (string, error) {
	jobID := uuid.New().String()
	var parentID *string
	if parentJobID != nil && *parentJobID != "" {
		parentID = parentJobID
	}
	var pJSON *string
	if processesJSON != "" {
		pJSON = &processesJSON
	}
	var srcURL *string
	if strings.TrimSpace(sourceURL) != "" {
		trimmed := strings.TrimSpace(sourceURL)
		srcURL = &trimmed
	}
	var srcParams *string
	if len(sourceParams) > 0 {
		b, err := json.Marshal(sourceParams)
		if err != nil {
			return "", err
		}
		s := string(b)
		srcParams = &s
	}
	var preferredNode *string
	if strings.TrimSpace(preferredNodeID) != "" {
		trimmed := strings.TrimSpace(preferredNodeID)
		preferredNode = &trimmed
	}

	// Serialize enqueue per artifact/job-type so retry-after-timeout returns the
	// existing active job instead of creating a duplicate queued job.
	if artifactHash == "" {
		err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			_, err := db.ExecContext(ctx, `
			INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, parent_job_id, processes_json, source_url, source_params, preferred_node_id)
			VALUES ($1, $2, $3, $4, 'queued', $5, $6, $7, $8::jsonb, $9)
		`, jobID, tenantID, artifactHash, jobType, parentID, pJSON, srcURL, srcParams, preferredNode)
			return err
		})
		if err == nil {
			NotifyProcessingJobQueued()
		}
		return jobID, err
	}

	resultJobID := jobID
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		if _, lockErr := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, artifactHash, jobType); lockErr != nil {
			return lockErr
		}

		var existingJobID string
		err := tx.QueryRowContext(ctx, `
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
			resultJobID = existingJobID
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		if _, err := tx.ExecContext(ctx, `
		INSERT INTO foghorn.processing_jobs (job_id, tenant_id, artifact_hash, job_type, status, parent_job_id, processes_json, source_url, source_params, preferred_node_id)
		VALUES ($1, $2, $3, $4, 'queued', $5, $6, $7, $8::jsonb, $9)
	`, jobID, tenantID, artifactHash, jobType, parentID, pJSON, srcURL, srcParams, preferredNode); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET status = 'queued',
		       updated_at = NOW()
		 WHERE artifact_hash = $1
		   AND tenant_id::text = $2
		   AND artifact_type = 'clip'
		   AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted')
	`, artifactHash, tenantID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	NotifyProcessingJobQueued()
	return resultJobID, nil
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
