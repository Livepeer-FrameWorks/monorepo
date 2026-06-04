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
		startOffsetMs := -durationMs
		if req.StartUnix != nil {
			startOffsetMs = *req.StartUnix * 1000 // expected negative
		}
		startMsAbs = nowMs + startOffsetMs
		endMsAbs = startMsAbs + durationMs

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

// pickClipSource decides which source feeds a clip harvest:
//   - LIVE        — the live shm window (last ~liveSHMWindow), available
//                   only while the stream is actually live
//   - DVR_ROLLING — the rolling DVR window of an actively recording
//                   (status='recording'|'starting') DVR
//   - CHAPTER     — one finalized chapter artifact whose state ∈
//                   {finalized, frozen, reclaimed}
//
// Selection is coverage-aware and best-effort. A source that *fully*
// covers the requested range wins outright, in priority order
// LIVE → DVR_ROLLING → CHAPTER. When no single source fully covers it,
// the source contributing the largest contiguous covered sub-range wins
// (ties broken LIVE > DVR > VOD) and the clip is harvested over that
// narrower fulfilled range. The request is rejected only when no source
// overlaps it at all — we never substitute the live buffer for a
// historical range it does not contain.
//
// No stitching: one clip is served from exactly one source artifact. A
// request spanning two chapters yields the best single-chapter overlap,
// not a join.

const liveSHMWindowMs = 120 * 1000 // 120s — matches MistServer's default live look-ahead

type clipSourceDecision struct {
	kind                pb.ClipPullRequest_SourceKind
	streamName          string // dvr+<internal_name> for DVR_ROLLING, vod+<artifact_hash> for CHAPTER
	sourceNodeID        string // recording/source node for live-style pulls when known
	dvrHash             string // populated for DVR_ROLLING
	chapterArtifactHash string // populated for CHAPTER
	effectiveStartMs    int64  // fulfilled range start (== requested when fully covered)
	effectiveEndMs      int64  // fulfilled range end
	partial             bool   // fulfilled range narrower than requested
	coverageMs          int64  // effectiveEnd - effectiveStart, for logging
	reason              string // why this source / why partial, for logging
}

// clipCoverage is one candidate source's contiguous covered interval,
// already clipped to the requested range (so covStart >= requestStart
// and covEnd <= requestEnd). An empty streamName means the source is not
// a candidate (e.g. no active DVR, no overlapping chapter).
type clipCoverage struct {
	kind                pb.ClipPullRequest_SourceKind
	streamName          string
	sourceNodeID        string
	dvrHash             string
	chapterArtifactHash string
	covStart            int64
	covEnd              int64
}

// alignFulfilledClipSeconds converts a fulfilled millisecond range into the
// whole-second harvest bounds the processing pipeline works in (source_start_unix
// / source_stop_unix are Unix seconds). The stop always floors so we never claim
// media past the covered end. The start rounding depends on whether the start is
// a hard boundary:
//
//   - Soft start (startHard=false): the fulfilled start equals the request and
//     media extends behind it (a clip-now whose window lies inside the live shm
//     buffer). Floor, which preserves the duration of a whole-second request
//     because the start and end share the same sub-second remainder: a 30s
//     clip-now still harvests exactly 30s instead of losing a second.
//   - Hard start (startHard=true): the fulfilled start was clamped to a media
//     boundary with nothing before it — a DVR segment edge, a chapter edge, the
//     live-window edge, or a freshly-started stream's start. Ceil, so the
//     harvested and reported range stays inside proven media rather than
//     claiming earlier media that does not exist.
//
// ok is false when the range collapses below one second. Deriving the seconds in
// one place keeps the harvested range and the stored/returned range identical.
func alignFulfilledClipSeconds(startHard bool, startMs, endMs int64) (startUnix, stopUnix int64, ok bool) {
	stopUnix = endMs / 1000 // floor
	if startHard {
		startUnix = (startMs + 999) / 1000 // ceil: stay within proven media
	} else {
		startUnix = startMs / 1000 // floor: preserve duration; media extends behind
	}
	return startUnix, stopUnix, stopUnix > startUnix
}

// covers reports whether this candidate fully spans the requested range.
// Coverage is clipped to the request, so full coverage means the covered
// interval reaches both edges.
func (c clipCoverage) covers(reqStartMs, reqEndMs int64) bool {
	return c.streamName != "" && c.covStart <= reqStartMs && c.covEnd >= reqEndMs
}

