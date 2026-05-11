package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	maxClosedChapterBackfillPerSweep = control.DefaultClosedChapterBackfillLimit
	perArtifactSweepTimeout          = 30 * time.Second
	terminalBackfillCandidateLimit   = 250
	terminalBackfillArtifactsPerTick = 25
)

// CHAPTER SWEEPER
// Periodic job that materializes DVR chapter manifests. Three responsibilities:
//
//   1. Active current-chapter rolling update. Re-materialize the EVENT-
//      shaped manifest of the chapter containing now() every
//      RebuildIntervalSeconds (default 60s). Bounded query: only that
//      chapter's range is read from dvr_segments.
//
//   2. Boundary close. When the current chapter's end_ms crosses now(),
//      re-materialize one final time as VOD with #EXT-X-ENDLIST, flip
//      is_current=false, and materialize the new current chapter as
//      EVENT.
//
//   3. Dirty rebuild. Late lost_local rows clear last_rebuilt_at on any
//      overlapping materialized chapter. The sweeper rebuilds a bounded batch
//      so cached closed chapters get #EXT-X-GAP without waiting for a viewer.
//
// Backfill / cache-on-request for never-materialized chapters is handled by
// the chapter retrieval RPC directly, not by the sweeper.
//
// Bounded operations: the sweeper queries each artifact's policy + the
// current chapter's range only — never enumerates an artifact's full
// segment list, in keeping with the unbounded-artifact-lifetime invariant.

// ChapterSweeperConfig configures the sweeper.
type ChapterSweeperConfig struct {
	DB                     *sql.DB
	Logger                 logging.Logger
	Interval               time.Duration // sweep tick (default 60s)
	RebuildIntervalSeconds int32         // min seconds between rebuilds of the same active chapter (default 60s)
}

// ChapterSweeper is the periodic job. Single instance per Foghorn process;
// state is in foghorn.dvr_chapters so multiple Foghorn replicas all see
// the same materialized state and the work is naturally idempotent.
type ChapterSweeper struct {
	db                     *sql.DB
	logger                 logging.Logger
	interval               time.Duration
	rebuildIntervalSeconds int32
	stopCh                 chan struct{}
	wg                     sync.WaitGroup
}

// NewChapterSweeper constructs a sweeper with sensible defaults.
func NewChapterSweeper(cfg ChapterSweeperConfig) *ChapterSweeper {
	interval := cfg.Interval
	if interval == 0 {
		interval = 60 * time.Second
	}
	rebuildInterval := cfg.RebuildIntervalSeconds
	if rebuildInterval <= 0 {
		rebuildInterval = 60
	}
	return &ChapterSweeper{
		db:                     cfg.DB,
		logger:                 cfg.Logger,
		interval:               interval,
		rebuildIntervalSeconds: rebuildInterval,
		stopCh:                 make(chan struct{}),
	}
}

// Start begins the background sweep loop.
func (s *ChapterSweeper) Start() {
	s.wg.Add(1)
	go s.run()
	s.logger.WithFields(logging.Fields{
		"interval_seconds":         int(s.interval.Seconds()),
		"rebuild_interval_seconds": s.rebuildIntervalSeconds,
	}).Info("Chapter sweeper started")
}

// Stop signals the loop to exit and waits.
func (s *ChapterSweeper) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	s.logger.Info("Chapter sweeper stopped")
}

func (s *ChapterSweeper) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	firstSweep := time.NewTimer(15 * time.Second)
	defer firstSweep.Stop()

	for {
		select {
		case <-firstSweep.C:
			s.sweep()
		case <-ticker.C:
			s.sweep()
		case <-s.stopCh:
			return
		}
	}
}

