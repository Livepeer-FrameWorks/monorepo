package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/hls"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Builds chapter HLS manifests from foghorn.dvr_segments and uploads them
// to S3 under chapters/{chapter_id}.m3u8. Two manifest shapes:
//
//   active (is_current=true):  EVENT-typed, no #EXT-X-ENDLIST, polled by readers
//   closed (is_current=false): VOD-typed, has #EXT-X-ENDLIST, fixed bounded playlist
//
// Canonical chapter manifests use relative ../segments/{segment_name} URIs
// because chapter playlists live under chapters/. Viewer playback resolves
// dvr+{chapter_id} through MistServer; the edge defrosts this bounded chapter
// and serves the same canonical shape from local disk.
//
// Bounded operations: every call queries dvr_segments with start_ms/end_ms.

// GenerateChapterOptions controls a chapter generation pass.
type GenerateChapterOptions struct {
	ArtifactHash    string
	Mode            string
	IntervalSeconds int32
	StartMs         int64
	EndMs           int64
	IsActive        bool // true = EVENT manifest, no ENDLIST; false = VOD with ENDLIST
}

const (
	DefaultClosedChapterBackfillLimit = 50
	MaxFinalizeChapterBackfillBatches = 10
)

type ClosedChapterBackfillResult struct {
	Generated int
	NextMs    int64
	Complete  bool
}

// GenerateChapter builds and uploads the chapter manifest, then upserts
// the foghorn.dvr_chapters row. Idempotent on chapter_id: repeated
// calls with the same inputs simply re-upload and bump last_rebuilt_at.
//
// Returns the chapter_id and the manifest's S3 key.
func GenerateChapter(ctx context.Context, opts GenerateChapterOptions, logger logging.Logger) (string, string, error) {
	if s3Client == nil {
		return "", "", errors.New("s3 client not configured")
	}
	if opts.ArtifactHash == "" || opts.Mode == "" || opts.EndMs <= opts.StartMs {
		return "", "", fmt.Errorf("invalid chapter range: artifact=%q mode=%q [%d,%d)", opts.ArtifactHash, opts.Mode, opts.StartMs, opts.EndMs)
	}

	tenantID, streamName, ok := resolveDVRTenantAndStream(ctx, opts.ArtifactHash, logger)
	if !ok {
		return "", "", fmt.Errorf("could not resolve tenant/stream for chapter generation: %s", opts.ArtifactHash)
	}

	rows, err := ListDVRSegmentsForRange(ctx, opts.ArtifactHash, opts.StartMs, opts.EndMs)
	if err != nil {
		return "", "", fmt.Errorf("list segments for chapter range: %w", err)
	}

	chapterID := BuildChapterID(opts.ArtifactHash, opts.Mode, opts.IntervalSeconds, opts.StartMs, opts.EndMs)

	finalSegs := make([]hls.FinalSegment, 0, len(rows))
	var hasGaps bool
	var segmentCount int32
	for _, r := range rows {
		switch r.Status {
		case "uploaded", "deleted_local":
			finalSegs = append(finalSegs, hls.FinalSegment{
				Name:              r.SegmentName,
				Sequence:          r.Sequence,
				DurationMs:        r.DurationMs,
				MediaStartMs:      r.MediaStartMs,
				MediaEndMs:        r.MediaEndMs,
				ProgramDateTimeMs: r.MediaStartMs,
			})
			segmentCount++
		case "lost_local":
			finalSegs = append(finalSegs, hls.FinalSegment{
				Name:              r.SegmentName,
				Sequence:          r.Sequence,
				DurationMs:        r.DurationMs,
				MediaStartMs:      r.MediaStartMs,
				MediaEndMs:        r.MediaEndMs,
				ProgramDateTimeMs: r.MediaStartMs,
				Lost:              true,
			})
			hasGaps = true
			segmentCount++
		}
	}

	// Chapter playlists live one level under the artifact prefix
	// (chapters/{chapter_id}.m3u8) but reference the shared segments/
	// directory at the artifact root via "../segments/{name}". The physical
	// segment layout stays one artifact directory; chapter playlists are
	// just different views over the same segment files.
	manifestBody := hls.BuildVOD(finalSegs, hls.BuildVODOptions{
		HasGaps:          hasGaps,
		Event:            opts.IsActive,
		SegmentURIPrefix: "../segments/",
	})

	prefix := s3Client.BuildDVRS3Key(tenantID, streamName, opts.ArtifactHash)
	manifestKey := prefix + "/chapters/" + chapterID + ".m3u8"
	if err := s3Client.PutObject(ctx, manifestKey, []byte(manifestBody), "application/vnd.apple.mpegurl"); err != nil {
		return chapterID, "", fmt.Errorf("upload chapter manifest: %w", err)
	}

	now := time.Now().UTC()
	row := DVRChapterRow{
		ChapterID:       chapterID,
		ArtifactHash:    opts.ArtifactHash,
		Mode:            opts.Mode,
		IntervalSeconds: sql.NullInt32{Int32: opts.IntervalSeconds, Valid: opts.IntervalSeconds > 0},
		StartMs:         opts.StartMs,
		EndMs:           opts.EndMs,
		IsCurrent:       opts.IsActive,
		ManifestS3Key:   sql.NullString{String: manifestKey, Valid: true},
		MaterializedAt:  sql.NullTime{Time: now, Valid: true},
		LastRebuiltAt:   sql.NullTime{Time: now, Valid: true},
		SegmentCount:    segmentCount,
		HasGaps:         hasGaps,
	}
	if err := UpsertChapter(ctx, row); err != nil {
		return chapterID, manifestKey, fmt.Errorf("upsert chapter row: %w", err)
	}
	if opts.IsActive {
		RefreshWarmDVRChapterEdges(ctx, chapterID, logger)
	}
	return chapterID, manifestKey, nil
}

