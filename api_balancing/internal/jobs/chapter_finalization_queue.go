package jobs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Polls foghorn.dvr_chapters for rows in state='closed' (or stuck in
// 'finalizing' past the dispatch timeout) and dispatches a
// ProcessingJobRequest{job_type="dvr_chapter_finalize"} to the
// recording-origin Helmsman. Helmsman remuxes the chapter's TS range
// to a canonical .mkv VOD artifact via processing+<hash>; MistProc on
// that boot produces the spritesheet/poster/Chandler thumbnail tracks.
// A later vod+<hash> boot generates only the .dtsh sidecar. Freeze +
// dtsh_synced carry the chapter to state='frozen'; chapter_reclaim_sweep
// then deletes source segments and advances to 'reclaimed'. Per-DVR
// mutex serializes finalization to bound concurrent disk usage.
//
// Dispatch timeout: chapter intervals can be hours; we picked
// max(2*chapter_duration, 30 minutes) capped at 24h, which goes in the
// ProcessingJobRequest.deadline_unix_ms. The same value drives the
// stuck-finalizing recovery probe so a Helmsman that died mid-job
// doesn't park the chapter forever.

const (
	chapterFinalizationDefaultTick      = 30 * time.Second
	chapterFinalizationDispatchBatchMax = 20
	chapterFinalizationMaxAttempts      = 5
	chapterFinalizationMinTimeout       = 30 * time.Minute
	chapterFinalizationMaxTimeout       = 24 * time.Hour
	// chapterFinalizeAbandonNodeGrace bounds the wait for a dead
	// recording origin to come back with pending-local-only
	// segments. Past this, the pending segments are presumed lost
	// and the chapter terminal-fails as failed_source_missing —
	// without this bound an origin loss would wedge the chapter
	// forever (queue keeps returning nil because the queue can't
	// dispatch without local-segment access from the origin).
	chapterFinalizeAbandonNodeGrace = 4 * time.Hour
	chapterRecoveryURLTTL           = 6 * time.Hour
)

type ChapterFinalizationQueueConfig struct {
	DB              *sql.DB
	Logger          logging.Logger
	Interval        time.Duration
	GatewayResolver GatewayResolver
	ConfigCacher    ProcessConfigCacher
}

type ChapterFinalizationQueue struct {
	db              *sql.DB
	logger          logging.Logger
	interval        time.Duration
	stopCh          chan struct{}
	wakeCh          chan struct{}
	wg              sync.WaitGroup
	gatewayResolver GatewayResolver
	configCacher    ProcessConfigCacher
}

var (
	chapterFinalizeWakeMu    sync.Mutex
	chapterFinalizeWakeChans = map[chan struct{}]struct{}{}
)

// presignArtifactGET is a seam over control.GeneratePresignedGETForArtifact so
// buildSegmentRefs' status routing (and the transient-failure-is-retryable
// invariant) can be tested without a live S3 signer.
var presignArtifactGET = control.GeneratePresignedGETForArtifact

func NewChapterFinalizationQueue(cfg ChapterFinalizationQueueConfig) *ChapterFinalizationQueue {
	interval := cfg.Interval
	if interval == 0 {
		interval = chapterFinalizationDefaultTick
	}
	return &ChapterFinalizationQueue{
		db:              cfg.DB,
		logger:          cfg.Logger,
		interval:        interval,
		stopCh:          make(chan struct{}),
		wakeCh:          make(chan struct{}, 1),
		gatewayResolver: cfg.GatewayResolver,
		configCacher:    cfg.ConfigCacher,
	}
}

func (q *ChapterFinalizationQueue) Start() {
	registerChapterFinalizationWake(q.wakeCh)
	q.wg.Add(1)
	go q.run()
	q.logger.WithField("interval_seconds", int(q.interval.Seconds())).Info("Chapter finalization queue started")
}

func (q *ChapterFinalizationQueue) Stop() {
	unregisterChapterFinalizationWake(q.wakeCh)
	close(q.stopCh)
	q.wg.Wait()
	q.logger.Info("Chapter finalization queue stopped")
}

func (q *ChapterFinalizationQueue) run() {
	defer q.wg.Done()
	ticker := time.NewTicker(q.interval)
	defer ticker.Stop()

	q.tick()

	for {
		select {
		case <-ticker.C:
			q.tick()
		case <-q.wakeCh:
			q.tick()
		case <-q.stopCh:
			return
		}
	}
}

func registerChapterFinalizationWake(ch chan struct{}) {
	chapterFinalizeWakeMu.Lock()
	defer chapterFinalizeWakeMu.Unlock()
	chapterFinalizeWakeChans[ch] = struct{}{}
}

func unregisterChapterFinalizationWake(ch chan struct{}) {
	chapterFinalizeWakeMu.Lock()
	defer chapterFinalizeWakeMu.Unlock()
	delete(chapterFinalizeWakeChans, ch)
}

// NotifyChapterFinalizationQueued wakes local chapter finalizers after a
// chapter row enters the closed state. Polling remains recovery for HA peers.
func NotifyChapterFinalizationQueued() {
	chapterFinalizeWakeMu.Lock()
	defer chapterFinalizeWakeMu.Unlock()
	for ch := range chapterFinalizeWakeChans {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (q *ChapterFinalizationQueue) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var chapters []control.DVRChapterRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var listErr error
		chapters, listErr = control.ListChaptersNeedingFinalization(ctx, chapterFinalizationDispatchBatchMax, chapterFinalizationMaxTimeout)
		return listErr
	})
	if err != nil {
		q.logger.WithError(err).Warn("Chapter finalization queue: list failed")
		return
	}
	for _, c := range chapters {
		dispatchCtx, dispatchCancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := control.WithDVRChapterMutationLock(dispatchCtx, c.ArtifactHash, func() error {
			return q.dispatchChapter(dispatchCtx, c)
		}); err != nil {
			q.logger.WithError(err).WithField("chapter_id", c.ChapterID).Warn("Chapter finalization dispatch failed")
		}
		dispatchCancel()
	}
}