// sweep walks every active DVR with a chapter mode set and ensures the
// current chapter's manifest is fresh + the previous chapter has rotated
// to closed if its boundary just passed.
func (s *ChapterSweeper) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	s.processDirtyChapters(ctx)
	s.processTerminalBackfills(ctx)
	if cleared, clearErr := control.ClearCurrentChaptersForInactiveDVRs(ctx); clearErr != nil {
		s.logger.WithError(clearErr).Warn("Chapter sweep: failed to clear inactive current chapters")
	} else if cleared > 0 {
		s.logger.WithField("cleared", cleared).Info("Chapter sweep: cleared inactive current chapters")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact_hash, dvr_chapter_mode, COALESCE(dvr_chapter_interval, 0),
		       COALESCE(EXTRACT(EPOCH FROM started_at)*1000, 0)::bigint,
		       COALESCE(dvr_window_seconds, 0)
		  FROM foghorn.artifacts
		 WHERE artifact_type = 'dvr'
		   AND status IN ('starting', 'recording')
		   AND dvr_chapter_mode IS NOT NULL
		   AND dvr_chapter_mode != ''
	`)
	if err != nil {
		s.logger.WithError(err).Warn("Chapter sweep: failed to enumerate active DVRs")
		return
	}
	defer rows.Close()

	type artifactRow struct {
		hash            string
		mode            string
		intervalSeconds int32
		startedAtMs     int64
		windowSeconds   int32
	}
	var artifacts []artifactRow
	for rows.Next() {
		var r artifactRow
		if err := rows.Scan(&r.hash, &r.mode, &r.intervalSeconds, &r.startedAtMs, &r.windowSeconds); err != nil {
			s.logger.WithError(err).Warn("Chapter sweep: failed to scan artifact row")
			continue
		}
		artifacts = append(artifacts, r)
	}
	if err := rows.Err(); err != nil {
		s.logger.WithError(err).Warn("Chapter sweep: row iteration error")
		return
	}
	nowMs := time.Now().UnixMilli()
	rebuildCutoff := time.Now().Add(-time.Duration(s.rebuildIntervalSeconds) * time.Second)

	for _, art := range artifacts {
		artifactCtx, artifactCancel := context.WithTimeout(ctx, perArtifactSweepTimeout)
		if err := control.WithDVRChapterMutationLock(artifactCtx, art.hash, func() error {
			s.processArtifact(artifactCtx, art.hash, art.mode, art.intervalSeconds, art.startedAtMs, art.windowSeconds, nowMs, rebuildCutoff)
			return nil
		}); err != nil {
			s.logger.WithError(err).WithField("artifact_hash", art.hash).Warn("Chapter sweep: failed to lock artifact")
		}
		artifactCancel()
	}
}

func (s *ChapterSweeper) processDirtyChapters(ctx context.Context) {
	chapters, err := control.DirtyMaterializedChapters(ctx, 100)
	if err != nil {
		s.logger.WithError(err).Warn("Chapter sweep: failed to list dirty chapters")
		return
	}
	for _, ch := range chapters {
		err := control.WithDVRChapterMutationLock(ctx, ch.ArtifactHash, func() error {
			locked, getErr := control.GetChapter(ctx, ch.ChapterID)
			if getErr != nil {
				if errors.Is(getErr, sql.ErrNoRows) {
					return nil
				}
				return getErr
			}
			if locked == nil || locked.LastRebuiltAt.Valid {
				return nil
			}
			isActive := locked.IsCurrent && control.DVRArtifactStillRecording(ctx, locked.ArtifactHash)
			_, _, genErr := control.GenerateChapter(ctx, control.GenerateChapterOptions{
				ArtifactHash:    locked.ArtifactHash,
				Mode:            locked.Mode,
				IntervalSeconds: locked.IntervalSeconds.Int32,
				StartMs:         locked.StartMs,
				EndMs:           locked.EndMs,
				IsActive:        isActive,
			}, s.logger)
			return genErr
		})
		if err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"artifact_hash": ch.ArtifactHash,
				"chapter_id":    ch.ChapterID,
			}).Warn("Chapter sweep: failed to rebuild dirty chapter")
		}
	}
}

func (s *ChapterSweeper) processTerminalBackfills(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact_hash
		  FROM foghorn.artifacts
		 WHERE artifact_type = 'dvr'
		   AND status IN ('completed', 'completed_partial', 'failed', 'ready')
		   AND ended_at IS NOT NULL
		   AND dvr_chapter_mode IS NOT NULL
		   AND dvr_chapter_mode != ''
		   AND dvr_chapter_backfill_complete = false
		 ORDER BY ended_at ASC, artifact_hash ASC
		 LIMIT $1
	`, terminalBackfillCandidateLimit)
	if err != nil {
		s.logger.WithError(err).Warn("Chapter sweep: failed to enumerate terminal DVRs for chapter backfill")
		return
	}
	defer rows.Close()
	processed := 0
	for rows.Next() {
		var artifactHash string
		if err := rows.Scan(&artifactHash); err != nil {
			s.logger.WithError(err).Warn("Chapter sweep: failed to scan terminal DVR")
			continue
		}
		if processed >= terminalBackfillArtifactsPerTick {
			break
		}
		artifactCtx, cancel := context.WithTimeout(ctx, perArtifactSweepTimeout)
		needsBackfill, needsErr := terminalChaptersNeedBackfill(artifactCtx, artifactHash)
		if needsErr != nil {
			cancel()
			s.logger.WithError(needsErr).WithField("artifact_hash", artifactHash).Warn("Chapter sweep: failed to inspect terminal chapter backfill")
			continue
		}
		if !needsBackfill {
			if markErr := control.MarkDVRChapterBackfillComplete(artifactCtx, artifactHash); markErr != nil {
				s.logger.WithError(markErr).WithField("artifact_hash", artifactHash).Warn("Chapter sweep: failed to mark terminal chapter backfill complete")
			}
			cancel()
			continue
		}
		if err := control.FinalizeDVRChapters(artifactCtx, artifactHash, s.logger); err != nil {
			s.logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Chapter sweep: terminal chapter backfill stopped")
		}
		processed++
		cancel()
	}
	if err := rows.Err(); err != nil {
		s.logger.WithError(err).Warn("Chapter sweep: terminal DVR iteration error")
	}
}

