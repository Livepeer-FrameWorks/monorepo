package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Chapter-finalize jobs don't share the processing_jobs ledger (which
// keys on UUID job_ids) — they live in foghorn.dvr_chapters and have
// string job_ids of the form "chapter-finalize-v2-<attempt>-<chapter_id>". Routing
// happens at the top of processProcessingJobResult; this file owns the
// chapter-side state advance, artifact registration, and downstream
// DTSH dispatch.

const chapterFinalizeJobIDPrefix = "chapter-finalize-"
const chapterFinalizeAttemptPrefix = "v2-"

func chapterFinalizeIdentityFromJobID(jobID string) (chapterID string, attempt int32, ok bool) {
	if !strings.HasPrefix(jobID, chapterFinalizeJobIDPrefix) {
		return "", 0, false
	}
	rest := strings.TrimPrefix(jobID, chapterFinalizeJobIDPrefix)
	if !strings.HasPrefix(rest, chapterFinalizeAttemptPrefix) {
		// Recognize legacy attempt-less IDs as chapter jobs, but give them
		// attempt zero so every attempt-fenced mutation rejects them. The
		// explicit marker avoids misreading a numeric chapter ID as an attempt.
		return rest, 0, rest != ""
	}
	encoded := rest
	rest = strings.TrimPrefix(rest, chapterFinalizeAttemptPrefix)
	separator := strings.IndexByte(rest, '-')
	if separator <= 0 || separator == len(rest)-1 {
		return encoded, 0, true
	}
	parsed, err := strconv.ParseInt(rest[:separator], 10, 32)
	if err != nil || parsed <= 0 {
		return encoded, 0, true
	}
	return rest[separator+1:], int32(parsed), true
}

