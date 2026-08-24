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
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
)

type ProcessingDispatcherConfig struct {
	DB         *sql.DB
	Logger     logging.Logger
	Interval   time.Duration // Poll interval (default: 5s)
	MaxRetries int           // Max retry attempts per job (default: 3)
	JobTTL     time.Duration // Max time before dispatched job is stale (default: 5m)
}

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
	ApplyLivepeerWorkload(processesJSON, workload string) string
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
	DurableLocal   bool
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
		wakeCh:     make(chan struct{}, 1),
	}
}

func (d *ProcessingDispatcher) SetProcessConfigCacher(c ProcessConfigCacher) {
	d.configCacher = c
}

func (d *ProcessingDispatcher) SetGatewayResolver(r GatewayResolver) {
	d.gatewayResolver = r
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
	rows, err := foghorndb.New(d.db).ClaimQueuedProcessingJobs(ctx)
	if err != nil {
		d.logger.WithError(err).Error("Failed to claim queued processing jobs")
		return
	}
	for _, row := range rows {
		job := processingJob{
			JobID: row.JobID, TenantID: row.TenantID, ArtifactHash: row.ArtifactHash,
			ArtifactType: sql.NullString{String: row.ArtifactType, Valid: row.ArtifactType != ""}, JobType: row.JobType,
			InputCodec: row.InputCodec, OutputProfiles: row.OutputProfiles,
			Status: row.Status, RetryCount: int(row.RetryCount.Int32), S3URL: row.S3Url,
			SourceURL: row.SourceUrl, SourceParams: row.SourceParams,
			PreferredNode: row.PreferredNodeID, ProcessesJSON: row.ProcessesJson, InternalName: row.InternalName,
			StreamID: sql.NullString{String: row.StreamID, Valid: row.StreamID != ""}, StreamInternal: row.StreamInternalName,
			DurableLocal: row.DurableBackendLocal,
		}
		d.dispatchJob(ctx, &job)
	}
}