func terminalChaptersNeedBackfill(ctx context.Context, artifactHash string) (bool, error) {
	policy, ok, err := control.ReadDVRChapterPolicy(ctx, artifactHash)
	if err != nil || !ok {
		return false, err
	}
	terminalAtMs := policy.EndedAtMs
	if terminalAtMs <= policy.StartedAtMs {
		return false, nil
	}
	atMs := terminalAtMs - 1
	effInterval := policy.EffectiveIntervalSeconds()
	tailStartMs, scheduledTailEndMs, ok := control.CurrentChapterBounds(policy.Mode, effInterval, policy.StartedAtMs, atMs)
	if !ok {
		return false, nil
	}
	tailEndMs := terminalAtMs
	if tailEndMs > scheduledTailEndMs {
		tailEndMs = scheduledTailEndMs
	}
	if tailEndMs <= tailStartMs {
		return false, nil
	}
	latest, err := control.LatestChapterBefore(ctx, artifactHash, policy.Mode, effInterval, tailStartMs)
	if err != nil {
		return false, err
	}
	if firstStart, _, firstOK := control.CurrentChapterBounds(policy.Mode, effInterval, policy.StartedAtMs, policy.StartedAtMs); firstOK {
		if latest == nil && firstStart < tailStartMs {
			return true, nil
		}
	}
	if latest != nil && latest.EndMs < tailStartMs {
		return true, nil
	}
	if firstStart, _, firstOK := control.CurrentChapterBounds(policy.Mode, effInterval, policy.StartedAtMs, policy.StartedAtMs); firstOK && firstStart < tailStartMs {
		intervalMs := int64(effInterval) * 1000
		if intervalMs <= 0 {
			return false, nil
		}
		expected := (tailStartMs - firstStart) / intervalMs
		actual, countErr := control.CountMaterializedClosedChapters(ctx, artifactHash, policy.Mode, effInterval, firstStart, tailStartMs)
		if countErr != nil {
			return false, countErr
		}
		if actual < expected {
			return true, nil
		}
	}
	tailID := control.BuildChapterID(artifactHash, policy.Mode, effInterval, tailStartMs, tailEndMs)
	tail, err := control.GetChapter(ctx, tailID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		return false, err
	}
	return tail == nil || !tail.MaterializedAt.Valid || !tail.LastRebuiltAt.Valid || tail.IsCurrent, nil
}