// handleChapterFinalizeResult is the dedicated completion handler for
// chapter finalize jobs. Same shape as the VOD processing branch
// (register artifact in warm cache + state, update foghorn.artifacts
// size/format/sync_status, trigger DTSH) but transitions the chapter
// row + skips the processing_jobs UPDATE that would fail on the
// non-UUID job_id.
func handleChapterFinalizeResult(
	ctx context.Context,
	chapterID, jobStatus string,
	expectedAttempt int32,
	result *ipcpb.ProcessingJobResult,
	nodeID string,
	logger logging.Logger,
) {
	if db == nil {
		return
	}
	fields := logging.Fields{
		"job_id":     result.GetJobId(),
		"status":     jobStatus,
		"node_id":    nodeID,
		"chapter_id": chapterID,
	}

	// Attempt and reporting-node authorization are enforced inside each guarded
	// transition. finalize_attempts distinguishes a reclaimed assignment from a
	// delayed result even when both attempts use the same recording-origin node;
	// finalize_node_id additionally rejects another connection. Chapter jobs do
	// not have a processing_jobs row, so both values live on dvr_chapters.
	if jobStatus == "failed" {
		if terminal, reason := chapterTerminalFailure(result.GetOutputs(), result.GetError()); terminal {
			// MarkChapterFailed transitions the chapter, fails the child artifact, AND enqueues the
			// failed lifecycle in one transaction — no separate (loss-prone) emit needed.
			changed, err := MarkChapterFailed(ctx, chapterID, ChapterStateFailedSourceMissing, reason, nodeID, expectedAttempt)
			if err != nil {
				logger.WithError(err).WithFields(fields).Warn("Chapter finalize: terminal-fail mark failed")
			} else if !changed {
				logger.WithFields(fields).Info("Chapter finalize: terminal-fail result rejected by attempt/node fence")
			}
			return
		}
		changed, err := RetryChapterFinalize(ctx, chapterID, result.GetError(), nodeID, expectedAttempt)
		if err != nil {
			logger.WithError(err).WithFields(fields).Warn("Chapter finalize: retry rollback failed")
		} else if !changed {
			logger.WithFields(fields).Info("Chapter finalize: retry result rejected by attempt/node fence")
		}
		return
	}
	if jobStatus != "completed" {
		logger.WithFields(fields).Warn("Chapter finalize: unhandled result status")
		return
	}

	outputPath := result.GetOutputPath()
	playbackHash := chapterPlaybackArtifactHashFromOutputs(result.GetOutputs(), outputPath)
	if playbackHash == "" || outputPath == "" {
		// A completed chapter result MUST carry BOTH a produced output path and a resolvable
		// playback hash. A malformed completion is neither silently swallowed (which would strand
		// the chapter 'finalizing' forever) nor finalized without an origin copy — bounce it to
		// 'closed' so the queue re-dispatches; finalize_attempts bounds the retries → terminal-fail.
		logger.WithFields(fields).WithFields(logging.Fields{
			"has_hash":   playbackHash != "",
			"has_output": outputPath != "",
		}).Warn("Chapter finalize: malformed completion (missing hash or output path); bouncing to closed for retry")
		changed, err := RetryChapterFinalize(ctx, chapterID, "malformed completion: missing hash or output path", nodeID, expectedAttempt)
		if err != nil {
			logger.WithError(err).WithFields(fields).Warn("Chapter finalize: bounce finalizing→closed failed")
		} else if !changed {
			logger.WithFields(fields).Info("Chapter finalize: malformed completion rejected by attempt/node fence")
		}
		return
	}
	sizeBytes := result.GetOutputSizeBytes()
	segCount := int32(0)
	if v, ok := result.GetOutputs()["chapter_segment_count"]; ok {
		if parsed, err := strconv.ParseInt(v, 10, 32); err == nil {
			segCount = int32(parsed)
		}
	}
	hasGaps := result.GetOutputs()["chapter_has_gaps"] == "true"
	// Helmsman's chapter finalize records the MKV's actual media span
	// (first owned segment's media_start_ms .. last segment's
	// media_end_ms). Stored on the chapter row so the player can anchor
	// video.currentTime to wall-clock without drift even when chapter
	// boundaries don't align to segment boundaries. Missing values fall
	// through as zero — MarkChapterFinalized leaves the columns NULL.
	var mediaStartMs, mediaEndMs int64
	if v, ok := result.GetOutputs()["chapter_media_start_ms"]; ok {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			mediaStartMs = parsed
		}
	}
	if v, ok := result.GetOutputs()["chapter_media_end_ms"]; ok {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			mediaEndMs = parsed
		}
	}

	// The chapter's finalized MKV is an ordinary vod+ artifact; its accepted A/V track set
	// rides on the completion-validated ProcessingJobResult. Persist it by the resolved playback
	// artifact_hash alongside readiness, gated on tracks_present (present replaces, empty clears,
	// absent leaves untouched); the reconciler projects it onto the catalog.
	tracksPresent := result.GetTracksPresent()
	tracksJSON, tErr := marshalRecordingTracks(result.GetTracks())
	if tErr != nil {
		logger.WithError(tErr).WithFields(fields).Warn("Chapter finalize: failed to marshal tracks; leaving existing summary")
		tracksPresent = false
		tracksJSON = "[]"
	}

	// Chapter media duration: prefer the measured muxed-output duration from the validated
	// RecordingEnd (result.media_duration_ms) — it reflects actual playback length. Fall back to
	// the MKV segment-timeline span (end - start) only when the measured value is absent, since
	// the span can diverge from playback duration under gaps/discontinuities. 0 leaves the stored
	// value untouched.
	chapterDurationMs := result.GetMediaDurationMs()
	if chapterDurationMs <= 0 && mediaEndMs > mediaStartMs {
		chapterDurationMs = mediaEndMs - mediaStartMs
	}

	// The whole chapter finalization is ONE transaction: lock the chapter + its allocated
	// playback artifact, persist readiness/duration/tracks + vod_metadata + origin (with its
	// node-copy event) + the finalizing→finalized transition + the completion lifecycle, and
	// commit together. A chapter is never left half-finalized (artifact ready but chapter row
	// still 'finalizing', or vice versa). A duplicate/late completion (chapter no longer
	// 'finalizing') is an ignored no-op; any transient failure rolls the whole tx back and
	// bounces the chapter finalizing→closed so the queue re-dispatches promptly.
	resolvedHash, txErr := finalizeChapterArtifactTx(ctx, chapterID, playbackHash, outputPath, nodeID, expectedAttempt,
		sizeBytes, segCount, hasGaps, mediaStartMs, mediaEndMs, chapterDurationMs, tracksPresent, tracksJSON,
		result.GetOutputs(), logger, fields)
	if txErr != nil {
		if errors.Is(txErr, errChapterNotInFinalizing) {
			logger.WithFields(fields).Info("Chapter finalize: chapter not in finalizing; ignoring completion")
			return
		}
		if errors.Is(txErr, errChapterFinalizeNodeMismatch) {
			logger.WithFields(fields).Warn("Chapter finalize: reporting node is not the assigned finalize node; ignoring completion")
			return
		}
		logger.WithError(txErr).WithFields(fields).Warn("Chapter finalize: atomic finalize failed; bouncing chapter to closed for retry")
		changed, rbErr := RetryChapterFinalize(ctx, chapterID, "finalize persist failed: "+txErr.Error(), nodeID, expectedAttempt)
		if rbErr != nil {
			logger.WithError(rbErr).WithFields(fields).Warn("Chapter finalize: bounce finalizing→closed failed")
		} else if !changed {
			logger.WithFields(fields).Info("Chapter finalize: failed-persist retry rejected by attempt/node fence")
		}
		return
	}
	logger.WithFields(fields).WithFields(logging.Fields{
		"artifact_hash": resolvedHash,
		"segments":      segCount,
		"has_gaps":      hasGaps,
	}).Info("Chapter finalized (state=finalized)")

	// After commit: in-memory placement so the chapter VOD is immediately servable on the node
	// that produced it (origin, is_complete=true) — best-effort, not part of durability.
	if outputPath != "" {
		state.DefaultManager().AddNodeArtifact(nodeID, &ipcpb.StoredArtifact{
			ClipHash:   resolvedHash,
			FilePath:   outputPath,
			SizeBytes:  uint64(sizeBytes),
			CreatedAt:  time.Now().Unix(),
			Format:     "mkv",
			Role:       ipcpb.StoredArtifact_ROLE_ORIGIN,
			IsComplete: true,
		})
		NotifyArtifactMapUpdated(nodeID)
	}

	// DTSH generation runs on the Helmsman side immediately after
	// PUSH_END (api_sidecar/internal/handlers/processing_chapter.go).
	// Spritesheet / Chandler thumbnail tracks come from Commodore's
	// dvr_finalize process snapshot (see chapter_finalization_queue.go), so
	// MistProc fires the configured tracks during the processing+<hash> boot.
	// No further server-side fan-out is needed here.
}