// alignedDecision converts a candidate's raw covered interval into a decision
// whose effective range is already whole-second aligned (the granularity the
// harvest pipeline works in). ok is false when the candidate is empty or its
// overlap collapses below one second after alignment — i.e. it is not a usable
// clip source. Because alignment happens here, ranking compares the coverage
// that would actually be harvested, not a raw millisecond overlap that might
// later collapse.
func (c clipCoverage) alignedDecision(reqStartMs, reqEndMs int64) (clipSourceDecision, bool) {
	if c.streamName == "" || c.covEnd <= c.covStart {
		return clipSourceDecision{}, false
	}
	// The start is a hard media boundary whenever coverage had to clamp it
	// forward of the request (segment/chapter/stream-start/window edge); only
	// then does rounding round up to stay inside proven media.
	startHard := c.covStart > reqStartMs
	startUnix, stopUnix, ok := alignFulfilledClipSeconds(startHard, c.covStart, c.covEnd)
	if !ok {
		return clipSourceDecision{}, false
	}
	effStart := startUnix * 1000
	effEnd := stopUnix * 1000
	// "Full" is a property of the raw coverage spanning the request, not of the
	// aligned range — otherwise a now-anchored clip-now (sub-second request end)
	// would be flagged partial just for losing its sub-second tail to flooring.
	full := c.covers(reqStartMs, reqEndMs)
	reason := fmt.Sprintf("%s covers [%d,%d) of requested [%d,%d)",
		c.kind.String(), effStart, effEnd, reqStartMs, reqEndMs)
	if !full {
		reason = "best-effort " + reason
	}
	return clipSourceDecision{
		kind:                c.kind,
		streamName:          c.streamName,
		sourceNodeID:        c.sourceNodeID,
		dvrHash:             c.dvrHash,
		chapterArtifactHash: c.chapterArtifactHash,
		effectiveStartMs:    effStart,
		effectiveEndMs:      effEnd,
		partial:             !full,
		coverageMs:          effEnd - effStart,
		reason:              reason,
	}, true
}

// chooseClipSource ranks the three candidates by the coverage they would
// actually harvest after whole-second alignment. The slice order
// {live, dvr, chap} encodes both the full-coverage priority and the partial
// tie-break (LIVE > DVR > VOD). Kept pure so a caller that discovers the chosen
// source is unroutable can re-rank with that candidate zeroed, without
// re-querying. A candidate whose overlap collapses below one second after
// alignment is not viable and is skipped, so a smaller-but-second-aligned
// source can still win instead of the whole request failing.
func chooseClipSource(reqStartMs, reqEndMs int64, live, dvr, chap clipCoverage) (clipSourceDecision, error) {
	ordered := []clipCoverage{live, dvr, chap}

	// Full coverage wins in priority order (viable candidates only).
	for _, c := range ordered {
		if dec, ok := c.alignedDecision(reqStartMs, reqEndMs); ok && !dec.partial {
			return dec, nil
		}
	}

	// Best-effort: largest aligned overlap; strict-greater so earlier
	// (higher-priority) candidates win ties.
	best := clipSourceDecision{}
	for _, c := range ordered {
		dec, ok := c.alignedDecision(reqStartMs, reqEndMs)
		if !ok {
			continue
		}
		if dec.coverageMs > best.coverageMs {
			best = dec
		}
	}
	if best.streamName == "" {
		return clipSourceDecision{}, fmt.Errorf("requested range [%d,%d) has no source with at least one whole second of live buffer, rolling DVR, or finalized chapter coverage", reqStartMs, reqEndMs)
	}
	return best, nil
}

func (s *FoghornGRPCServer) pickClipSource(ctx context.Context, tenantID, streamInternalName string, clipStartMs, clipEndMs int64) (clipSourceDecision, error) {
	live, dvr, chap, err := s.computeClipCoverages(ctx, tenantID, streamInternalName, clipStartMs, clipEndMs)
	if err != nil {
		return clipSourceDecision{}, err
	}
	return chooseClipSource(clipStartMs, clipEndMs, live, dvr, chap)
}

