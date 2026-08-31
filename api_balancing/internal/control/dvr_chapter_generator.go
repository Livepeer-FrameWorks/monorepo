package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Chapter rotation against the rolling DVR. Boundary math, policy
// reads, and the open/close handoff that the sweeper drives.
//
// A boundary trigger does two things in one DB transaction:
//   - closes the currently-open chapter (state open → closed)
//   - opens a new chapter at the next interval (state=open, is_current=true)
//
// The closed chapter then enters jobs/chapter_finalization_queue.go,
// which remuxes its segment range to a canonical .mkv VOD artifact
// (origin_type='dvr_chapter', library_visible=false).

// OpenChapterAtBoundary records a new open chapter row. If a previous
// chapter is still current it is closed in the same transaction (its
// state moves to 'closed', driving the finalization queue).
//
// Idempotent on chapter_id: re-recording the same boundary is a no-op.
func OpenChapterAtBoundary(ctx context.Context, artifactHash, mode string, intervalSeconds int32, startMs, endMs int64) (string, error) {
	if artifactHash == "" || mode == "" || endMs <= startMs {
		return "", fmt.Errorf("invalid chapter range: artifact=%q mode=%q [%d,%d)", artifactHash, mode, startMs, endMs)
	}
	chapterID := BuildChapterID(artifactHash, mode, intervalSeconds, startMs, endMs)
	row := DVRChapterRow{
		ChapterID:       chapterID,
		ArtifactHash:    artifactHash,
		Mode:            mode,
		IntervalSeconds: sql.NullInt32{Int32: intervalSeconds, Valid: intervalSeconds > 0},
		StartMs:         startMs,
		EndMs:           endMs,
		IsCurrent:       true,
		State:           ChapterStateOpen,
	}
	if err := OpenChapter(ctx, row); err != nil {
		return chapterID, err
	}
	return chapterID, nil
}

// CloseTerminalChapter materializes every chapter row up to the
// recording's terminal stop time. The final (possibly partial) chapter
// is inserted with end_ms = terminalAtMs and a chapter_id derived from
// that truncated range, so it matches what ListVirtualChaptersForArtifact
// rebuilds. Any in-flight 'open' row created by the sweeper with the
// scheduled bounds is dropped before insertion — its chapter_id is
// derived from the un-truncated scheduled end and would not overlay
// correctly with the listing's derived ID.
//
// Idempotent: every INSERT is ON CONFLICT (chapter_id) DO NOTHING; the
// open-chapter DELETE is also a no-op on retry once the open row is
// gone. No-op when chapters are disabled.
func CloseTerminalChapter(ctx context.Context, artifactHash string, terminalAtMs int64, logger logging.Logger) error {
	if db == nil {
		return sql.ErrConnDone
	}
	var changed bool
	err := WithDVRChapterMutationTx(ctx, artifactHash, func(tx *sql.Tx) error {
		var txErr error
		changed, txErr = CloseTerminalChapterTx(ctx, tx, artifactHash, terminalAtMs)
		return txErr
	})
	if err != nil {
		logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("CloseTerminalChapter: mutation failed")
		return err
	}
	if changed {
		logger.WithFields(logging.Fields{
			"artifact_hash":  artifactHash,
			"terminal_at_ms": terminalAtMs,
		}).Info("Terminal DVR chapters materialized")
		notifyChapterClosed()
	}
	return nil
}

// CloseTerminalChapterTx performs only the terminal chapter database
// mutation. The caller owns the transaction and, for cross-worker
// serialization, the DVR chapter advisory lock.
func CloseTerminalChapterTx(ctx context.Context, tx foghorndb.DBTX, artifactHash string, terminalAtMs int64) (bool, error) {
	policy, ok, err := readDVRChapterPolicy(ctx, tx, artifactHash)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	intervalSeconds := policy.EffectiveIntervalSeconds()
	if intervalSeconds <= 0 || policy.StartedAtMs <= 0 || terminalAtMs <= policy.StartedAtMs {
		return false, nil
	}
	intervalMs := int64(intervalSeconds) * 1000
	var firstStart int64
	switch policy.Mode {
	case ChapterModeWindowSized:
		firstStart = policy.StartedAtMs
	case ChapterModeFixedInterval:
		firstStart = (policy.StartedAtMs / intervalMs) * intervalMs
	default:
		return false, nil
	}
	// Drop any in-flight open chapter: its bounds are scheduled bounds
	// which won't match the truncated terminal chapter_id we're about to
	// materialize. 'open' implies no finalization has started, so the
	// row is purely metadata and safe to delete.
	q := foghorndb.New(tx)
	if err := q.DeleteOpenDVRChapters(ctx, artifactHash); err != nil {
		return false, fmt.Errorf("drop open chapter at terminal close: %w", err)
	}
	var intervalArg interface{}
	if intervalSeconds > 0 {
		intervalArg = intervalSeconds
	}
	for s := firstStart; s < terminalAtMs; s += intervalMs {
		e := s + intervalMs
		if e > terminalAtMs {
			e = terminalAtMs
		}
		if e <= s {
			continue
		}
		chapterID := BuildChapterID(artifactHash, policy.Mode, intervalSeconds, s, e)
		if err := q.InsertClosedDVRChapter(ctx, foghorndb.InsertClosedDVRChapterParams{
			ChapterID: chapterID, ArtifactHash: artifactHash, Mode: policy.Mode,
			IntervalSeconds: sql.NullInt32{Int32: intervalSeconds, Valid: intervalArg != nil}, StartMs: s, EndMs: e,
		}); err != nil {
			return false, fmt.Errorf("materialize terminal chapter [%d,%d): %w", s, e, err)
		}
	}
	return true, nil
}