// errChapterNotInFinalizing signals that the locked chapter was not in 'finalizing' (a
// duplicate/late completion or a concurrent worker already advanced it). The caller treats
// this as an ignored no-op — NOT a transient failure — so it does not bounce the chapter.
var errChapterNotInFinalizing = errors.New("chapter not in finalizing state")

// errChapterFinalizeNodeMismatch signals that the reporting node is not the one this chapter's finalize
// attempt is currently dispatched to (read under the FOR UPDATE lock). Treated as an ignored no-op.
var errChapterFinalizeNodeMismatch = errors.New("chapter finalize reporting node mismatch")

// finalizeChapterArtifactTx performs the atomic chapter-finalization transaction and returns
// the resolved playback artifact hash. It locks the chapter and its allocated artifact
// FOR UPDATE, requires the chapter to be in 'finalizing' (else errChapterNotInFinalizing),
// persists artifact readiness + vod_metadata + origin placement, transitions the chapter via
// MarkChapterFinalizedTx (requiring exactly one row), enqueues the completion lifecycle, and
// commits. Any error rolls the whole transaction back.
func finalizeChapterArtifactTx(
	ctx context.Context,
	chapterID, playbackHash, outputPath, nodeID string,
	expectedAttempt int32,
	sizeBytes int64,
	segCount int32,
	hasGaps bool,
	mediaStartMs, mediaEndMs, chapterDurationMs int64,
	tracksPresent bool,
	tracksJSON string,
	outputs map[string]string,
	logger logging.Logger,
	fields logging.Fields,
) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()

	// Lock the chapter row and its allocated playback artifact together, and confirm BOTH the
	// chapter is still 'finalizing' AND the artifact is still 'finalizing' (its allocated state)
	// AND the parent DVR isn't deleted. A late completion arriving after a parent-DVR delete
	// cascade (which soft-deletes the child artifact) must NOT resurrect it to 'ready' — those
	// guards make it an ignored no-op instead. A missing row (chapter/artifact gone) is transient.
	qtx := foghorndb.New(tx)
	locked, lockErr := qtx.LockChapterFinalizeArtifact(ctx, chapterID)
	if lockErr != nil {
		return "", lockErr
	}
	chapterState, storedHash, tenantID := locked.State, locked.PlaybackArtifactHash, locked.TenantID
	artifactStatus, parentStatus, assignedNode := locked.ArtifactStatus.String, locked.ParentStatus, locked.FinalizeNodeID
	if chapterState != ChapterStateFinalizing {
		return "", errChapterNotInFinalizing
	}
	// Reporting-node authorization, read UNDER the FOR UPDATE lock so a concurrent stale-finalize reclaim
	// cannot reassign finalize_node_id between the check and the finalize. Only the node this attempt is
	// currently dispatched to may finalize it (and become its recorded origin). A mismatch/unset assignment is
	// an ignored no-op — not a retry — so it does not bounce a legitimately-reassigned attempt.
	if assignedNode == "" || assignedNode != nodeID {
		return "", errChapterFinalizeNodeMismatch
	}
	if expectedAttempt <= 0 || locked.FinalizeAttempts != expectedAttempt {
		return "", errChapterNotInFinalizing
	}
	if artifactStatus != "finalizing" {
		// The allocated artifact is no longer awaiting finalization (deleted by a parent cascade,
		// or already finalized/failed). Do NOT resurrect it — ignored no-op.
		return "", errChapterNotInFinalizing
	}
	if parentStatus == "deleted" {
		// Parent DVR was deleted; the chapter must not finalize into a live artifact.
		return "", errChapterNotInFinalizing
	}
	resolvedHash := storedHash
	if resolvedHash == "" {
		resolvedHash = playbackHash
	}
	if resolvedHash != playbackHash {
		logger.WithFields(fields).WithFields(logging.Fields{
			"allocated_hash": resolvedHash,
			"result_hash":    playbackHash,
		}).Warn("Chapter finalize: result artifact hash differs from allocated; using allocated")
	}

	// Artifact row → reflect the produced MKV (size, format, duration, tracks) and move to
	// local/pending. Guard on status='finalizing' + require exactly one row so a concurrent
	// delete that slipped between the lock read and this write cannot be clobbered.
	affected, dbErr := qtx.FinalizeChapterPlaybackArtifact(ctx, foghorndb.FinalizeChapterPlaybackArtifactParams{
		ArtifactHash: resolvedHash, SizeBytes: sizeBytes, TracksJson: tracksJSON,
		DurationMs: chapterDurationMs, TracksPresent: tracksPresent,
	})
	if dbErr != nil {
		return "", dbErr
	}
	if affected != 1 {
		return "", errChapterNotInFinalizing
	}

	// vod_metadata (codecs/resolution/fps) from Helmsman stream info — same tx.
	if metaErr := updateChapterVodMetadataTx(ctx, tx, resolvedHash, outputs); metaErr != nil {
		return "", metaErr
	}

	// Origin placement + node-copy event — same tx. This node wrote the canonical MKV.
	if outputPath != "" {
		if regErr := RegisterOriginArtifactTx(ctx, tx, resolvedHash, nodeID, outputPath, sizeBytes, true); regErr != nil {
			return "", regErr
		}
	}

	// Chapter transition. We hold the chapter lock and confirmed 'finalizing', so exactly one
	// row must transition; anything else is an anomaly that must not commit.
	rows, finErr := MarkChapterFinalizedTx(ctx, tx, chapterID, expectedAttempt, segCount, hasGaps, mediaStartMs, mediaEndMs)
	if finErr != nil {
		return "", finErr
	}
	if rows != 1 {
		return "", fmt.Errorf("chapter finalize transitioned %d rows, want exactly 1", rows)
	}

	// Completion lifecycle in the same tx (durable outbox).
	vodData := &ipcpb.VodLifecycleData{
		Status:  ipcpb.VodLifecycleData_STATUS_COMPLETED,
		VodHash: resolvedHash,
	}
	if tenantID != "" {
		vodData.TenantId = &tenantID
	}
	now := time.Now().Unix()
	vodData.CompletedAt = &now
	if sizeBytes > 0 {
		u := uint64(sizeBytes)
		vodData.SizeBytes = &u
	}
	if outputPath != "" {
		vodData.FilePath = &outputPath
	}
	if enqErr := artifactoutbox.EnqueueVodLifecycleTx(ctx, tx, vodData); enqErr != nil {
		return "", enqErr
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return "", commitErr
	}
	committed = true
	return resolvedHash, nil
}

