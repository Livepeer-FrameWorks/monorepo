package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	perArtifactSweepTimeout = 30 * time.Second
)

// CHAPTER SWEEPER
// Periodic job that drives chapter boundaries on active DVRs. Two
// responsibilities:
//
//   1. Boundary rotation. When an active DVR's current chapter's end_ms
//      has crossed now(), close it (state open → closed) and open the
//      next chapter at the next interval. The closed row enters
//      jobs/chapter_finalization_queue.go, which produces the
//      canonical .mkv chapter artifact.
//
//   2. Open-state recovery. If an active DVR has no current chapter
//      (sweeper restart after a missed tick), open the chapter that
//      contains now() so playback timeline progress doesn't stall.
//
// Bounded operations: the sweeper queries each artifact's policy and
// the current chapter row only — never enumerates segments — in
// keeping with the unbounded-artifact-lifetime invariant.

type ChapterSweeperConfig struct {
	DB       *sql.DB
	Logger   logging.Logger
	Interval time.Duration
}

type ChapterSweeper struct {
	db       *sql.DB
	logger   logging.Logger
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func NewChapterSweeper(cfg ChapterSweeperConfig) *ChapterSweeper {
	interval := cfg.Interval
	if interval == 0 {
		interval = 60 * time.Second
	}
	return &ChapterSweeper{
		db:       cfg.DB,
		logger:   cfg.Logger,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

func (s *ChapterSweeper) Start() {
	s.wg.Add(1)
	go s.run()
	s.logger.WithField("interval_seconds", int(s.interval.Seconds())).Info("Chapter sweeper started")
}

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

func (s *ChapterSweeper) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if cleared, clearErr := control.ClearCurrentChaptersForInactiveDVRs(ctx); clearErr != nil {
		s.logger.WithError(clearErr).Warn("Chapter sweep: failed to clear inactive current chapters")
	} else if cleared > 0 {
		s.logger.WithField("cleared", cleared).Info("Chapter sweep: cleared inactive current chapters")
	}

	rows, err := foghorndb.New(s.db).ListActiveDVRChapterPolicies(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("Chapter sweep: failed to enumerate active DVRs")
		return
	}

	type artifactRow struct {
		hash            string
		mode            string
		intervalSeconds int32
		startedAtMs     int64
		windowSeconds   int32
	}
	var artifacts []artifactRow
	for _, row := range rows {
		artifacts = append(artifacts, artifactRow{
			hash: row.ArtifactHash, mode: row.DvrChapterMode.String,
			intervalSeconds: row.ChapterIntervalSeconds,
			startedAtMs:     row.StartedAtMs, windowSeconds: row.WindowSeconds,
		})
	}
	nowMs := time.Now().UnixMilli()

	for _, art := range artifacts {
		artifactCtx, artifactCancel := context.WithTimeout(ctx, perArtifactSweepTimeout)
		var notify bool
		if err := control.WithDVRChapterMutationTx(artifactCtx, art.hash, func(tx *sql.Tx) error {
			var txErr error
			notify, txErr = s.processArtifact(artifactCtx, tx, art.hash, art.mode, art.intervalSeconds, art.startedAtMs, art.windowSeconds, nowMs)
			return txErr
		}); err != nil {
			s.logger.WithError(err).WithField("artifact_hash", art.hash).Warn("Chapter sweep: failed to lock artifact")
		} else if notify {
			control.NotifyChapterMutationCommitted()
		}
		artifactCancel()
	}
}

// processArtifact rotates chapters on one active DVR. Closes the
// current chapter when its boundary has passed and opens the next.
//
// When chapter mode is window_sized_chapters and the artifact's
// dvr_chapter_interval is 0, fall back to dvr_window_seconds (the
// live window doubles as the chapter length). For fixed_interval the
// interval must be set; otherwise the artifact is silently skipped.
func (s *ChapterSweeper) processArtifact(
	ctx context.Context,
	tx *sql.Tx,
	artifactHash, mode string,
	intervalSeconds int32,
	startedAtMs int64,
	windowSeconds int32,
	nowMs int64,
) (bool, error) {
	logFields := logging.Fields{
		"artifact_hash": artifactHash,
		"mode":          mode,
	}

	effInterval := control.EffectiveChapterInterval(mode, intervalSeconds, windowSeconds)
	startMs, endMs, ok := control.CurrentChapterBounds(mode, effInterval, startedAtMs, nowMs)
	if !ok {
		return false, nil
	}

	prev, err := control.CurrentChapterTx(ctx, tx, artifactHash)
	if err != nil {
		s.logger.WithError(err).WithFields(logFields).Warn("Chapter sweep: failed to read current chapter")
		return false, err
	}
	if prev != nil && prev.StartMs == startMs && prev.EndMs == endMs {
		return false, nil
	}
	notify, err := control.BackfillChaptersThroughTx(ctx, tx, artifactHash, mode, effInterval, startedAtMs, nowMs)
	if err != nil {
		if errors.Is(err, sql.ErrConnDone) {
			return false, err
		}
		s.logger.WithError(err).WithFields(logFields).Warn("Chapter sweep: backfill through nowMs failed")
		return false, err
	}
	s.logger.WithFields(logFields).WithFields(logging.Fields{
		"start_ms": startMs,
		"end_ms":   endMs,
	}).Info("Chapter sweep: boundary advanced (backfilled any missed intervals)")
	return notify, nil
}