// computeClipCoverages gathers the covered interval each source can serve
// for the requested range. LIVE is assessed in-memory first; when it fully
// covers the request the recorded sources are not queried at all, keeping the
// common clip-now path off the DB critical path (and immune to DVR/chapter
// assessment errors). Otherwise DVR and CHAPTER are assessed against the DB.
func (s *FoghornGRPCServer) computeClipCoverages(ctx context.Context, tenantID, streamInternalName string, clipStartMs, clipEndMs int64) (live, dvr, chap clipCoverage, err error) {
	if clipEndMs <= clipStartMs {
		return live, dvr, chap, fmt.Errorf("invalid clip range [%d, %d)", clipStartMs, clipEndMs)
	}
	if tenantID == "" {
		return live, dvr, chap, fmt.Errorf("tenant_id is required for clip source dispatch")
	}
	nowMs := time.Now().UnixMilli()

	// LIVE — in-memory, no DB. Live is available only while the stream is
	// actually live; an absent/offline stream contributes nothing.
	var liveStartedAtMs int64
	liveAvailable := false
	if ss := state.DefaultManager().GetStreamState(streamInternalName); ss != nil && ss.StartedAt != nil && ss.Status == "live" {
		liveStartedAtMs = ss.StartedAt.UnixMilli()
		liveAvailable = true
	}
	liveStart, liveEnd := liveCoverageRange(liveStartedAtMs, liveAvailable, clipStartMs, clipEndMs, nowMs)
	live = clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_LIVE, covStart: liveStart, covEnd: liveEnd}
	dvr = clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING}
	chap = clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_CHAPTER}
	if liveEnd > liveStart {
		live.streamName = streamInternalName
	}
	if live.covers(clipStartMs, clipEndMs) {
		return live, dvr, chap, nil
	}

	dvr, chap, err = s.computeRecordedCoverages(ctx, tenantID, streamInternalName, clipStartMs, clipEndMs)
	return live, dvr, chap, err
}

// computeRecordedCoverages assesses the DB-backed sources (rolling DVR and
// finalized chapters) for the requested range. Legitimately-unusable
// conditions (stopped, ambiguous origin, no recording node, no overlapping
// chapter) contribute zero coverage so the other sources can still serve;
// genuine DB errors propagate, because we must not downgrade to a shorter clip
// or claim "no media" when the truth is we could not assess the source.
func (s *FoghornGRPCServer) computeRecordedCoverages(ctx context.Context, tenantID, streamInternalName string, clipStartMs, clipEndMs int64) (dvr, chap clipCoverage, err error) {
	dvr = clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING}
	chap = clipCoverage{kind: pb.ClipPullRequest_SOURCE_KIND_CHAPTER}

	// DVR_ROLLING — only when an actively-recording DVR with a known recording
	// node exists. findRecordingDVR reports unusable recordings as an empty
	// node with no error.
	dvrHash, dvrInternalName, dvrStartedAtMs, dvrStatus, dvrRecordingNode, dvrErr := s.findRecordingDVR(ctx, tenantID, streamInternalName)
	if dvrErr != nil {
		return dvr, chap, dvrErr
	}
	if dvrHash != "" && isActiveDVRStatusString(dvrStatus) && dvrRecordingNode != "" {
		cs, ce, coverErr := s.rollingDVRCoverageRange(ctx, dvrHash, dvrStartedAtMs, clipStartMs, clipEndMs)
		if coverErr != nil {
			return dvr, chap, coverErr
		}
		if ce > cs {
			dvr.streamName = "dvr+" + dvrInternalName
			dvr.sourceNodeID = dvrRecordingNode
			dvr.dvrHash = dvrHash
			dvr.covStart = cs
			dvr.covEnd = ce
		}
	}

	// CHAPTER — best single finalized chapter overlapping the range.
	chHash, chStart, chEnd, chErr := s.chapterArtifactBestOverlap(ctx, tenantID, streamInternalName, clipStartMs, clipEndMs)
	if chErr != nil {
		return dvr, chap, chErr
	}
	if chHash != "" && chEnd > chStart {
		chap.streamName = "vod+" + chHash
		chap.chapterArtifactHash = chHash
		chap.covStart = chStart
		chap.covEnd = chEnd
	}

	return dvr, chap, nil
}