func chapterArtifactLifecycleIdentity(ctx context.Context, chapterID string) (artifactHash, tenantID string, err error) {
	if db == nil {
		return "", "", sql.ErrConnDone
	}
	row, err := foghorndb.New(db).GetChapterArtifactLifecycleIdentity(ctx, chapterID)
	return row.PlaybackArtifactHash.String, row.TenantID, err
}

// updateChapterVodMetadataTx mirrors VodPipeline.updateVodMetadata's schema fill on the
// caller's transaction (chapter jobs live outside foghorn.processing_jobs). An empty outputs
// map is a no-op success — the finalize tx must still commit for a chapter that reported no
// stream-info metadata.
func updateChapterVodMetadataTx(
	ctx context.Context,
	tx *sql.Tx,
	artifactHash string,
	outputs map[string]string,
) error {
	if len(outputs) == 0 {
		return nil
	}
	err := foghorndb.New(tx).UpsertChapterVodMetadata(ctx, foghorndb.UpsertChapterVodMetadataParams{
		ArtifactHash: artifactHash,
		DurationMs:   nullStringChapterMeta(outputs["duration_ms"]), Resolution: nullStringChapterMeta(outputs["resolution"]),
		VideoCodec: nullStringChapterMeta(outputs["video_codec"]), AudioCodec: nullStringChapterMeta(outputs["audio_codec"]),
		BitrateKbps: nullStringChapterMeta(outputs["bitrate_kbps"]), Width: nullStringChapterMeta(outputs["width"]),
		Height: nullStringChapterMeta(outputs["height"]), Fps: nullStringChapterMeta(outputs["fps"]),
		AudioChannels: nullStringChapterMeta(outputs["audio_channels"]), AudioSampleRate: nullStringChapterMeta(outputs["audio_sample_rate"]),
	})
	if err != nil {
		return fmt.Errorf("chapter vod_metadata upsert: %w", err)
	}
	return nil
}

