package control

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/artifactoutbox"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// Chapter-finalize jobs don't share the processing_jobs ledger (which
// keys on UUID job_ids) — they live in foghorn.dvr_chapters and have
// string job_ids of the form "chapter-finalize-<chapter_id>". Routing
// happens at the top of processProcessingJobResult; this file owns the
// chapter-side state advance, artifact registration, and downstream
// DTSH dispatch.

const chapterFinalizeJobIDPrefix = "chapter-finalize-"

func chapterIDFromJobID(jobID string) string {
	if !strings.HasPrefix(jobID, chapterFinalizeJobIDPrefix) {
		return ""
	}
	return strings.TrimPrefix(jobID, chapterFinalizeJobIDPrefix)
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
	result *pb.ProcessingJobResult,
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

	if jobStatus == "failed" {
		if terminal, reason := chapterTerminalFailure(result.GetOutputs(), result.GetError()); terminal {
			if err := MarkChapterFailed(ctx, chapterID, ChapterStateFailedSourceMissing, reason); err != nil {
				logger.WithError(err).WithFields(fields).Warn("Chapter finalize: terminal-fail mark failed")
			}
			emitChapterVodLifecycle(ctx, logger, chapterID, pb.VodLifecycleData_STATUS_FAILED, 0, "", reason)
			return
		}
		if err := RetryChapterFinalize(ctx, chapterID, result.GetError()); err != nil {
			logger.WithError(err).WithFields(fields).Warn("Chapter finalize: retry rollback failed")
		}
		return
	}
	if jobStatus != "completed" {
		logger.WithFields(fields).Warn("Chapter finalize: unhandled result status")
		return
	}

	outputPath := result.GetOutputPath()
	playbackHash := chapterPlaybackArtifactHashFromOutputs(result.GetOutputs(), outputPath)
	if playbackHash == "" {
		logger.WithFields(fields).Warn("Chapter finalize: no playback artifact hash in result")
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

	// Update artifact row to reflect the produced MKV — size, format,
	// move to local/pending so the freeze pipeline picks it up. Mirror
	// the normal VOD processing update sans the processing_jobs JOIN.
	if _, dbErr := db.ExecContext(ctx, `
		UPDATE foghorn.artifacts
		   SET status = 'ready',
		       format = 'mkv',
		       size_bytes = NULLIF($2, 0)::bigint,
		       sync_status = 'pending',
		       storage_location = 'local',
		       updated_at = NOW()
		 WHERE artifact_hash = $1
	`, playbackHash, sizeBytes); dbErr != nil {
		logger.WithError(dbErr).WithFields(fields).Warn("Chapter finalize: artifact row update failed")
	}

	// Warm-cache registration so the chapter VOD is immediately
	// playable on the node that produced it. Same hooks as the VOD
	// processing branch — artifact_nodes row + in-memory state.
	if outputPath != "" {
		if artifactRepo != nil {
			if err := artifactRepo.AddCachedNodeWithPath(ctx, playbackHash, nodeID, outputPath, sizeBytes); err != nil {
				logger.WithError(err).WithFields(fields).Warn("Chapter finalize: warm-cache add failed")
			}
		}
		state.DefaultManager().AddNodeArtifact(nodeID, &pb.StoredArtifact{
			ClipHash:  playbackHash,
			FilePath:  outputPath,
			SizeBytes: uint64(sizeBytes),
			CreatedAt: time.Now().Unix(),
			Format:    "mkv",
		})
	}

	if err := MarkChapterFinalized(ctx, chapterID, segCount, hasGaps, mediaStartMs, mediaEndMs); err != nil {
		logger.WithError(err).WithFields(fields).Warn("Chapter finalize: state update failed")
		return
	}
	logger.WithFields(fields).WithFields(logging.Fields{
		"artifact_hash": playbackHash,
		"segments":      segCount,
		"has_gaps":      hasGaps,
	}).Info("Chapter finalized (state=finalized)")

	// Populate vod_metadata from Helmsman's stream-info outputs so the
	// chapter artifact behaves like any other VOD on the player side
	// (duration, resolution, codecs, fps). Mirrors VodPipeline's
	// updateVodMetadata without the processing_jobs lookup it does
	// first (chapter jobs are not in that table).
	updateChapterVodMetadata(ctx, logger, fields, playbackHash, result.GetOutputs())
	emitChapterVodLifecycle(ctx, logger, chapterID, pb.VodLifecycleData_STATUS_COMPLETED, sizeBytes, outputPath, "")

	// DTSH generation runs on the Helmsman side immediately after
	// PUSH_END (api_sidecar/internal/handlers/processing_chapter.go).
	// Spritesheet / Chandler thumbnail tracks come from the tenant's
	// VOD processing pipeline — chapter finalize attaches the same
	// processes_json the VOD upload flow uses (see chapter_finalization_queue.go),
	// so MistProc fires those tracks during the processing+<hash> boot.
	// No further server-side fan-out is needed here.
}

func emitChapterVodLifecycle(
	ctx context.Context,
	logger logging.Logger,
	chapterID string,
	status pb.VodLifecycleData_Status,
	sizeBytes int64,
	filePath string,
	errMsg string,
) {
	artifactHash, tenantID, lookupErr := chapterArtifactLifecycleIdentity(ctx, chapterID)
	if lookupErr != nil {
		logger.WithError(lookupErr).WithField("chapter_id", chapterID).Warn("Chapter finalize: lifecycle identity lookup failed")
		return
	}
	now := time.Now().Unix()
	data := &pb.VodLifecycleData{
		Status:      status,
		VodHash:     artifactHash,
		TenantId:    &tenantID,
		CompletedAt: &now,
	}
	if status == pb.VodLifecycleData_STATUS_PROCESSING {
		data.StartedAt = &now
		data.CompletedAt = nil
	}
	if sizeBytes > 0 {
		u := uint64(sizeBytes)
		data.SizeBytes = &u
	}
	if filePath != "" {
		data.FilePath = &filePath
	}
	if errMsg != "" {
		data.Error = &errMsg
	}
	artifactoutbox.EnqueueVodLifecycleLogged(data)
}

func chapterArtifactLifecycleIdentity(ctx context.Context, chapterID string) (artifactHash, tenantID string, err error) {
	if db == nil {
		return "", "", sql.ErrConnDone
	}
	err = db.QueryRowContext(ctx, `
		SELECT c.playback_artifact_hash, a.tenant_id::text
		  FROM foghorn.dvr_chapters c
		  JOIN foghorn.artifacts a
		    ON a.artifact_hash = c.playback_artifact_hash
		 WHERE c.chapter_id = $1
	`, chapterID).Scan(&artifactHash, &tenantID)
	return artifactHash, tenantID, err
}

// updateChapterVodMetadata mirrors VodPipeline.updateVodMetadata's
// schema fill without taking a dependency on the pipeline (chapter
// jobs live outside foghorn.processing_jobs).
func updateChapterVodMetadata(
	ctx context.Context,
	logger logging.Logger,
	fields logging.Fields,
	artifactHash string,
	outputs map[string]string,
) {
	if len(outputs) == 0 {
		return
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.vod_metadata (
			artifact_hash,
			duration_ms, resolution, video_codec, audio_codec,
			bitrate_kbps, width, height, fps,
			audio_channels, audio_sample_rate, updated_at
		) VALUES (
			$1,
			$2::integer, $3, $4, $5,
			$6::integer, $7::integer, $8::integer, $9::real,
			$10::integer, $11::integer, NOW()
		)
		ON CONFLICT (artifact_hash) DO UPDATE SET
			duration_ms       = COALESCE(EXCLUDED.duration_ms, foghorn.vod_metadata.duration_ms),
			resolution        = COALESCE(EXCLUDED.resolution, foghorn.vod_metadata.resolution),
			video_codec       = COALESCE(EXCLUDED.video_codec, foghorn.vod_metadata.video_codec),
			audio_codec       = COALESCE(EXCLUDED.audio_codec, foghorn.vod_metadata.audio_codec),
			bitrate_kbps      = COALESCE(EXCLUDED.bitrate_kbps, foghorn.vod_metadata.bitrate_kbps),
			width             = COALESCE(EXCLUDED.width, foghorn.vod_metadata.width),
			height            = COALESCE(EXCLUDED.height, foghorn.vod_metadata.height),
			fps               = COALESCE(EXCLUDED.fps, foghorn.vod_metadata.fps),
			audio_channels    = COALESCE(EXCLUDED.audio_channels, foghorn.vod_metadata.audio_channels),
			audio_sample_rate = COALESCE(EXCLUDED.audio_sample_rate, foghorn.vod_metadata.audio_sample_rate),
			updated_at        = NOW()
	`,
		artifactHash,
		nullIfEmptyChapterMeta(outputs["duration_ms"]),
		nullIfEmptyChapterMeta(outputs["resolution"]),
		nullIfEmptyChapterMeta(outputs["video_codec"]),
		nullIfEmptyChapterMeta(outputs["audio_codec"]),
		nullIfEmptyChapterMeta(outputs["bitrate_kbps"]),
		nullIfEmptyChapterMeta(outputs["width"]),
		nullIfEmptyChapterMeta(outputs["height"]),
		nullIfEmptyChapterMeta(outputs["fps"]),
		nullIfEmptyChapterMeta(outputs["audio_channels"]),
		nullIfEmptyChapterMeta(outputs["audio_sample_rate"]),
	)
	if err != nil {
		logger.WithError(err).WithFields(fields).Warn("Chapter finalize: vod_metadata upsert failed")
	}
}

func nullIfEmptyChapterMeta(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
	var (
		originType, originID sql.NullString
		tenantID             sql.NullString
		internalName         sql.NullString
		requiresAuth         sql.NullBool
	)
	if scanErr := db.QueryRowContext(ctx, `
		SELECT origin_type, origin_id, tenant_id::text,
		       COALESCE(internal_name, ''),
		       (status NOT IN ('deleted', 'failed'))::boolean
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1
	`, input).Scan(&originType, &originID, &tenantID, &internalName, &requiresAuth); scanErr != nil {
		return nil
	}
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
		ContentType: "vod",
		// ContentId is what the caller passed in — keep it as the
		// public playback_id when one was used so downstream URL
		// generation stays public-ID-shaped (not artifact-hash-shaped).
		ContentId:    originalInput,
		TenantId:     parent.GetTenantId(),
		StreamId:     parent.GetStreamId(),
		InternalName: "vod+" + input,
		RequiresAuth: true,
	}
	if parentPlaybackID := parent.GetPlaybackId(); parentPlaybackID != "" {
		if policy, perr := CommodoreClient.ResolveArtifactPlaybackID(ctx, parentPlaybackID); perr == nil && policy.GetFound() {
			res.RequiresAuth = policy.GetRequiresAuth()
			res.ClusterPeers = policy.GetClusterPeers()
		}
	}
	return res
}

// resolveChapterArtifactPlaybackResp synthesizes a
// ResolveArtifactPlaybackIDResponse for a chapter artifact_hash so
// ResolveArtifactPlayback can flow through the standard
// foghorn.artifacts placement/defrost path while preserving parent-DVR
// auth inheritance for hidden chapter VODs.
//
// Returns (nil, false) for any input that isn't a chapter artifact —
// caller falls through to the normal Commodore-backed resolution.
func resolveChapterArtifactPlaybackResp(ctx context.Context, input string) (*pb.ResolveArtifactPlaybackIDResponse, bool) {
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
	var (
		originType, originID, tenantID, internalName sql.NullString
	)
	if scanErr := db.QueryRowContext(ctx, `
		SELECT origin_type, origin_id, tenant_id::text,
		       COALESCE(internal_name, '')
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1
		   AND status NOT IN ('deleted', 'failed')
	`, input).Scan(&originType, &originID, &tenantID, &internalName); scanErr != nil {
		return nil, false
	}
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
	resp := &pb.ResolveArtifactPlaybackIDResponse{
		Found:           true,
		ArtifactHash:    input,
		InternalName:    input, // bare hash; ResolveArtifactPlayback adds vod+ prefix elsewhere if needed
		TenantId:        parent.GetTenantId(),
		StreamId:        parent.GetStreamId(),
		ContentType:     "vod",
		OriginClusterId: parent.GetOriginClusterId(),
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
	var (
		originType, originID, tenantID, originCluster sql.NullString
	)
	if err := db.QueryRowContext(ctx, `
		SELECT origin_type, origin_id, tenant_id::text,
		       COALESCE(origin_cluster_id, '')
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1
		   AND status NOT IN ('deleted', 'failed')
	`, artifactHash).Scan(&originType, &originID, &tenantID, &originCluster); err != nil {
		return nil
	}
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
			TenantID:        tenantID.String,
			OriginClusterID: originCluster.String,
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
