package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/state"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// resolveClipAbsoluteRangeMs converts an incoming CreateClipRequest's
// timing fields into an absolute Unix-ms range that the source
// dispatcher can compare against the live shm boundary, the rolling
// DVR window, and chapter ranges. The four legal modes from the
// gateway are:
//
//   - ABSOLUTE:  start_unix (seconds) + duration_sec (seconds)
//   - RELATIVE:  start_ms (seconds-from-stream-start) + stop_ms /
//     duration_sec
//   - DURATION:  start_unix OR start_ms + duration_sec
//   - CLIP_NOW:  negative start_unix relative to now + duration_sec
//
// RELATIVE / DURATION-from-media require the live stream's started_at
// (StreamStateManager carries this once the buffer fills); without it
// we cannot translate media offsets to wall-clock and the dispatch
// rejects the request rather than mis-routing it to LIVE/DVR/CHAPTER.
func resolveClipAbsoluteRangeMs(req *pb.CreateClipRequest, streamInternalName string) (startMsAbs, endMsAbs int64, err error) {
	nowMs := time.Now().UnixMilli()
	mode := req.GetMode()
	durationMs := int64(0)
	if req.DurationSec != nil {
		durationMs = *req.DurationSec * 1000
	}

	resolveMediaStart := func() (int64, error) {
		ss := state.DefaultManager().GetStreamState(streamInternalName)
		if ss == nil || ss.StartedAt == nil {
			return 0, fmt.Errorf("relative clip needs live stream start time, but stream %s has no recorded StartedAt", streamInternalName)
		}
		return ss.StartedAt.UnixMilli(), nil
	}

	switch mode {
	case pb.ClipMode_CLIP_MODE_ABSOLUTE:
		if req.StartUnix == nil {
			return 0, 0, fmt.Errorf("ABSOLUTE clip requires start_unix")
		}
		startMsAbs = *req.StartUnix * 1000
		if req.StopUnix != nil {
			endMsAbs = *req.StopUnix * 1000
		} else if durationMs > 0 {
			endMsAbs = startMsAbs + durationMs
		} else {
			return 0, 0, fmt.Errorf("ABSOLUTE clip requires stop_unix or duration_sec")
		}

	case pb.ClipMode_CLIP_MODE_RELATIVE:
		if req.StartMs == nil {
			return 0, 0, fmt.Errorf("RELATIVE clip requires start_ms")
		}
		anchorMs, anchorErr := resolveMediaStart()
		if anchorErr != nil {
			return 0, 0, anchorErr
		}
		startMsAbs = anchorMs + (*req.StartMs * 1000)
		if req.StopMs != nil {
			endMsAbs = anchorMs + (*req.StopMs * 1000)
		} else if durationMs > 0 {
			endMsAbs = startMsAbs + durationMs
		} else {
			return 0, 0, fmt.Errorf("RELATIVE clip requires stop_ms or duration_sec")
		}

	case pb.ClipMode_CLIP_MODE_DURATION:
		if durationMs <= 0 {
			return 0, 0, fmt.Errorf("DURATION clip requires positive duration_sec")
		}
		switch {
		case req.StartUnix != nil:
			startMsAbs = *req.StartUnix * 1000
		case req.StartMs != nil:
			anchorMs, anchorErr := resolveMediaStart()
			if anchorErr != nil {
				return 0, 0, anchorErr
			}
			startMsAbs = anchorMs + (*req.StartMs * 1000)
		default:
			return 0, 0, fmt.Errorf("DURATION clip requires start_unix or start_ms")
		}
		endMsAbs = startMsAbs + durationMs

	case pb.ClipMode_CLIP_MODE_CLIP_NOW:
		if durationMs <= 0 {
			return 0, 0, fmt.Errorf("CLIP_NOW requires positive duration_sec")
		}
		offset := int64(0)
		if req.StartUnix != nil {
			offset = *req.StartUnix * 1000 // expected negative
		}
		// CLIP_NOW = "the last <duration> seconds ending now-<|offset|>".
		// Without an offset it's "the last <duration> seconds ending now".
		endMsAbs = nowMs + offset
		startMsAbs = endMsAbs - durationMs

	case pb.ClipMode_CLIP_MODE_UNSPECIFIED:
		// Legacy callers may omit mode; treat StartUnix-only inputs as ABSOLUTE.
		if req.StartUnix != nil {
			startMsAbs = *req.StartUnix * 1000
			if req.StopUnix != nil {
				endMsAbs = *req.StopUnix * 1000
			} else if durationMs > 0 {
				endMsAbs = startMsAbs + durationMs
			} else {
				return 0, 0, fmt.Errorf("unspecified-mode clip requires stop_unix or duration_sec")
			}
		} else {
			return 0, 0, fmt.Errorf("clip mode is required")
		}

	default:
		return 0, 0, fmt.Errorf("unsupported clip mode %s", mode.String())
	}

	if endMsAbs <= startMsAbs {
		return 0, 0, fmt.Errorf("clip range non-positive after normalization [%d, %d)", startMsAbs, endMsAbs)
	}
	return startMsAbs, endMsAbs, nil
}