// processArtifact handles one active DVR. Identifies the current chapter,
// rotates if needed, debounce-rebuilds the active manifest.
//
// When chapter mode is window_sized_chapters and the artifact's
// dvr_chapter_interval is 0, fall back to dvr_window_seconds (the live
// window doubles as the chapter length). For fixed_interval, the interval
// must be set; otherwise the artifact is silently skipped.
func (s *ChapterSweeper) processArtifact(
	ctx context.Context,
	artifactHash, mode string,
	intervalSeconds int32,
	startedAtMs int64,
	windowSeconds int32,
	nowMs int64,
	rebuildCutoff time.Time,
) {
	logFields := logging.Fields{
		"artifact_hash": artifactHash,
		"mode":          mode,
	}

	effInterval := control.EffectiveChapterInterval(mode, intervalSeconds, windowSeconds)
	startMs, endMs, ok := control.CurrentChapterBounds(mode, effInterval, startedAtMs, nowMs)
	if !ok {
		s.logger.WithFields(logFields).WithFields(logging.Fields{
			"interval": effInterval,
			"started":  startedAtMs,
			"now":      nowMs,
		}).Debug("Chapter sweep: cannot compute bounds; skipping")
		return
	}

	// Detect a stale current chapter (boundary passed). If one exists with
	// a different (start_ms, end_ms) than the new computed bounds, close
	// it as VOD before materializing the new current.
	prev, err := control.CurrentChapter(ctx, artifactHash)
	if err != nil {
		s.logger.WithError(err).WithFields(logFields).Warn("Chapter sweep: failed to read current chapter")
		return
	}
	var backfillFromMs int64
	if prev != nil && (prev.StartMs != startMs || prev.EndMs != endMs) {
		if _, _, closeErr := control.GenerateChapter(ctx, control.GenerateChapterOptions{
			ArtifactHash:    prev.ArtifactHash,
			Mode:            prev.Mode,
			IntervalSeconds: prev.IntervalSeconds.Int32,
			StartMs:         prev.StartMs,
			EndMs:           prev.EndMs,
			IsActive:        false,
		}, s.logger); closeErr != nil {
			s.logger.WithError(closeErr).WithFields(logFields).Warn("Chapter sweep: failed to write closed previous chapter")
			return
		}
		backfillFromMs = prev.EndMs
	}
	if backfillFromMs == 0 {
		latest, latestErr := control.LatestChapterBefore(ctx, artifactHash, mode, effInterval, startMs)
		if latestErr != nil {
			s.logger.WithError(latestErr).WithFields(logFields).Warn("Chapter sweep: failed to read latest materialized chapter")
			return
		}
		if latest != nil {
			backfillFromMs = latest.EndMs
		} else if firstStart, _, firstOK := control.CurrentChapterBounds(mode, effInterval, startedAtMs, startedAtMs); firstOK {
			backfillFromMs = firstStart
		}
	}
	if backfillFromMs > 0 && backfillFromMs < startMs {
		if _, backfillErr := control.BackfillClosedChapters(ctx, artifactHash, mode, effInterval, startedAtMs, backfillFromMs, startMs, maxClosedChapterBackfillPerSweep, s.logger); backfillErr != nil {
			s.logger.WithError(backfillErr).WithFields(logFields).Warn("Chapter sweep: closed chapter backfill stopped")
		}
	}

	// Debounce: if the current chapter row exists and was rebuilt within
	// rebuildIntervalSeconds, skip this tick.
	chapterID := control.BuildChapterID(artifactHash, mode, effInterval, startMs, endMs)
	existing, err := control.GetChapter(ctx, chapterID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).WithFields(logFields).Warn("Chapter sweep: failed to read chapter row")
		return
	}
	if existing != nil && existing.LastRebuiltAt.Valid && existing.LastRebuiltAt.Time.After(rebuildCutoff) {
		if existing.IsCurrent {
			// Recently rebuilt; skip. Gap invalidation clears LastRebuiltAt.
			return
		}
	}

	if !control.DVRArtifactStillRecording(ctx, artifactHash) {
		return
	}

	if _, _, err := control.GenerateChapter(ctx, control.GenerateChapterOptions{
		ArtifactHash:    artifactHash,
		Mode:            mode,
		IntervalSeconds: effInterval,
		StartMs:         startMs,
		EndMs:           endMs,
		IsActive:        true,
	}, s.logger); err != nil {
		s.logger.WithError(err).WithFields(logFields).Warn("Chapter sweep: failed to materialize current chapter")
	}
}