func nullStringChapterMeta(s string) sql.NullString {
	return sql.NullString{String: strings.TrimSpace(s), Valid: strings.TrimSpace(s) != ""}
}

// chapterPlaybackArtifactHashFromOutputs prefers the outputs map's
// explicit artifact_hash when present, else derives from the output
// path's filename (matches Helmsman's vod/<hash>.mkv layout).
func chapterPlaybackArtifactHashFromOutputs(outputs map[string]string, outputPath string) string {
	if v, ok := outputs["artifact_hash"]; ok && v != "" {
		return v
	}
	if outputPath == "" {
		return ""
	}
	base := outputPath
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSuffix(base, ".mkv")
	return base
}

// resolveChapterArtifactContent returns a ContentResolution when the
// input matches a chapter VOD artifact's Commodore-minted public
// playback_id. Raw artifact hashes are NOT accepted — public playback
// IDs are the only chapter address.
//
// Returns nil when input doesn't match — caller falls through to the
// standard Commodore-backed resolution.
//
// Auth + tenant + stream context inherit from the parent DVR via the
// chapter row, mirroring the artifact-origin policy walk used by
// DVRChapterPolicyInternalName.
func resolveChapterArtifactContent(ctx context.Context, input string) *ContentResolution {
	if db == nil || CommodoreClient == nil {
		return nil
	}
	input = strings.TrimSpace(input)
	originalInput := input
	resp, resolveErr := CommodoreClient.ResolveChapterPlaybackID(ctx, input)
	if resolveErr != nil || resp == nil || !resp.GetFound() {
		return nil
	}
	input = resp.GetArtifactHash()
	if len(input) != 32 {
		return nil
	}
	row, scanErr := foghorndb.New(db).GetChapterArtifactResolution(ctx, input)
	if scanErr != nil {
		return nil
	}
	originType, originID := row.OriginType, row.OriginID
	if !originType.Valid || originType.String != "dvr_chapter" || !originID.Valid {
		return nil
	}
	chapter, err := GetChapter(ctx, originID.String)
	if err != nil {
		return nil
	}
	// MarkChapterFinalizing allocates playback_artifact_hash before the
	// MKV exists. Refusing playback for not-yet-playable chapters
	// surfaces a clean "content not found" up the resolver chain
	// instead of routing viewers at an artifact whose .mkv is still
	// in the remux job.
	if !isPlayableChapterState(chapter.State) {
		return nil
	}
	// Parent DVR carries the playback policy; chapter inherits it.
	parent, err := CommodoreClient.ResolveDVRHash(ctx, chapter.ArtifactHash)
	if err != nil || parent == nil || !parent.GetFound() {
		return nil
	}
	res := &ContentResolution{
		ContentType: "chapter",
		// ContentId is what the caller passed in — keep it as the
		// public playback_id when one was used so downstream URL
		// generation stays public-ID-shaped (not artifact-hash-shaped).
		ContentId: originalInput,
		TenantId:  parent.GetTenantId(), UserId: parent.GetUserId(), StreamId: parent.GetStreamId(),
		InternalName: "vod+" + input, ArtifactHash: input, OriginClusterID: parent.GetOriginClusterId(),
		ParentStreamInternalName: parent.GetStreamInternalName(), RequiresAuth: true,
	}
	if parentPlaybackID := parent.GetPlaybackId(); parentPlaybackID != "" {
		if policy, perr := CommodoreClient.ResolveArtifactPlaybackID(ctx, parentPlaybackID); perr == nil && policy.GetFound() {
			res.RequiresAuth = policy.GetRequiresAuth()
			res.ClusterPeers = policy.GetClusterPeers()
			res.AuthorityClusterPeers = policy.GetAuthorityClusterPeers()
		}
	}
	return res
}