// clipSourceDispatch decides which source feeds a clip harvest:
//   - LIVE        — range overlaps the live shm window (last ~liveSHMWindow)
//   - DVR_ROLLING — range fits in the rolling DVR window of an actively
//                   recording (status='recording'|'starting') DVR
//   - CHAPTER     — range fits entirely within one finalized chapter
//                   artifact whose state ∈ {finalized, frozen, reclaimed}
//
// Cross-source ranges (spanning shm boundary, spanning chapter boundary,
// etc.) are rejected.
//
// CHAPTER is checked across every finalized chapter for the stream — a
// clip from a long-ago recording works as long as the chapter exists.
// Falling through to DVR_ROLLING requires an actively-recording DVR;
// stopped recordings without a covering finalized chapter reject the
// request.

const liveSHMWindowMs = 120 * 1000 // 120s — matches MistServer's default live look-ahead

type clipSourceDecision struct {
	kind                pb.ClipPullRequest_SourceKind
	streamName          string // dvr+<internal_name> for DVR_ROLLING, vod+<artifact_hash> for CHAPTER
	dvrHash             string // populated for DVR_ROLLING
	chapterArtifactHash string // populated for CHAPTER
}

func (s *FoghornGRPCServer) pickClipSource(ctx context.Context, tenantID, streamInternalName string, clipStartMs, clipEndMs int64) (clipSourceDecision, error) {
	if clipEndMs <= clipStartMs {
		return clipSourceDecision{}, fmt.Errorf("invalid clip range [%d, %d)", clipStartMs, clipEndMs)
	}
	if tenantID == "" {
		return clipSourceDecision{}, fmt.Errorf("tenant_id is required for clip source dispatch")
	}
	nowMs := time.Now().UnixMilli()
	liveBoundary := nowMs - liveSHMWindowMs

	// Range entirely inside shm window: live.
	if clipStartMs >= liveBoundary && clipEndMs <= nowMs {
		return clipSourceDecision{
			kind:       pb.ClipPullRequest_SOURCE_KIND_LIVE,
			streamName: streamInternalName,
		}, nil
	}
	// Range spans live boundary: reject (cross-source).
	if clipStartMs < liveBoundary && clipEndMs > liveBoundary {
		return clipSourceDecision{}, fmt.Errorf("clip range spans live/dvr boundary at %d; split the request", liveBoundary)
	}

	// Past-live range. Prefer a covering finalized chapter — those
	// work for any historical recording, active or stopped.
	chapterHash, chapterErr := s.chapterArtifactCoveringStream(ctx, tenantID, streamInternalName, clipStartMs, clipEndMs)
	if chapterErr != nil {
		return clipSourceDecision{}, chapterErr
	}
	if chapterHash != "" {
		return clipSourceDecision{
			kind:                pb.ClipPullRequest_SOURCE_KIND_CHAPTER,
			streamName:          "vod+" + chapterHash,
			chapterArtifactHash: chapterHash,
		}, nil
	}

	// No covering chapter: fall back to rolling DVR, but only when an
	// actively-recording DVR covers the range. The rolling stream
	// (dvr+<internal_name>) is only resolvable while ingest is running.
	dvrHash, dvrInternalName, dvrStartedAtMs, dvrStatus, err := s.findRecordingDVR(ctx, tenantID, streamInternalName)
	if err != nil {
		return clipSourceDecision{}, err
	}
	if dvrHash == "" {
		return clipSourceDecision{}, fmt.Errorf("no playable chapter or active DVR covers the requested range")
	}
	if !isActiveDVRStatusString(dvrStatus) {
		return clipSourceDecision{}, fmt.Errorf("range falls in a recording that is no longer active and has no covering finalized chapter yet")
	}
	if clipStartMs < dvrStartedAtMs {
		return clipSourceDecision{}, fmt.Errorf("clip range starts %dms before DVR recording", dvrStartedAtMs-clipStartMs)
	}
	covers, coverErr := s.rollingDVRCoversRange(ctx, dvrHash, clipStartMs, clipEndMs)
	if coverErr != nil {
		return clipSourceDecision{}, coverErr
	}
	if !covers {
		return clipSourceDecision{}, fmt.Errorf("range is older than the rolling DVR window and no covering finalized chapter exists yet")
	}
	return clipSourceDecision{
		kind:       pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING,
		streamName: "dvr+" + dvrInternalName,
		dvrHash:    dvrHash,
	}, nil
}