// dispatchChapter runs one finalize attempt for a single chapter row.
// Allocates the playback artifact (idempotent on retry via the
// origin_type/origin_id unique partial index), assembles the source
// segment list, picks the recording-origin node, and sends the
// ProcessingJobRequest. On terminal source-missing, marks the chapter
// failed; transient failures bounce the chapter back to 'closed' so
// the next tick retries.
func (q *ChapterFinalizationQueue) dispatchChapter(ctx context.Context, c control.DVRChapterRow) error {
	if c.FinalizeAttempts >= chapterFinalizationMaxAttempts {
		return control.MarkChapterFailed(ctx, c.ChapterID, control.ChapterStateFailedPermanent,
			fmt.Sprintf("max attempts (%d) exceeded", chapterFinalizationMaxAttempts))
	}

	parent, err := q.readParentDVR(ctx, c.ArtifactHash)
	if err != nil {
		return fmt.Errorf("read parent DVR: %w", err)
	}

	segments, err := control.ListDVRSegmentsOwnedByChapter(ctx, c.ArtifactHash, c.StartMs, c.EndMs)
	if err != nil {
		return fmt.Errorf("list source segments: %w", err)
	}
	if len(segments) == 0 {
		return control.MarkChapterFailed(ctx, c.ChapterID,
			control.ChapterStateFailedSourceMissing,
			"chapter range has no segments")
	}
	refs, missing, refErr := q.buildSegmentRefs(parent.tenantID, parent.streamInternalName, c.ArtifactHash, segments)
	if refErr != nil {
		return fmt.Errorf("build segment refs: %w", refErr)
	}
	if missing > 0 {
		return control.MarkChapterFailed(ctx, c.ChapterID,
			control.ChapterStateFailedSourceMissing,
			fmt.Sprintf("%d source segments missing from both local and recovery freeze", missing))
	}

	// Pick the dispatch target. The recording origin is preferred —
	// local TS files avoid the S3 recovery fetch — but the chapter
	// must still finalize if the origin is gone OR offline. A stale
	// artifact_nodes row pointing at a dead node would otherwise let
	// dispatch repeatedly hit a node that can't process the job. We
	// check node liveness + processing capability before treating
	// parent.recordingNode as authoritative; fall through to the
	// alternate-node path when the origin is unavailable.
	targetNode := parent.recordingNode
	if targetNode != "" && !nodeAliveAndProcessingCapable(targetNode) {
		q.logger.WithFields(logging.Fields{
			"chapter_id":     c.ChapterID,
			"dvr_hash":       c.ArtifactHash,
			"recording_node": targetNode,
		}).Info("Chapter finalize: recording origin offline/non-processing; falling back to alternate node via S3 recovery")
		targetNode = ""
	}
	if targetNode == "" {
		if pendingLocalOnly(refs) {
			// Some segments are pending-local-only on the (gone)
			// recording node. Defer — Mist on another node can't read
			// them without the S3 freeze, and we don't promote
			// pending → lost_local from here. But the wait is bounded:
			// once the chapter has been frozen-eligible (boundary
			// past) for the abandoned-node grace period, the pending
			// segments are presumed lost (the dead origin will never
			// finish their uploads) and we terminal-fail the chapter
			// so reclaim/playback callers don't wedge forever.
			eligibleSinceMs := c.EndMs
			if eligibleSinceMs <= 0 || c.CreatedAt.UnixMilli() > eligibleSinceMs {
				eligibleSinceMs = c.CreatedAt.UnixMilli()
			}
			if time.Since(time.UnixMilli(eligibleSinceMs)) >= chapterFinalizeAbandonNodeGrace {
				q.logger.WithFields(logging.Fields{
					"chapter_id":    c.ChapterID,
					"dvr_hash":      c.ArtifactHash,
					"grace_minutes": int(chapterFinalizeAbandonNodeGrace.Minutes()),
				}).Warn("Chapter finalize: origin gone past grace with pending-local segments; classifying chapter as failed_source_missing")
				return control.MarkChapterFailed(ctx, c.ChapterID,
					control.ChapterStateFailedSourceMissing,
					"recording origin unavailable past grace with pending source segments")
			}
			q.logger.WithFields(logging.Fields{
				"chapter_id": c.ChapterID,
				"dvr_hash":   c.ArtifactHash,
			}).Debug("Chapter finalize: recording origin gone and some segments are pending-local-only; awaiting next tick")
			return nil
		}
		altNode, reason := routeProcessingJob(nil)
		if altNode == "" {
			q.logger.WithFields(logging.Fields{
				"chapter_id": c.ChapterID,
				"dvr_hash":   c.ArtifactHash,
				"reason":     reason,
			}).Debug("Chapter finalize: recording origin gone and no alternate processing node available; awaiting next tick")
			return nil
		}
		targetNode = altNode
		q.logger.WithFields(logging.Fields{
			"chapter_id": c.ChapterID,
			"dvr_hash":   c.ArtifactHash,
			"alt_node":   altNode,
		}).Info("Chapter finalize: recording origin gone; dispatching to alternate processing node via S3 recovery")
	}

	playbackHash := chapterPlaybackArtifactHash(c.ChapterID)
	if allocErr := q.ensurePlaybackArtifactRow(ctx, playbackHash, c, parent); allocErr != nil {
		return fmt.Errorf("allocate playback artifact: %w", allocErr)
	}

	// Mint (or fetch existing) Commodore-owned public playback_id and
	// cache it on the chapter row. The mint is idempotent on chapter_id
	// so retries reuse the same playback_id. The public playback_id is
	// the contract for chapter playback — if minting fails we must
	// leave the chapter in 'closed' so the next tick retries; we do
	// NOT advance to 'finalizing' without a publicly addressable ID
	// since there is no artifact-hash fallback anymore.
	filename := fmt.Sprintf("dvr-chapter-%s-%d-%d.mkv", c.ArtifactHash, c.StartMs, c.EndMs)
	playbackID, mintErr := q.mintChapterPlaybackID(ctx, c.ChapterID, parent, playbackHash, filename)
	if mintErr != nil {
		q.logger.WithError(mintErr).WithFields(logging.Fields{
			"chapter_id":    c.ChapterID,
			"artifact_hash": playbackHash,
		}).Warn("Chapter finalization queue: mint playback_id failed; chapter stays closed for retry")
		return fmt.Errorf("mint chapter playback_id: %w", mintErr)
	}
	if cacheErr := control.SetChapterPlaybackID(ctx, c.ChapterID, playbackID); cacheErr != nil {
		// Cache-on-chapter-row failure is recoverable on the next read
		// (resolver falls back to Commodore.ResolveChapterPlaybackID
		// directly), but the chapter shouldn't dispatch finalize
		// against a known-stale cache state — log and retry.
		q.logger.WithError(cacheErr).WithField("chapter_id", c.ChapterID).Warn("Chapter finalization queue: cache playback_id on dvr_chapters failed; chapter stays closed for retry")
		return fmt.Errorf("cache playback_id on chapter row: %w", cacheErr)
	}

	// Resolve the DVR-finalization processing pipeline BEFORE marking the
	// chapter finalizing. Commodore is the processing-policy authority; this
	// queue stores/applies the resolved snapshot without deriving a local
	// subset. If Commodore is unreachable or returns an error we must leave
	// the chapter in 'closed' so the next queue tick retries.
	if control.CommodoreClient == nil {
		return fmt.Errorf("commodore client not configured; cannot resolve tenant processes_json")
	}
	processesJSON := ""
	{
		resp, perr := control.CommodoreClient.GetTenantProcessesJSONForLifecycle(ctx, parent.tenantID, "dvr_finalize", parent.originClusterID, parent.streamID)
		if perr != nil {
			q.logger.WithError(perr).WithFields(logging.Fields{
				"chapter_id":    c.ChapterID,
				"artifact_hash": playbackHash,
			}).Warn("Chapter finalization queue: tenant processes_json lookup failed; chapter stays closed for retry")
			return fmt.Errorf("resolve tenant processes_json: %w", perr)
		}
		processesJSON = resp.GetProcessesJson()
	}
	// Fill the Livepeer broadcaster list so Helmsman sees concrete gateway
	// addresses. Commodore returns the config without broadcasters by design
	// (the resolver runs in the local cluster's context).
	if q.gatewayResolver != nil && processesJSON != "" {
		processesJSON = q.gatewayResolver.ApplyLivepeerBroadcasters(processesJSON, nil)
		processesJSON = q.gatewayResolver.ApplyLivepeerWorkload(processesJSON, mist.WorkloadVOD)
	}
	// Cache the resolved config for the STREAM_PROCESS trigger that
	// fires when Mist boots the processing+<hash> stream. Mirrors the
	// processing dispatcher.
	if q.configCacher != nil && processesJSON != "" {
		q.configCacher.CacheProcessConfig(playbackHash, processesJSON)
	}

	ok, err := control.MarkChapterFinalizing(ctx, c.ChapterID, playbackHash, chapterFinalizationMaxTimeout)
	if err != nil {
		return err
	}
	if !ok {
		// Either advanced past closed already, or another worker
		// re-claimed this stale finalizing row within the same tick.
		return nil
	}
	startedAt := time.Now().Unix()
	artifactoutbox.EnqueueVodLifecycleLogged(&ipcpb.VodLifecycleData{
		Status:    ipcpb.VodLifecycleData_STATUS_PROCESSING,
		VodHash:   playbackHash,
		TenantId:  &parent.tenantID,
		StartedAt: &startedAt,
	})

	deadline := time.Now().Add(chapterFinalizationDeadline(c)).UnixMilli()
	chapterInt := chapterInternalName(playbackHash)
	req := &ipcpb.ProcessingJobRequest{
		JobId:                    "chapter-finalize-" + c.ChapterID,
		TenantId:                 parent.tenantID,
		ArtifactHash:             playbackHash,
		JobType:                  "dvr_chapter_finalize",
		InternalName:             chapterInt,
		OutputRuntimeName:        "vod+" + chapterInt,
		ProcessesJson:            processesJSON,
		DeadlineUnixMs:           deadline,
		SourceChapterId:          c.ChapterID,
		SourceDvrHash:            c.ArtifactHash,
		SourceStreamInternalName: parent.streamInternalName,
		ChapterStartMs:           c.StartMs,
		ChapterEndMs:             c.EndMs,
		SourceSegments:           refs,
	}
	if err := control.SendProcessingJob(targetNode, req); err != nil {
		retryErr := control.RetryChapterFinalize(ctx, c.ChapterID, fmt.Sprintf("dispatch failed: %v", err))
		if retryErr != nil {
			q.logger.WithError(retryErr).WithField("chapter_id", c.ChapterID).Warn("Chapter finalization queue: roll-back to closed failed")
		}
		return fmt.Errorf("dispatch processing job: %w", err)
	}
	q.logger.WithFields(logging.Fields{
		"chapter_id":    c.ChapterID,
		"dvr_hash":      c.ArtifactHash,
		"artifact_hash": playbackHash,
		"node_id":       targetNode,
		"segments":      len(refs),
		"deadline_unix": deadline,
	}).Info("Chapter finalization dispatched")
	return nil
}