// resolveChapterArtifactPlaybackResp synthesizes a
// ResolveArtifactPlaybackIDResponse for a chapter artifact_hash so
// ResolveArtifactPlayback can flow through the standard
// foghorn.artifacts placement path while preserving parent-DVR
// auth inheritance for hidden chapter VODs.
//
// Returns (nil, false) for any input that isn't a chapter artifact —
// caller falls through to the normal Commodore-backed resolution.
func resolveChapterArtifactPlaybackResp(ctx context.Context, input string) (*commodorepb.ResolveArtifactPlaybackIDResponse, bool) {
	if db == nil || CommodoreClient == nil {
		return nil, false
	}
	input = strings.TrimSpace(input)
	// Public chapter playback_id → artifact_hash. No artifact-hash
	// fallback: public playback IDs are the only chapter address.
	chapterPB, resolveErr := CommodoreClient.ResolveChapterPlaybackID(ctx, input)
	if resolveErr != nil || chapterPB == nil || !chapterPB.GetFound() {
		return nil, false
	}
	input = chapterPB.GetArtifactHash()
	if len(input) != 32 {
		return nil, false
	}
	row, scanErr := foghorndb.New(db).GetPlayableChapterArtifactResolution(ctx, input)
	if scanErr != nil {
		return nil, false
	}
	originType, originID := row.OriginType, row.OriginID
	if !originType.Valid || originType.String != "dvr_chapter" || !originID.Valid {
		return nil, false
	}
	chapter, err := GetChapter(ctx, originID.String)
	if err != nil {
		return nil, false
	}
	if !isPlayableChapterState(chapter.State) {
		return nil, false
	}
	parent, err := CommodoreClient.ResolveDVRHash(ctx, chapter.ArtifactHash)
	if err != nil || parent == nil || !parent.GetFound() {
		return nil, false
	}
	resp := &commodorepb.ResolveArtifactPlaybackIDResponse{
		Found:                    true,
		ArtifactHash:             input,
		InternalName:             input, // bare hash; ResolveArtifactPlayback adds vod+ prefix elsewhere if needed
		TenantId:                 parent.GetTenantId(),
		UserId:                   parent.GetUserId(),
		StreamId:                 parent.GetStreamId(),
		ContentType:              "chapter",
		OriginClusterId:          parent.GetOriginClusterId(),
		ParentStreamInternalName: parent.GetStreamInternalName(),
		// Fail-closed default: chapter playback inherits parent-DVR
		// policy. If we can't reach Commodore to confirm public access,
		// authenticate. Only flip to public when the parent's policy
		// lookup succeeds and explicitly says RequiresAuth=false.
		RequiresAuth: true,
	}
	if parentPB := parent.GetPlaybackId(); parentPB != "" {
		if policy, perr := CommodoreClient.ResolveArtifactPlaybackID(ctx, parentPB); perr == nil && policy.GetFound() {
			resp.RequiresAuth = policy.GetRequiresAuth()
			resp.ClusterPeers = policy.GetClusterPeers()
			resp.AuthorityClusterPeers = policy.GetAuthorityClusterPeers()
		}
	}
	return resp, true
}