// rollingDVRCoversRange verifies the rolling DVR manifest
// (dvr+<internal_name>) can actually serve [startMs, endMs]. The
// rolling window is Mist's sliding live-DVR window (dvr_window_seconds
// long), independent of chapter rotation: a chapter boundary closes an
// archival cut, it does not advance the live window. So an in-flight
// recording can still serve clips across the tail of a previously-closed
// chapter as long as the segments are within the window and haven't
// been physically reclaimed from local disk.
//
// We pair the time bound with a continuity check over the segment
// ledger so a lost or reclaimed segment in the middle of the range
// rejects the dispatch rather than silently serving a hole.
// `failed_upload` is deliberately NOT excluded: those rows have a local
// file present (only the S3 recovery push failed), and rolling-DVR
// playback runs from Mist/local — not from S3 recovery.
func (s *FoghornGRPCServer) rollingDVRCoversRange(ctx context.Context, dvrHash string, startMs, endMs int64) (bool, error) {
	if s.db == nil {
		return false, fmt.Errorf("db not configured")
	}
	if endMs <= startMs {
		return false, fmt.Errorf("invalid range: end <= start")
	}

	var dvrWindowSec sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT dvr_window_seconds FROM foghorn.artifacts WHERE artifact_hash = $1`,
		dvrHash,
	).Scan(&dvrWindowSec); err != nil {
		return false, err
	}
	nowMs := time.Now().UnixMilli()
	if dvrWindowSec.Valid && dvrWindowSec.Int64 > 0 {
		windowStart := nowMs - dvrWindowSec.Int64*1000
		if startMs < windowStart {
			return false, nil
		}
	}

	// Ordered interval walk so duplicate/overlapping ledger rows don't
	// inflate coverage. We coalesce adjacent intervals and track the
	// rightmost contiguous edge from startMs; if it reaches endMs the
	// range is covered, otherwise there's a hole.
	rows, err := s.db.QueryContext(ctx, `
		SELECT GREATEST(media_start_ms, $2) AS seg_start,
		       LEAST(media_end_ms, $3)      AS seg_end
		  FROM foghorn.dvr_segments
		 WHERE artifact_hash = $1
		   AND status NOT IN ('reclaimed', 'deleted_local', 'lost_local')
		   AND media_end_ms > $2
		   AND media_start_ms < $3
		 ORDER BY media_start_ms, media_end_ms
	`, dvrHash, startMs, endMs)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	covered := startMs
	for rows.Next() {
		var segStart, segEnd int64
		if err := rows.Scan(&segStart, &segEnd); err != nil {
			return false, err
		}
		if segStart > covered {
			return false, nil
		}
		if segEnd > covered {
			covered = segEnd
		}
		if covered >= endMs {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return covered >= endMs, nil
}

func isActiveDVRStatusString(s string) bool {
	// finalizing excluded: FinalizeDVR has claimed the stop, so the
	// rolling manifest is no longer the canonical clip source — chapter
	// artifacts cover the historical range.
	switch s {
	case "starting", "recording":
		return true
	default:
		return false
	}
}

// findRecordingDVR returns the most recent DVR row for the source
// stream plus its current status. Caller decides whether to use it
// based on the status — only 'starting'/'recording' should serve as
// a DVR_ROLLING clip source.
func (s *FoghornGRPCServer) findRecordingDVR(ctx context.Context, tenantID, streamInternalName string) (dvrHash, dvrInternalName string, startedAtMs int64, status string, err error) {
	if s.db == nil {
		return "", "", 0, "", fmt.Errorf("db not configured")
	}
	if tenantID == "" {
		return "", "", 0, "", fmt.Errorf("tenant_id is required")
	}
	var hash, internalName, st sql.NullString
	var started sql.NullInt64
	row := s.db.QueryRowContext(ctx, `
		SELECT artifact_hash,
		       COALESCE(internal_name, ''),
		       COALESCE(EXTRACT(EPOCH FROM started_at)*1000, 0)::bigint,
		       status
		  FROM foghorn.artifacts
		 WHERE artifact_type = 'dvr'
		   AND stream_internal_name = $1
		   AND tenant_id = $2::uuid
		 ORDER BY started_at DESC NULLS LAST
		 LIMIT 1
	`, streamInternalName, tenantID)
	if scanErr := row.Scan(&hash, &internalName, &started, &st); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", "", 0, "", nil
		}
		return "", "", 0, "", scanErr
	}
	if hash.Valid {
		dvrHash = hash.String
	}
	if internalName.Valid {
		dvrInternalName = internalName.String
	}
	if started.Valid {
		startedAtMs = started.Int64
	}
	if st.Valid {
		status = st.String
	}
	return dvrHash, dvrInternalName, startedAtMs, status, nil
}

// chapterArtifactCoveringStream returns the playback_artifact_hash of
// any finalized chapter for the given stream whose range fully
// contains [clipStartMs, clipEndMs). Searches across every DVR
// recording the stream has produced, so a clip from an old chapter
// works regardless of which DVR is currently active.
func (s *FoghornGRPCServer) chapterArtifactCoveringStream(ctx context.Context, tenantID, streamInternalName string, clipStartMs, clipEndMs int64) (string, error) {
	if s.db == nil {
		return "", fmt.Errorf("db not configured")
	}
	if tenantID == "" {
		return "", fmt.Errorf("tenant_id is required")
	}
	var hash sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(c.playback_artifact_hash, '')
		  FROM foghorn.dvr_chapters c
		  JOIN foghorn.artifacts a ON a.artifact_hash = c.artifact_hash
		 WHERE a.artifact_type = 'dvr'
		   AND a.stream_internal_name = $1
		   AND a.tenant_id = $4::uuid
		   AND c.start_ms <= $2
		   AND c.end_ms   >= $3
		   AND c.state IN ('finalized', 'frozen', 'reclaimed')
		   AND c.playback_artifact_hash IS NOT NULL
		 ORDER BY c.start_ms DESC
		 LIMIT 1
	`, streamInternalName, clipStartMs, clipEndMs, tenantID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !hash.Valid || hash.String == "" {
		return "", nil
	}
	return hash.String, nil
}