type parentDVR struct {
	tenantID           string
	userID             string
	streamID           string
	streamInternalName string
	originClusterID    string
	storageClusterID   string
	recordingNode      string
}

func (q *ChapterFinalizationQueue) readParentDVR(ctx context.Context, dvrHash string) (parentDVR, error) {
	var p parentDVR
	if err := q.db.QueryRowContext(ctx, `
		SELECT a.tenant_id::text,
		       COALESCE(a.user_id::text, ''),
		       COALESCE(a.stream_id::text, ''),
		       COALESCE(a.stream_internal_name, ''),
		       COALESCE(a.origin_cluster_id, ''),
		       COALESCE(a.storage_cluster_id, ''),
		       COALESCE(
		           (SELECT node_id
		              FROM foghorn.artifact_nodes
		             WHERE artifact_hash = a.artifact_hash
		               AND is_orphaned = false
		             ORDER BY last_seen_at DESC NULLS LAST
		             LIMIT 1), '')
		  FROM foghorn.artifacts a
		 WHERE a.artifact_hash = $1
		   AND a.artifact_type = 'dvr'
	`, dvrHash).Scan(&p.tenantID, &p.userID, &p.streamID, &p.streamInternalName, &p.originClusterID, &p.storageClusterID, &p.recordingNode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, fmt.Errorf("parent DVR row missing")
		}
		return p, err
	}
	return p, nil
}