// ChapterArtifactInfo carries the routing context for a chapter VOD
// artifact resolved from Foghorn's media-plane rows. Used by
// STREAM_SOURCE when a `vod+<chapter_artifact_hash>` token reaches
// Mist; this path preserves parent-DVR policy inheritance.
type ChapterArtifactInfo struct {
	ArtifactHash    string
	TenantID        string
	OriginClusterID string
	StreamID        string
}

// ResolveChapterArtifactByHash returns the chapter context for an
// artifact_hash matching a chapter-origin VOD. Returns nil for any
// other input — callers fall through to other resolution paths.
// Resolves a chapter VOD artifact's routing context from its raw
// artifact_hash. Used by internal STREAM_SOURCE handlers — DTSH gen,
// the freeze pipeline, the DTSH retry sweep, AND clip harvest from a
// historical chapter (vod+<chapter_artifact_hash> as a Mist input
// source for the clip remux). The security boundary for chapter
// playback is inherited parent-DVR auth, not the raw-hash addressing.
func ResolveChapterArtifactByHash(ctx context.Context, artifactHash string) *ChapterArtifactInfo {
	if db == nil || CommodoreClient == nil {
		return nil
	}
	artifactHash = strings.TrimSpace(artifactHash)
	if len(artifactHash) != 32 {
		return nil
	}
	row, err := foghorndb.New(db).GetChapterArtifactRouting(ctx, artifactHash)
	if err != nil {
		return nil
	}
	originType, originID := row.OriginType, row.OriginID
	if !originType.Valid || originType.String != "dvr_chapter" || !originID.Valid {
		return nil
	}
	chapter, err := GetChapter(ctx, originID.String)
	if err != nil || chapter == nil {
		return nil
	}
	parent, err := CommodoreClient.ResolveDVRHash(ctx, chapter.ArtifactHash)
	if err != nil || parent == nil || !parent.GetFound() {
		// Still return useful context from the foghorn row.
		return &ChapterArtifactInfo{
			ArtifactHash:    artifactHash,
			TenantID:        row.TenantID,
			OriginClusterID: row.OriginClusterID,
		}
	}
	return &ChapterArtifactInfo{
		ArtifactHash:    artifactHash,
		TenantID:        parent.GetTenantId(),
		OriginClusterID: parent.GetOriginClusterId(),
		StreamID:        parent.GetStreamId(),
	}
}

// isPlayableChapterState gates resolver entries on a chapter's
// readiness. playback_artifact_hash is allocated at finalize-dispatch
// time so the row exists in 'finalizing', but the .mkv doesn't —
// playback resolution must wait until the chapter reaches finalized
// or beyond (frozen/reclaimed are stable; the artifact persists).
func isPlayableChapterState(state string) bool {
	switch state {
	case ChapterStateFinalized, ChapterStateFrozen, ChapterStateReclaimed:
		return true
	default:
		return false
	}
}

// chapterTerminalFailure inspects the Helmsman processing-result error
// and outputs to decide whether the chapter should retry or fail
// terminally. Source-missing surfaces as outputs["chapter_failure"] =
// "source_missing"; everything else is transient.
func chapterTerminalFailure(outputs map[string]string, errMsg string) (bool, string) {
	if outputs["chapter_failure"] == "source_missing" {
		reason := outputs["chapter_failure_detail"]
		if reason == "" {
			reason = "source segments unavailable; recovery exhausted"
		}
		return true, reason
	}
	if errMsg != "" && strings.Contains(strings.ToLower(errMsg), "source_missing") {
		return true, errMsg
	}
	return false, ""
}