// BackfillChaptersThrough materializes every chapter interval from the
// recording's first interval boundary up through the interval that
// contains throughMs. Past intervals are inserted directly in state
// 'closed' (the finalization queue picks them up on its next tick);
// the interval containing throughMs is opened as the new current
// chapter. Idempotent on every chapter_id, so a sweeper that has been
// down for hours recovers every missed boundary on its next run.
//
// No-op when chapters are disabled (empty mode) or the policy inputs
// can't produce a sensible bounded interval.
func BackfillChaptersThrough(
	ctx context.Context,
	artifactHash, mode string,
	intervalSeconds int32,
	startedAtMs, throughMs int64,
) error {
	if db == nil {
		return sql.ErrConnDone
	}
	var notify bool
	err := WithDVRChapterMutationTx(ctx, artifactHash, func(tx *sql.Tx) error {
		var txErr error
		notify, txErr = BackfillChaptersThroughTx(ctx, tx, artifactHash, mode, intervalSeconds, startedAtMs, throughMs)
		return txErr
	})
	if err == nil && notify {
		notifyChapterClosed()
	}
	return err
}

// BackfillChaptersThroughTx performs only the boundary database mutation on
// the caller's lock-owning transaction.
func BackfillChaptersThroughTx(
	ctx context.Context,
	tx foghorndb.DBTX,
	artifactHash, mode string,
	intervalSeconds int32,
	startedAtMs, throughMs int64,
) (bool, error) {
	if artifactHash == "" || mode == "" || intervalSeconds <= 0 || startedAtMs <= 0 {
		return false, nil
	}
	if throughMs <= startedAtMs {
		return false, nil
	}
	targetStart, targetEnd, ok := CurrentChapterBounds(mode, intervalSeconds, startedAtMs, throughMs)
	if !ok {
		return false, nil
	}
	intervalMs := int64(intervalSeconds) * 1000
	// First-interval anchor: window_sized aligns to startedAtMs;
	// fixed_interval aligns to unix epoch 0.
	var firstStart int64
	switch mode {
	case ChapterModeWindowSized:
		firstStart = startedAtMs
	case ChapterModeFixedInterval:
		firstStart = (startedAtMs / intervalMs) * intervalMs
	default:
		return false, nil
	}
	var intervalArg interface{}
	if intervalSeconds > 0 {
		intervalArg = intervalSeconds
	}
	q := foghorndb.New(tx)
	notify := false
	for s := firstStart; s < targetStart; s += intervalMs {
		e := s + intervalMs
		chapterID := BuildChapterID(artifactHash, mode, intervalSeconds, s, e)
		if err := q.InsertClosedDVRChapter(ctx, foghorndb.InsertClosedDVRChapterParams{
			ChapterID: chapterID, ArtifactHash: artifactHash, Mode: mode,
			IntervalSeconds: sql.NullInt32{Int32: intervalSeconds, Valid: intervalArg != nil}, StartMs: s, EndMs: e,
		}); err != nil {
			return false, fmt.Errorf("backfill closed chapter [%d,%d): %w", s, e, err)
		}
		notify = true
	}
	chapterID := BuildChapterID(artifactHash, mode, intervalSeconds, targetStart, targetEnd)
	openedNotify, err := openChapterTx(ctx, tx, DVRChapterRow{
		ChapterID: chapterID, ArtifactHash: artifactHash, Mode: mode,
		IntervalSeconds: sql.NullInt32{Int32: intervalSeconds, Valid: intervalSeconds > 0},
		StartMs:         targetStart, EndMs: targetEnd, IsCurrent: true, State: ChapterStateOpen,
	})
	return notify || openedNotify, err
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
	return readDVRChapterPolicy(ctx, db, artifactHash)
}

func readDVRChapterPolicy(ctx context.Context, conn foghorndb.DBTX, artifactHash string) (DVRChapterPolicy, bool, error) {
	row, err := foghorndb.New(conn).GetDVRChapterPolicy(ctx, artifactHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DVRChapterPolicy{}, false, nil
		}
		return DVRChapterPolicy{}, false, err
	}
	p := DVRChapterPolicy{
		Mode: row.Mode, IntervalSeconds: row.IntervalSeconds, StartedAtMs: row.StartedAtMs,
		EndedAtMs: row.EndedAtMs, WindowSeconds: row.WindowSeconds,
	}
	if p.Mode == "" || p.StartedAtMs <= 0 || p.EffectiveIntervalSeconds() <= 0 {
		return p, false, nil
	}
	return p, true, nil
}

func DVRChapterMaxRangeMs(ctx context.Context, conn foghorndb.DBTX, artifactHash, tenantID string) (int64, error) {
	if conn == nil {
		return 0, sql.ErrConnDone
	}
	q := foghorndb.New(conn)
	var windowSeconds sql.NullInt32
	var err error
	if tenantID != "" {
		windowSeconds, err = q.GetTenantDVRChapterMaxRange(ctx, foghorndb.GetTenantDVRChapterMaxRangeParams{ArtifactHash: artifactHash, TenantID: tenantID})
	} else {
		windowSeconds, err = q.GetDVRChapterMaxRange(ctx, artifactHash)
	}
	if err != nil {
		return 0, err
	}
	if windowSeconds.Valid && windowSeconds.Int32 > 0 {
		return int64(windowSeconds.Int32) * 1000, nil
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

// CurrentChapterBounds computes the [startMs, endMs) of the chapter
// that contains nowMs for the given artifact policy. Returns ok=false
// when the inputs cannot produce a sensible bounded chapter (e.g.
// zero interval for fixed_interval mode).
//
// window_sized_chapters anchors at startedAtMs; fixed_interval anchors
// at unix epoch 0 (UTC, no offset).
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