func FinalizeCurrentChapter(ctx context.Context, artifactHash string, logger logging.Logger) error {
	return FinalizeDVRChapters(ctx, artifactHash, logger)
}

func FinalizeDVRChapters(ctx context.Context, artifactHash string, logger logging.Logger) error {
	policy, ok, err := ReadDVRChapterPolicy(ctx, artifactHash)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	terminalAtMs := policy.EndedAtMs
	if terminalAtMs <= 0 {
		terminalAtMs = time.Now().UnixMilli()
	}
	atMs := terminalAtMs
	if atMs > policy.StartedAtMs {
		atMs--
	}
	effInterval := policy.EffectiveIntervalSeconds()
	tailStartMs, scheduledTailEndMs, ok := CurrentChapterBounds(policy.Mode, effInterval, policy.StartedAtMs, atMs)
	if !ok {
		return nil
	}
	tailEndMs := terminalAtMs
	if tailEndMs > scheduledTailEndMs {
		tailEndMs = scheduledTailEndMs
	}
	if tailEndMs <= tailStartMs {
		return nil
	}
	firstStartMs, _, firstOK := CurrentChapterBounds(policy.Mode, effInterval, policy.StartedAtMs, policy.StartedAtMs)

	backfillFromMs := int64(0)
	tailGenerated := false
	prev, err := CurrentChapter(ctx, artifactHash)
	if err != nil {
		return err
	}
	if prev != nil {
		closeEndMs := prev.EndMs
		if terminalAtMs > prev.StartMs && terminalAtMs < closeEndMs {
			closeEndMs = terminalAtMs
		}
		_, _, err = GenerateChapter(ctx, GenerateChapterOptions{
			ArtifactHash:    prev.ArtifactHash,
			Mode:            prev.Mode,
			IntervalSeconds: prev.IntervalSeconds.Int32,
			StartMs:         prev.StartMs,
			EndMs:           closeEndMs,
			IsActive:        false,
		}, logger)
		if err != nil {
			return err
		}
		if closeErr := CloseCurrentChapter(ctx, artifactHash); closeErr != nil {
			return closeErr
		}
		if closeEndMs != prev.EndMs {
			if deleteErr := DeleteChapter(ctx, prev.ChapterID); deleteErr != nil {
				return deleteErr
			}
			deleteChapterManifestObjects(ctx, prev, logger)
		}
		tailGenerated = prev.StartMs == tailStartMs && closeEndMs == tailEndMs
	}
	if firstOK {
		missingStart, missingErr := NextMissingClosedChapterStart(ctx, artifactHash, policy.Mode, effInterval, firstStartMs, tailStartMs)
		if missingErr != nil {
			return missingErr
		}
		backfillFromMs = missingStart
	}

	backfillCapped := false
	for batches := 0; backfillFromMs > 0 && backfillFromMs < tailStartMs; batches++ {
		if batches >= MaxFinalizeChapterBackfillBatches {
			logger.WithFields(logging.Fields{
				"artifact_hash": artifactHash,
				"next_ms":       backfillFromMs,
				"tail_start_ms": tailStartMs,
			}).Warn("DVR chapter finalization stopped closed-chapter backfill at batch cap")
			backfillCapped = true
			break
		}
		result, backfillErr := BackfillClosedChapters(ctx, artifactHash, policy.Mode, effInterval, policy.StartedAtMs, backfillFromMs, tailStartMs, DefaultClosedChapterBackfillLimit, logger)
		if backfillErr != nil {
			return backfillErr
		}
		if result.Complete {
			break
		}
		if result.NextMs <= backfillFromMs {
			return fmt.Errorf("chapter backfill did not advance from %d", backfillFromMs)
		}
		backfillFromMs = result.NextMs
	}

	if tailGenerated {
		if ready, readyErr := terminalChapterIndexComplete(ctx, artifactHash, policy, effInterval, firstStartMs, tailStartMs, tailEndMs, backfillCapped); readyErr != nil {
			return readyErr
		} else if ready {
			return MarkDVRChapterBackfillComplete(ctx, artifactHash)
		}
		return nil
	}
	chapterID := BuildChapterID(artifactHash, policy.Mode, effInterval, tailStartMs, tailEndMs)
	existing, err := GetChapter(ctx, chapterID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existing != nil && existing.MaterializedAt.Valid && existing.LastRebuiltAt.Valid && !existing.IsCurrent {
		if ready, readyErr := terminalChapterIndexComplete(ctx, artifactHash, policy, effInterval, firstStartMs, tailStartMs, tailEndMs, backfillCapped); readyErr != nil {
			return readyErr
		} else if ready {
			return MarkDVRChapterBackfillComplete(ctx, artifactHash)
		}
		return nil
	}
	_, _, err = GenerateChapter(ctx, GenerateChapterOptions{
		ArtifactHash:    artifactHash,
		Mode:            policy.Mode,
		IntervalSeconds: effInterval,
		StartMs:         tailStartMs,
		EndMs:           tailEndMs,
		IsActive:        false,
	}, logger)
	if err != nil {
		return err
	}
	if ready, readyErr := terminalChapterIndexComplete(ctx, artifactHash, policy, effInterval, firstStartMs, tailStartMs, tailEndMs, backfillCapped); readyErr != nil {
		return readyErr
	} else if ready {
		return MarkDVRChapterBackfillComplete(ctx, artifactHash)
	}
	return nil
}

func terminalChapterIndexComplete(ctx context.Context, artifactHash string, policy DVRChapterPolicy, intervalSeconds int32, firstStartMs, tailStartMs, tailEndMs int64, backfillCapped bool) (bool, error) {
	if policy.EndedAtMs <= 0 || backfillCapped || firstStartMs <= 0 {
		return false, nil
	}
	missingStart, err := NextMissingClosedChapterStart(ctx, artifactHash, policy.Mode, intervalSeconds, firstStartMs, tailStartMs)
	if err != nil || missingStart > 0 {
		return false, err
	}
	tailID := BuildChapterID(artifactHash, policy.Mode, intervalSeconds, tailStartMs, tailEndMs)
	tail, err := GetChapter(ctx, tailID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return tail.MaterializedAt.Valid && tail.LastRebuiltAt.Valid && !tail.IsCurrent, nil
}

func BackfillClosedChapters(
	ctx context.Context,
	artifactHash, mode string,
	intervalSeconds int32,
	startedAtMs int64,
	fromMs int64,
	untilStartMs int64,
	limit int,
	logger logging.Logger,
) (ClosedChapterBackfillResult, error) {
	if limit <= 0 {
		limit = DefaultClosedChapterBackfillLimit
	}
	result := ClosedChapterBackfillResult{NextMs: fromMs}
	cursor := fromMs
	for i := 0; i < limit && cursor < untilStartMs; i++ {
		startMs, endMs, ok := CurrentChapterBounds(mode, intervalSeconds, startedAtMs, cursor)
		if !ok {
			return result, fmt.Errorf("cannot compute chapter bounds at %d", cursor)
		}
		if endMs <= cursor {
			return result, fmt.Errorf("chapter bounds did not advance at %d", cursor)
		}
		if startMs >= untilStartMs || endMs > untilStartMs {
			result.Complete = true
			result.NextMs = cursor
			return result, nil
		}
		chapterID := BuildChapterID(artifactHash, mode, intervalSeconds, startMs, endMs)
		existing, err := GetChapter(ctx, chapterID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
		if existing == nil || !existing.MaterializedAt.Valid || !existing.LastRebuiltAt.Valid || existing.IsCurrent {
			if _, _, err := GenerateChapter(ctx, GenerateChapterOptions{
				ArtifactHash:    artifactHash,
				Mode:            mode,
				IntervalSeconds: intervalSeconds,
				StartMs:         startMs,
				EndMs:           endMs,
				IsActive:        false,
			}, logger); err != nil {
				return result, err
			}
			result.Generated++
		}
		cursor = endMs
		result.NextMs = cursor
	}
	result.Complete = cursor >= untilStartMs
	return result, nil
}

func deleteChapterManifestObjects(ctx context.Context, row *DVRChapterRow, logger logging.Logger) {
	if s3Client == nil || row == nil || !row.ManifestS3Key.Valid || row.ManifestS3Key.String == "" {
		return
	}
	if err := s3Client.Delete(ctx, row.ManifestS3Key.String); err != nil {
		logger.WithError(err).WithField("s3_key", row.ManifestS3Key.String).Warn("Failed to delete superseded DVR chapter manifest")
	}
}

type DVRChapterPolicy struct {
	Mode            string
	IntervalSeconds int32
	StartedAtMs     int64
	EndedAtMs       int64
	WindowSeconds   int32
}

func (p DVRChapterPolicy) EffectiveIntervalSeconds() int32 {
	return EffectiveChapterInterval(p.Mode, p.IntervalSeconds, p.WindowSeconds)
}

func ReadDVRChapterPolicy(ctx context.Context, artifactHash string) (DVRChapterPolicy, bool, error) {
	if db == nil {
		return DVRChapterPolicy{}, false, sql.ErrConnDone
	}
	var p DVRChapterPolicy
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(dvr_chapter_mode, ''),
		       COALESCE(dvr_chapter_interval, 0),
		       COALESCE(EXTRACT(EPOCH FROM started_at)*1000, 0)::bigint,
		       COALESCE(EXTRACT(EPOCH FROM ended_at)*1000, 0)::bigint,
		       COALESCE(dvr_window_seconds, 0)
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, artifactHash).Scan(&p.Mode, &p.IntervalSeconds, &p.StartedAtMs, &p.EndedAtMs, &p.WindowSeconds)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DVRChapterPolicy{}, false, nil
		}
		return DVRChapterPolicy{}, false, err
	}
	if p.Mode == "" || p.StartedAtMs <= 0 || p.EffectiveIntervalSeconds() <= 0 {
		return p, false, nil
	}
	return p, true, nil
}