// liveCoverageRange returns the slice of [clipStartMs, clipEndMs) the live
// shm buffer can serve, or 0,0 when none. `live` must be true only when the
// source is actually available (stream live with a known StartedAt); an
// absent/offline stream contributes nothing so selection falls through to
// DVR/chapter instead of hard-failing. The lower bound is raised to
// startedAtMs so a freshly-started stream still filling its buffer does not
// claim coverage it does not have.
func liveCoverageRange(startedAtMs int64, live bool, clipStartMs, clipEndMs, nowMs int64) (covStart, covEnd int64) {
	if !live {
		return 0, 0
	}
	lowerBound := max(nowMs-liveSHMWindowMs, startedAtMs)
	covStart = max(clipStartMs, lowerBound)
	covEnd = min(clipEndMs, nowMs)
	if covEnd <= covStart {
		return 0, 0
	}
	return covStart, covEnd
}

// rollingDVRCoverageRange returns the largest contiguous interval of
// [startMs, endMs) the rolling DVR manifest (dvr+<internal_name>) can
// actually serve, or 0,0 when none.
//
// The lower bound is clamped to max(startMs, windowStart, dvrStartedAtMs):
// the rolling window is Mist's sliding live-DVR window (dvr_window_seconds
// long, independent of chapter rotation), and a request reaching back
// before the recording began can still take the overlapping tail. We walk
// the segment ledger and return the longest run with no hole, so a lost
// or reclaimed segment splits the run rather than silently serving a gap.
// `failed_upload` is deliberately NOT excluded: those rows have a local
// file present (only the S3 recovery push failed), and rolling-DVR
// playback runs from Mist/local — not from S3 recovery.
func (s *FoghornGRPCServer) rollingDVRCoverageRange(ctx context.Context, dvrHash string, dvrStartedAtMs, startMs, endMs int64) (int64, int64, error) {
	if s.db == nil {
		return 0, 0, fmt.Errorf("db not configured")
	}
	if endMs <= startMs {
		return 0, 0, fmt.Errorf("invalid range: end <= start")
	}

	var dvrWindowSec sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT dvr_window_seconds FROM foghorn.artifacts WHERE artifact_hash = $1`,
		dvrHash,
	).Scan(&dvrWindowSec); err != nil {
		return 0, 0, err
	}
	nowMs := time.Now().UnixMilli()
	lowerBound := max(startMs, dvrStartedAtMs)
	if dvrWindowSec.Valid && dvrWindowSec.Int64 > 0 {
		lowerBound = max(lowerBound, nowMs-dvrWindowSec.Int64*1000)
	}
	if endMs <= lowerBound {
		return 0, 0, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT GREATEST(media_start_ms, $2) AS seg_start,
		       LEAST(media_end_ms, $3)      AS seg_end
		  FROM foghorn.dvr_segments
		 WHERE artifact_hash = $1
		   AND status NOT IN ('reclaimed', 'deleted_local', 'lost_local')
		   AND media_end_ms > $2
		   AND media_start_ms < $3
		 ORDER BY media_start_ms, media_end_ms
	`, dvrHash, lowerBound, endMs)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	// Coalesce adjacent/overlapping segments into contiguous runs and keep
	// the longest. Ordered by media_start_ms so a gap (next seg_start past
	// the running edge) closes the current run.
	var bestStart, bestEnd, runStart, runEnd int64
	runActive := false
	for rows.Next() {
		var segStart, segEnd int64
		if scanErr := rows.Scan(&segStart, &segEnd); scanErr != nil {
			return 0, 0, scanErr
		}
		switch {
		case !runActive:
			runStart, runEnd = segStart, segEnd
			runActive = true
		case segStart <= runEnd:
			if segEnd > runEnd {
				runEnd = segEnd
			}
		default:
			if runEnd-runStart > bestEnd-bestStart {
				bestStart, bestEnd = runStart, runEnd
			}
			runStart, runEnd = segStart, segEnd
		}
		if runEnd >= endMs {
			break // a run reaching endMs is maximal; later runs start later, so shorter
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if runActive && runEnd-runStart > bestEnd-bestStart {
		bestStart, bestEnd = runStart, runEnd
	}
	if bestEnd <= bestStart {
		return 0, 0, nil
	}
	return bestStart, bestEnd, nil
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
func (s *FoghornGRPCServer) findRecordingDVR(ctx context.Context, tenantID, streamInternalName string) (dvrHash, dvrInternalName string, startedAtMs int64, status, recordingNodeID string, err error) {
	if s.db == nil {
		return "", "", 0, "", "", fmt.Errorf("db not configured")
	}
	if tenantID == "" {
		return "", "", 0, "", "", fmt.Errorf("tenant_id is required")
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
			return "", "", 0, "", "", nil
		}
		return "", "", 0, "", "", scanErr
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
	if !isActiveDVRStatusString(status) || dvrHash == "" {
		return dvrHash, dvrInternalName, startedAtMs, status, "", nil
	}

	rows, rowsErr := s.db.QueryContext(ctx, `
		SELECT node_id
		  FROM foghorn.artifact_nodes
		 WHERE artifact_hash = $1
		   AND is_orphaned = false
	`, dvrHash)
	if rowsErr != nil {
		return "", "", 0, "", "", rowsErr
	}
	defer rows.Close()
	var candidates []string
	for rows.Next() {
		var nodeID string
		if scanErr := rows.Scan(&nodeID); scanErr != nil {
			return "", "", 0, "", "", scanErr
		}
		if nodeID != "" {
			candidates = append(candidates, nodeID)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return "", "", 0, "", "", rowsErr
	}
	switch len(candidates) {
	case 0:
		return dvrHash, dvrInternalName, startedAtMs, status, "", nil
	case 1:
		return dvrHash, dvrInternalName, startedAtMs, status, candidates[0], nil
	default:
		// Ambiguous recording origin: no single node can serve the rolling
		// pull. This is not an infra error — it just means the rolling DVR
		// is not a usable clip source, so report no node and let the caller
		// fall through to chapter/live coverage.
		return dvrHash, dvrInternalName, startedAtMs, status, "", nil
	}
}

// chapterArtifactBestOverlap returns the playback_artifact_hash of the
// finalized chapter for the given stream with the largest overlap of
// [clipStartMs, clipEndMs), along with the overlapping interval. Searches
// across every DVR recording the stream has produced, so a clip from an
// old chapter works regardless of which DVR is currently active. Uses the
// chapter's actual media span when known (it can differ from the scheduled
// start_ms/end_ms when segment boundaries don't align to rotation), so the
// reported overlap reflects real media. Returns "" when no chapter overlaps.
func (s *FoghornGRPCServer) chapterArtifactBestOverlap(ctx context.Context, tenantID, streamInternalName string, clipStartMs, clipEndMs int64) (hash string, covStart, covEnd int64, err error) {
	if s.db == nil {
		return "", 0, 0, fmt.Errorf("db not configured")
	}
	if tenantID == "" {
		return "", 0, 0, fmt.Errorf("tenant_id is required")
	}
	var (
		h       sql.NullString
		ovStart sql.NullInt64
		ovEnd   sql.NullInt64
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(c.playback_artifact_hash, ''),
		       GREATEST(COALESCE(c.actual_media_start_ms, c.start_ms), $2) AS ov_start,
		       LEAST(COALESCE(c.actual_media_end_ms, c.end_ms), $3)        AS ov_end
		  FROM foghorn.dvr_chapters c
		  JOIN foghorn.artifacts a ON a.artifact_hash = c.artifact_hash
		 WHERE a.artifact_type = 'dvr'
		   AND a.stream_internal_name = $1
		   AND a.tenant_id = $4::uuid
		   AND c.state IN ('finalized', 'frozen', 'reclaimed')
		   AND c.playback_artifact_hash IS NOT NULL
		   AND COALESCE(c.actual_media_end_ms, c.end_ms)   > $2
		   AND COALESCE(c.actual_media_start_ms, c.start_ms) < $3
		 ORDER BY (LEAST(COALESCE(c.actual_media_end_ms, c.end_ms), $3)
		         - GREATEST(COALESCE(c.actual_media_start_ms, c.start_ms), $2)) DESC
		 LIMIT 1
	`, streamInternalName, clipStartMs, clipEndMs, tenantID).Scan(&h, &ovStart, &ovEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, 0, nil
	}
	if err != nil {
		return "", 0, 0, err
	}
	if !h.Valid || h.String == "" || !ovStart.Valid || !ovEnd.Valid || ovEnd.Int64 <= ovStart.Int64 {
		return "", 0, 0, nil
	}
	return h.String, ovStart.Int64, ovEnd.Int64, nil
}