// revertToQueued puts a claimed job back for the next dispatch cycle.
func (d *ProcessingDispatcher) revertToQueued(ctx context.Context, jobID string) {
	if err := foghorndb.New(d.db).RevertProcessingJobToQueued(ctx, jobID); err != nil {
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
		// FAIL CLOSED for a non-locally-backed input: this mints a LOCAL presigned URL, so signing a
		// federation-adopted remote object here would point processing at the wrong backend (bucket-name equality
		// alone is not ownership — providers can share a bucket name on different endpoints). Only the persisted
		// durable_backend_local fact proves the bytes are on THIS cell's backend. A row that is genuinely local
		// but not yet attributed reverts and self-heals on the reconciler's next durable_backend_local pass;
		// cross-cluster processing input resolved through the owning provider is the cross-cluster RFC.
		if !job.DurableLocal {
			d.logger.WithFields(logging.Fields{"job_id": job.JobID, "artifact_hash": job.ArtifactHash}).
				Warn("Processing input is not locally backed (durable_backend_local=false); not presigning locally")
			d.revertToQueued(ctx, job.JobID)
			d.markArtifactQueued(ctx, job, "input not locally backed")
			return
		}
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

	req := &ipcpb.ProcessingJobRequest{
		JobId:           job.JobID,
		TenantId:        job.TenantID,
		ArtifactHash:    artifactHash,
		SourceUrl:       sourceURL,
		JobType:         job.JobType,
		Params:          params,
		InternalName:    internalName,
		ProcessingClass: mist.ProcessingClassVideoTranscode,
	}
	// Foghorn-authoritative runtime name for the processed OUTPUT
	// artifact's DTSH boot post-transcode. Outputs are always vod+.
	if internalName != "" {
		req.OutputRuntimeName = "vod+" + internalName
	}
	if job.ProcessesJSON.Valid {
		resolved := job.ProcessesJSON.String
		resolved = mist.MaskLivepeerSourceForVOD(resolved)
		// One-shot job: finished processes must not be supervisor-restarted
		// (restart churn blocks the buffer's output-drain signal).
		resolved = mist.DisableProcessRestarts(resolved)
		if d.gatewayResolver != nil {
			// Queue jobs do not carry origin/official cluster IDs; nil candidates
			// resolves against the resolver's local cluster.
			resolved = d.gatewayResolver.ApplyLivepeerBroadcasters(resolved, nil)
			resolved = d.gatewayResolver.ApplyLivepeerWorkload(resolved, mist.WorkloadVOD)
		}
		req.ProcessesJson = resolved

		// Cache process config for STREAM_PROCESS trigger before dispatching
		if d.configCacher != nil && artifactHash != "" && resolved != "" {
			d.configCacher.CacheProcessConfig(artifactHash, resolved)
		}
	}

	// Persist the node assignment BEFORE the node can report a result. processing_node_id is what the
	// completion/failure/progress handlers bind the reporting node against, so writing it here — guarded on the
	// job still being 'dispatched' — closes the window in which a job handed to node A could be completed,
	// failed, or progressed by any other authenticated node B that learns the job id. commitDispatched re-asserts
	// the same value in its status='dispatched'→'processing' transition; a fast terminal report leaves status
	// past 'dispatched', so this no-ops. A transient failure reverts to queued for retry.
	//
	// The guarded UPDATE MUST have transitioned exactly this row: if a concurrent transition (cancel, delete,
	// or a competing dispatcher) already moved the job out of 'dispatched', RowsAffected==0 and we must NOT send
	// — sending a job that is no longer ours risks duplicate execution.
	queries := foghorndb.New(d.db)
	n, assignErr := queries.AssignProcessingJobNode(ctx, foghorndb.AssignProcessingJobNodeParams{JobID: job.JobID, ProcessingNodeID: sql.NullString{String: nodeID, Valid: nodeID != ""}})
	if assignErr != nil {
		d.logger.WithError(assignErr).WithFields(logging.Fields{
			"job_id":  job.JobID,
			"node_id": nodeID,
		}).Warn("Failed to persist processing node assignment before dispatch")
		d.revertToQueued(ctx, job.JobID)
		d.markArtifactQueued(ctx, job, "assignment persist failed")
		return
	}
	if n == 0 {
		d.logger.WithField("job_id", job.JobID).Info("Dispatch: job left 'dispatched' before assignment persisted (cancelled/raced); not sending")
		return
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

	if err := d.commitDispatched(ctx, job, nodeID, reason); err != nil {
		d.logger.WithError(err).WithField("job_id", job.JobID).Warn("Failed to commit dispatched job state")
		return
	}

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
	if err := foghorndb.New(d.db).ProjectProcessingArtifactStatus(ctx, foghorndb.ProjectProcessingArtifactStatusParams{ArtifactHash: job.ArtifactHash.String, TenantID: job.TenantID, TargetStatus: nextStatus}); err != nil {
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

// commitDispatched records the job's routing metadata, flips the clip/vod artifact to
// 'processing', and enqueues its ONE processing-started lifecycle event in a SINGLE
// transaction. foghorn.processing_jobs and foghorn.artifacts live in the same DB
// (d.db), so all three writes — the job update, the artifact-state transition, and the
// durable outbox row — commit together or not at all. A failed enqueue rolls the whole
// transition back instead of being swallowed by the fire-and-forget ...Logged helper.
// The lifecycle event is emitted only when THIS tx actually transitioned the artifact,
// so a concurrently-terminal artifact never produces a false processing-started event
// and exactly one event is published per successful dispatch.
func (d *ProcessingDispatcher) commitDispatched(ctx context.Context, job *processingJob, nodeID, reason string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin dispatched tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()

	// The job was claimed as 'dispatched' by the CTE, but the node is already running and can report
	// completion before this commit. Guard on status='dispatched' so a fast 'completed'/'failed' is NOT
	// rewritten back to 'processing'. RowsAffected==0 => the terminal report won the race: treat it as an
	// idempotent no-op (skip the artifact transition and the STARTED event) and roll back.
	queries := foghorndb.New(tx)
	n, execErr := queries.CommitDispatchedProcessingJob(ctx, foghorndb.CommitDispatchedProcessingJobParams{JobID: job.JobID, ProcessingNodeID: sql.NullString{String: nodeID, Valid: nodeID != ""}, RoutingReason: sql.NullString{String: reason, Valid: reason != ""}})
	if execErr != nil {
		return fmt.Errorf("update job routing metadata: %w", execErr)
	}
	if n == 0 {
		d.logger.WithField("job_id", job.JobID).Info("commitDispatched: job already left 'dispatched' (fast completion won); no-op")
		return nil
	}

	// Project the job state onto the clip/vod artifact. Guarded + tenant-scoped so a
	// concurrently deleted/expired/aborted/ready artifact is never resurrected, and a
	// hash collision never crosses tenants. Only a real transition emits the event.
	transitioned := false
	if job != nil && job.ArtifactHash.Valid && job.ArtifactHash.String != "" && job.TenantID != "" &&
		(job.ArtifactType.String == "clip" || job.ArtifactType.String == "vod") {
		n, artErr := queries.MarkProcessingArtifactStarted(ctx, foghorndb.MarkProcessingArtifactStartedParams{ArtifactHash: job.ArtifactHash.String, TenantID: job.TenantID})
		if artErr != nil {
			return fmt.Errorf("project processing status onto artifact: %w", artErr)
		}
		transitioned = n > 0
	}

	if transitioned {
		if enqErr := d.enqueueProcessingStartedTx(ctx, tx, job, nodeID); enqErr != nil {
			return fmt.Errorf("enqueue processing-started lifecycle: %w", enqErr)
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return fmt.Errorf("commit dispatched: %w", commitErr)
	}
	committed = true
	return nil
}

// enqueueProcessingStartedTx writes the ONE processing-started lifecycle event
// (clip STAGE_PROGRESS / vod STATUS_PROCESSING) onto the caller's transaction using
// the ...Tx outbox variant, so the enqueue failure propagates and rolls the dispatch
// state back rather than being logged-and-dropped.
func (d *ProcessingDispatcher) enqueueProcessingStartedTx(ctx context.Context, tx *sql.Tx, job *processingJob, nodeID string) error {
	if job == nil || !job.ArtifactHash.Valid {
		return nil
	}
	artifactHash := job.ArtifactHash.String
	tenantID := job.TenantID
	progress := uint32(0)
	vodProgress := int32(0)
	startedAt := time.Now().Unix()

	switch job.ArtifactType.String {
	case "clip":
		data := &ipcpb.ClipLifecycleData{
			Stage:           ipcpb.ClipLifecycleData_STAGE_PROGRESS,
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
		return artifactoutbox.EnqueueClipLifecycleTx(ctx, tx, data)
	case "vod":
		data := &ipcpb.VodLifecycleData{
			Status:      ipcpb.VodLifecycleData_STATUS_PROCESSING,
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
		return artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, data)
	}
	return nil
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
	queries := foghorndb.New(d.db)
	n, err := queries.RequeueStaleProcessingJobs(ctx, foghorndb.RequeueStaleProcessingJobsParams{UpdatedAt: sql.NullTime{Time: ttlCutoff, Valid: true}, RetryCount: sql.NullInt32{Int32: int32(d.maxRetries), Valid: true}})
	if err != nil {
		d.logger.WithError(err).Warn("Failed to recover stale processing jobs")
		return
	}
	if n > 0 {
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
	// Enumerate exhausted/stuck candidates WITHOUT terminalizing them — each is then failed in its
	// own transaction so the job-failed, artifact-failed, and lifecycle-outbox writes commit
	// together (or not at all). The old batch CTE terminalized the job first and updated the
	// artifact + telemetry separately, which could leave a failed job with a live artifact.
	jobIDs, err := queries.ListExhaustedProcessingJobIDs(ctx, foghorndb.ListExhaustedProcessingJobIDsParams{UpdatedAt: sql.NullTime{Time: ttlCutoff, Valid: true}, RetryCount: sql.NullInt32{Int32: int32(d.maxRetries), Valid: true}, CreatedAt: sql.NullTime{Time: queuedCutoff, Valid: true}})
	if err != nil {
		d.logger.WithError(err).Warn("Failed to list exhausted processing jobs")
		return
	}
	for _, jobID := range jobIDs {
		d.failExhaustedJobAtomic(ctx, jobID, ttlCutoff, queuedCutoff)
	}
}

// failExhaustedJobAtomic drives one exhausted/stuck job to its terminal failed state as a single
// transaction: it re-applies the exhaustion predicate under the job lock (so a job that recovered
// or completed between enumeration and now is a no-op), marks the job failed, and — for clip/vod
// artifacts — flips the artifact to failed and enqueues the failure lifecycle on the SAME tx. A
// failed job is never left with a live artifact or a lost telemetry event.
func (d *ProcessingDispatcher) failExhaustedJobAtomic(ctx context.Context, jobID string, ttlCutoff, queuedCutoff time.Time) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		d.logger.WithError(err).WithField("job_id", jobID).Warn("Failed to begin exhaustion transaction")
		return
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()

	// Terminalize the job ONLY if it still matches the exhausted/stuck predicate, and resolve its
	// artifact in the same tx. ErrNoRows ⇒ the job recovered/completed since enumeration ⇒ no-op.
	queries := foghorndb.New(tx)
	failed, scanErr := queries.FailExhaustedProcessingJob(ctx, foghorndb.FailExhaustedProcessingJobParams{JobID: jobID, UpdatedAt: sql.NullTime{Time: ttlCutoff, Valid: true}, RetryCount: sql.NullInt32{Int32: int32(d.maxRetries), Valid: true}, CreatedAt: sql.NullTime{Time: queuedCutoff, Valid: true}})
	if errors.Is(scanErr, sql.ErrNoRows) {
		return // recovered/completed since enumeration
	}
	if scanErr != nil {
		d.logger.WithError(scanErr).WithField("job_id", jobID).Warn("Failed to terminalize exhausted job")
		return
	}
	artifactHash, artifactType, tenantID := failed.ArtifactHash, failed.ArtifactType, failed.TenantID
	streamID, streamInternalName, errorMsg := failed.StreamID, failed.StreamInternalName, failed.ErrorMessage.String
	d.logger.WithFields(logging.Fields{"job_id": jobID, "artifact_hash": artifactHash.String}).Warn("Processing job exhausted; failing atomically")

	if artifactHash.Valid && (artifactType == "clip" || artifactType == "vod") {
		// Only fail a PRE-TERMINAL, tenant-matching artifact — never resurrect one that was
		// concurrently deleted/expired/aborted/completed, and never cross tenants on a hash collision.
		artFailed, artErr := queries.MarkExhaustedArtifactFailed(ctx, foghorndb.MarkExhaustedArtifactFailedParams{ArtifactHash: artifactHash.String, ErrorMessage: sql.NullString{String: errorMsg, Valid: errorMsg != ""}, TenantID: tenantID})
		if artErr != nil {
			d.logger.WithError(artErr).WithField("artifact_hash", artifactHash.String).Warn("Failed to mark exhausted artifact failed; rolling back")
			return
		}
		// Only emit the FAILED lifecycle if THIS tx actually transitioned the artifact. A 0-row
		// result means it was already terminal (concurrently ready/deleted/expired/aborted) — the
		// job still fails, but a false FAILED analytics event must not be emitted.
		if artFailed == 0 {
			// nothing to publish; commit the job-failed transition only
		} else if artifactType == "clip" {
			clipData := &ipcpb.ClipLifecycleData{Stage: ipcpb.ClipLifecycleData_STAGE_FAILED, ClipHash: artifactHash.String, Error: &errorMsg}
			if tenantID != "" {
				clipData.TenantId = &tenantID
			}
			if streamID != "" {
				clipData.StreamId = &streamID
			}
			if streamInternalName != "" {
				clipData.StreamInternalName = &streamInternalName
			}
			if enqErr := artifactoutbox.EnqueueClipLifecycleTx(ctx, tx, clipData); enqErr != nil {
				d.logger.WithError(enqErr).WithField("artifact_hash", artifactHash.String).Warn("Failed to enqueue clip failure lifecycle; rolling back")
				return
			}
		} else {
			vodData := &ipcpb.VodLifecycleData{Status: ipcpb.VodLifecycleData_STATUS_FAILED, VodHash: artifactHash.String, Error: &errorMsg}
			if tenantID != "" {
				vodData.TenantId = &tenantID
			}
			if enqErr := artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData); enqErr != nil {
				d.logger.WithError(enqErr).WithField("artifact_hash", artifactHash.String).Warn("Failed to enqueue vod failure lifecycle; rolling back")
				return
			}
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		d.logger.WithError(commitErr).WithField("job_id", jobID).Warn("Failed to commit exhaustion; will retry")
		return
	}
	committed = true
}

// InsertProcessingJob creates a new processing job. Exported for use by vod_pipeline.
func InsertProcessingJob(ctx context.Context, db *sql.DB, tenantID, artifactHash, jobType string, parentJobID *string, processesJSON string) (string, error) {
	return InsertProcessingJobWithSource(ctx, db, tenantID, artifactHash, jobType, parentJobID, processesJSON, "")
}

func InsertProcessingJobWithSource(ctx context.Context, db *sql.DB, tenantID, artifactHash, jobType string, parentJobID *string, processesJSON, sourceURL string) (string, error) {
	return InsertProcessingJobWithSourceParams(ctx, db, tenantID, artifactHash, jobType, parentJobID, processesJSON, sourceURL, nil, "")
}

func InsertProcessingJobWithSourceParams(ctx context.Context, db *sql.DB, tenantID, artifactHash, jobType string, parentJobID *string, processesJSON, sourceURL string, sourceParams map[string]string, preferredNodeID string) (string, error) {
	// The hashless path has no artifact to dedup against, so it skips the advisory
	// lock/dedup entirely; a plain retried INSERT is sufficient.
	if artifactHash == "" {
		jobID := uuid.New().String()
		parentID, pJSON, srcURL, srcParams, preferredNode, err := processingJobInsertArgs(parentJobID, processesJSON, sourceURL, sourceParams, preferredNodeID)
		if err != nil {
			return "", err
		}
		queries := foghorndb.New(db)
		err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			return queries.InsertProcessingJob(ctx, processingJobParams(jobID, tenantID, artifactHash, jobType, parentID, pJSON, srcURL, srcParams, preferredNode))
		})
		if err == nil {
			NotifyProcessingJobQueued()
		}
		return jobID, err
	}

	// Serialize enqueue per artifact/job-type so retry-after-timeout returns the
	// existing active job instead of creating a duplicate queued job. The advisory
	// lock is transaction-scoped (pg_advisory_xact_lock), so it composes with the
	// caller's tx in the ...Tx variant below.
	var resultJobID string
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		id, insErr := InsertProcessingJobWithSourceParamsTx(ctx, tx, tenantID, artifactHash, jobType, parentJobID, processesJSON, sourceURL, sourceParams, preferredNodeID)
		if insErr != nil {
			return insErr
		}
		resultJobID = id
		return nil
	})
	if err != nil {
		return "", err
	}
	NotifyProcessingJobQueued()
	return resultJobID, nil
}

// InsertProcessingJobWithSourceParamsTx inserts (or dedups to an existing) processing job on the
// CALLER's transaction, so the job row can commit atomically with the caller's artifact row and its
// lifecycle event. This is what lets CreateClip persist the clip artifact, the QUEUED lifecycle
// event, AND the processing job in ONE transaction — a crash between them can no longer leave a
// permanently-'queued' clip with no job.
//
// It takes pg_advisory_xact_lock(hashtext(artifact_hash), hashtext(job_type)): that lock is
// TRANSACTION-scoped (released on the caller's COMMIT/ROLLBACK), so it correctly composes with the
// outer tx and still serializes concurrent enqueues per artifact+job_type. The caller owns commit
// AND the post-commit NotifyProcessingJobQueued() wake — this function must NOT notify, because the
// row is not durable until the caller commits. artifact_hash is required (the dedup + lock are keyed
// on it); the hashless path lives in InsertProcessingJobWithSourceParams.
func InsertProcessingJobWithSourceParamsTx(ctx context.Context, tx *sql.Tx, tenantID, artifactHash, jobType string, parentJobID *string, processesJSON, sourceURL string, sourceParams map[string]string, preferredNodeID string) (string, error) {
	if artifactHash == "" {
		return "", errors.New("artifact_hash is required for a transactional processing-job insert")
	}
	jobID := uuid.New().String()
	parentID, pJSON, srcURL, srcParams, preferredNode, err := processingJobInsertArgs(parentJobID, processesJSON, sourceURL, sourceParams, preferredNodeID)
	if err != nil {
		return "", err
	}

	queries := foghorndb.New(tx)
	if lockErr := queries.LockProcessingJobIdentity(ctx, foghorndb.LockProcessingJobIdentityParams{Hashtext: artifactHash, Hashtext_2: jobType}); lockErr != nil {
		return "", lockErr
	}

	existingJobID, scanErr := queries.FindActiveProcessingJob(ctx, foghorndb.FindActiveProcessingJobParams{ArtifactHash: sql.NullString{String: artifactHash, Valid: true}, JobType: jobType})
	switch {
	case scanErr == nil:
		return existingJobID, nil
	case !errors.Is(scanErr, sql.ErrNoRows):
		return "", scanErr
	}

	if insErr := queries.InsertProcessingJob(ctx, processingJobParams(jobID, tenantID, artifactHash, jobType, parentID, pJSON, srcURL, srcParams, preferredNode)); insErr != nil {
		return "", insErr
	}

	if updErr := queries.MarkQueuedClipArtifact(ctx, foghorndb.MarkQueuedClipArtifactParams{ArtifactHash: artifactHash, TenantID: tenantID}); updErr != nil {
		return "", updErr
	}
	return jobID, nil
}

func processingJobParams(jobID, tenantID, artifactHash, jobType string, parentID, processesJSON, sourceURL, sourceParams, preferredNodeID *string) foghorndb.InsertProcessingJobParams {
	params := foghorndb.InsertProcessingJobParams{
		JobID: jobID, TenantID: tenantID, ArtifactHash: sql.NullString{String: artifactHash, Valid: artifactHash != ""}, JobType: jobType,
	}
	if parentID != nil {
		params.ParentJobID = sql.NullString{String: *parentID, Valid: true}
	}
	if processesJSON != nil {
		params.ProcessesJson = sql.NullString{String: *processesJSON, Valid: true}
	}
	if sourceURL != nil {
		params.SourceUrl = sql.NullString{String: *sourceURL, Valid: true}
	}
	if sourceParams != nil {
		params.SourceParams = sql.NullString{String: *sourceParams, Valid: true}
	}
	if preferredNodeID != nil {
		params.PreferredNodeID = sql.NullString{String: *preferredNodeID, Valid: true}
	}
	return params
}

// processingJobInsertArgs normalizes the optional processing-job columns into the nullable pointers
// the INSERT binds. Shared by the tx and non-tx enqueue paths so both marshal source_params and
// trim/nil the optional fields identically.
func processingJobInsertArgs(parentJobID *string, processesJSON, sourceURL string, sourceParams map[string]string, preferredNodeID string) (parentID, pJSON, srcURL, srcParams, preferredNode *string, err error) {
	if parentJobID != nil && *parentJobID != "" {
		parentID = parentJobID
	}
	if processesJSON != "" {
		pJSON = &processesJSON
	}
	if strings.TrimSpace(sourceURL) != "" {
		trimmed := strings.TrimSpace(sourceURL)
		srcURL = &trimmed
	}
	if len(sourceParams) > 0 {
		b, mErr := json.Marshal(sourceParams)
		if mErr != nil {
			return nil, nil, nil, nil, nil, mErr
		}
		s := string(b)
		srcParams = &s
	}
	if strings.TrimSpace(preferredNodeID) != "" {
		trimmed := strings.TrimSpace(preferredNodeID)
		preferredNode = &trimmed
	}
	return parentID, pJSON, srcURL, srcParams, preferredNode, nil
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