func (q *ChapterFinalizationQueue) buildSegmentRefs(_ /*tenantID*/, _ /*streamInternalName*/, _ /*dvrHash*/ string, rows []control.DVRSegmentRow) ([]*ipcpb.DVRChapterSegmentRef, int, error) {
	refs := make([]*ipcpb.DVRChapterSegmentRef, 0, len(rows))
	missing := 0
	for _, r := range rows {
		// LocalPath is intentionally empty: Helmsman owns the on-disk
		// DVR layout (storage/dvr/<streamID>/<dvrHash>/segments/<name>)
		// and resolves the segment path from its DVRManager job state
		// for the matching dvr_hash + segment_name. Foghorn doesn't
		// know the streamID-rooted layout, so it never tries to
		// pre-compute a path the sidecar would just have to undo.
		ref := &ipcpb.DVRChapterSegmentRef{
			SegmentName:  r.SegmentName,
			Sequence:     r.Sequence,
			DurationMs:   r.DurationMs,
			MediaStartMs: r.MediaStartMs,
			MediaEndMs:   r.MediaEndMs,
		}
		if r.SizeBytes.Valid && r.SizeBytes.Int64 > 0 {
			ref.SizeBytes = r.SizeBytes.Int64
		}
		switch r.Status {
		case "uploaded", "deleted_local":
			// The object is on S3 and Helmsman MAY need the recovery URL
			// when the local TS file is gone. A presign failure here
			// (signer/config/network blip) is transient and must not
			// be reframed as "missing locally and no recovery URL" by
			// the downstream finalize handler — that would mark the
			// chapter failed_source_missing on a transient. Fail the
			// whole ref build so the dispatcher rolls the chapter back
			// to 'closed' for retry.
			url, err := presignArtifactGET(context.Background(), r.S3Key)
			if err != nil {
				return nil, 0, fmt.Errorf("presign recovery URL for segment %s: %w", r.SegmentName, err)
			}
			ref.PresignedRecoveryUrl = url
		case "pending":
			// Local-only; Helmsman resolves from its DVR job state.
		case "lost_local":
			// Object is gone locally; recovery depends entirely on S3.
			// Presign failure for a lost_local row that *should* still
			// be on S3 (was_uploaded=true) is transient — fail-retryable.
			// A truly absent S3 object surfaces from the GET later.
			if r.S3Key == "" {
				missing++
				break
			}
			url, err := presignArtifactGET(context.Background(), r.S3Key)
			if err != nil {
				return nil, 0, fmt.Errorf("presign recovery URL for lost segment %s: %w", r.SegmentName, err)
			}
			ref.PresignedRecoveryUrl = url
		case "reclaimed":
			missing++
		}
		refs = append(refs, ref)
	}
	return refs, missing, nil
}