type queryRowContexter interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func DVRChapterMaxRangeMs(ctx context.Context, q queryRowContexter, artifactHash, tenantID string) (int64, error) {
	if q == nil {
		return 0, sql.ErrConnDone
	}
	query := `
		SELECT dvr_window_seconds
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1
		   AND artifact_type = 'dvr'
	`
	args := []interface{}{artifactHash}
	if tenantID != "" {
		query += ` AND tenant_id = $2`
		args = append(args, tenantID)
	}
	var windowSeconds sql.NullInt64
	if err := q.QueryRowContext(ctx, query, args...).Scan(&windowSeconds); err != nil {
		return 0, err
	}
	if windowSeconds.Valid && windowSeconds.Int64 > 0 {
		return windowSeconds.Int64 * 1000, nil
	}
	return int64(time.Hour / time.Millisecond), nil
}

func EffectiveChapterInterval(mode string, intervalSeconds, windowSeconds int32) int32 {
	if intervalSeconds > 0 {
		return intervalSeconds
	}
	if mode == ChapterModeWindowSized {
		return windowSeconds
	}
	return 0
}

// Boundary math for window_sized_chapters and fixed_interval modes.
// Returns the [start_ms, end_ms) of the chapter that contains nowMs under
// the given mode/policy. window_sized_chapters anchors at startedAtMs;
// fixed_interval anchors at unix epoch 0 (UTC, no offset).

// CurrentChapterBounds computes the (start_ms, end_ms) of the chapter that
// contains nowMs for the given artifact policy. Returns ok=false when the
// inputs cannot produce a sensible bounded chapter (e.g. zero interval for
// fixed_interval mode).
func CurrentChapterBounds(mode string, intervalSeconds int32, startedAtMs, nowMs int64) (startMs, endMs int64, ok bool) {
	switch mode {
	case ChapterModeWindowSized:
		if intervalSeconds <= 0 || startedAtMs <= 0 || nowMs < startedAtMs {
			return 0, 0, false
		}
		intervalMs := int64(intervalSeconds) * 1000
		offset := nowMs - startedAtMs
		bucket := offset / intervalMs
		startMs = startedAtMs + bucket*intervalMs
		endMs = startMs + intervalMs
		return startMs, endMs, true
	case ChapterModeFixedInterval:
		if intervalSeconds <= 0 {
			return 0, 0, false
		}
		intervalMs := int64(intervalSeconds) * 1000
		bucket := nowMs / intervalMs
		startMs = bucket * intervalMs
		endMs = startMs + intervalMs
		return startMs, endMs, true
	default:
		return 0, 0, false
	}
}