// mintChapterPlaybackID asks Commodore for the chapter's public
// playback_id. The Commodore-side INSERT is idempotent on chapter_id
// so retries return the same playback_id. Returns "" + error when the
// client is unavailable or the RPC fails; callers treat that as a soft
// miss and retry on the next finalization tick.
func (q *ChapterFinalizationQueue) mintChapterPlaybackID(ctx context.Context, chapterID string, parent parentDVR, artifactHash, filename string) (string, error) {
	if control.CommodoreClient == nil {
		return "", fmt.Errorf("commodore client not configured")
	}
	resp, err := control.CommodoreClient.MintChapterPlaybackID(ctx, chapterID, parent.tenantID, artifactHash, parent.userID, filename, parent.originClusterID, parent.storageClusterID, parent.streamID)
	if err != nil {
		return "", err
	}
	if resp.GetPlaybackId() == "" {
		return "", fmt.Errorf("commodore returned empty playback_id")
	}
	return resp.GetPlaybackId(), nil
}

func (q *ChapterFinalizationQueue) ensurePlaybackArtifactRow(ctx context.Context, hash string, c control.DVRChapterRow, parent parentDVR) error {
	internalName := chapterInternalName(hash)
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO foghorn.artifacts (
			artifact_hash, artifact_type, tenant_id, user_id,
			internal_name, stream_internal_name,
			origin_type, origin_id, library_visible,
			status, storage_location, sync_status, format,
			origin_cluster_id, created_at, updated_at
		) VALUES (
			$1, 'vod', $2::uuid, NULLIF($3, '')::uuid,
			$4, $5,
			'dvr_chapter', $6, false,
			'finalizing', 'pending', 'pending', 'mkv',
			NULLIF($7, ''), NOW(), NOW()
		)
		ON CONFLICT (artifact_hash) DO NOTHING
	`, hash, parent.tenantID, parent.userID, internalName, parent.streamInternalName, c.ChapterID, parent.originClusterID)
	return err
}

func chapterFinalizationDeadline(c control.DVRChapterRow) time.Duration {
	duration := time.Duration(c.EndMs-c.StartMs) * time.Millisecond
	d := 2 * duration
	if d < chapterFinalizationMinTimeout {
		d = chapterFinalizationMinTimeout
	}
	if d > chapterFinalizationMaxTimeout {
		d = chapterFinalizationMaxTimeout
	}
	return d
}

// chapterPlaybackArtifactHash is the deterministic playback artifact
// hash for a chapter. The unique partial index on
// foghorn.artifacts(origin_id) WHERE origin_type='dvr_chapter' enforces
// idempotency at the DB layer; this helper just keeps retries pointing
// at the same row by construction.
func chapterPlaybackArtifactHash(chapterID string) string {
	h := sha256.Sum256([]byte("dvr_chapter:" + chapterID))
	return hex.EncodeToString(h[:])[:32]
}

// chapterInternalName is the bare routing identifier stored on
// foghorn.artifacts.internal_name. Repo convention: DB rows hold the
// bare name; the vod+ Mist prefix is appended only where stream names
// are constructed (Helmsman push, STREAM_SOURCE resolution).
func chapterInternalName(playbackHash string) string {
	return playbackHash
}

// nodeAliveAndProcessingCapable returns true when the StreamStateManager
// has the node in its alive set AND the node advertises processing
// capability with available transcode slots. Mirrors routeProcessingJob's
// filtering so the chapter queue treats the recording origin as
// authoritative only when it could actually run the job.
func nodeAliveAndProcessingCapable(nodeID string) bool {
	if nodeID == "" {
		return false
	}
	sm := state.DefaultManager()
	for _, id := range sm.AliveNodeIDs(60 * time.Second) {
		if id != nodeID {
			continue
		}
		n := sm.GetNodeState(id)
		if n == nil || !n.IsHealthy || !n.CapProcessing {
			return false
		}
		if !n.CanRunClass(mist.ProcessingClassVideoTranscode) {
			return false
		}
		return true
	}
	return false
}

// pendingLocalOnly returns true when at least one segment ref has no
// recovery URL and is not a Mist-side gap marker. When the recording
// origin is gone, such a ref cannot be resolved on an alternate node;
// the dispatch must defer until the origin comes back or the segment's
// S3 freeze completes. uploaded/deleted_local rows always carry a
// presigned recovery URL after buildSegmentRefs runs; pending rows
// (still being uploaded) don't.
func pendingLocalOnly(refs []*ipcpb.DVRChapterSegmentRef) bool {
	for _, r := range refs {
		if r.GetPresignedRecoveryUrl() == "" {
			return true
		}
	}
	return false
}
