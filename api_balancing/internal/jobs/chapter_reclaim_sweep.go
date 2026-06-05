package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/lib/pq"
)

// CHAPTER RECLAIM SWEEP
// Walks chapters in state='frozen' (artifact + .dtsh both synced to S3)
// and reclaims their underlying source segments. A segment is reclaim-
// eligible iff every overlapping chapter is in {frozen, reclaimed} —
// i.e. no other chapter still needs the bytes for playback or
// finalization.
//
// Two side effects per eligible segment:
//   1. Helmsman deletes the local TS file (idempotent).
//   2. Foghorn deletes the recovery-bridge S3 object directly.
//
// Once all segments in a chapter's range are reclaimed (or were
// already lost), the chapter transitions to state='reclaimed' — the
// row stays as range metadata; playback uses the chapter artifact.
//
// reclaim_started_at gates concurrent workers so a tick can't issue
// duplicate Helmsman orders.

const (
	chapterReclaimDefaultTick  = 1 * time.Minute
	chapterReclaimBatchMax     = 20
	chapterReclaimFreshness    = 5 * time.Minute
	chapterReclaimPerArtifact  = 30 * time.Second
	chapterReclaimSegmentBatch = 100
	// How long a chapter must have existed without a live recording
	// node before the reclaim sweep gives up on getting a Helmsman ack
	// for Phase A and marks segments orphan_unreachable so Phase B
	// (S3 delete) can run. Bounded above by how long a brief node
	// restart could plausibly take so we don't presume a node gone
	// while it's just rebooting.
	chapterReclaimAbandonNodeGrace = 4 * time.Hour
)

type ChapterReclaimSweepConfig struct {
	DB       *sql.DB
	Logger   logging.Logger
	S3Delete S3SegmentDeleter
	Interval time.Duration
}

// S3SegmentDeleter abstracts the recovery-bridge S3 object deletion.
// Implementations are expected to be idempotent (NotFound == success).
type S3SegmentDeleter interface {
	Delete(ctx context.Context, key string) error
}

type ChapterReclaimSweep struct {
	db       *sql.DB
	logger   logging.Logger
	s3       S3SegmentDeleter
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func NewChapterReclaimSweep(cfg ChapterReclaimSweepConfig) *ChapterReclaimSweep {
	interval := cfg.Interval
	if interval == 0 {
		interval = chapterReclaimDefaultTick
	}
	return &ChapterReclaimSweep{
		db:       cfg.DB,
		logger:   cfg.Logger,
		s3:       cfg.S3Delete,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

func (s *ChapterReclaimSweep) Start() {
	s.wg.Add(1)
	go s.run()
	s.logger.WithField("interval_seconds", int(s.interval.Seconds())).Info("Chapter reclaim sweep started")
}

func (s *ChapterReclaimSweep) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	s.logger.Info("Chapter reclaim sweep stopped")
}

func (s *ChapterReclaimSweep) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	firstTick := time.NewTimer(45 * time.Second)
	defer firstTick.Stop()

	for {
		select {
		case <-firstTick.C:
			s.tick()
		case <-ticker.C:
			s.tick()
		case <-s.stopCh:
			return
		}
	}
}

func (s *ChapterReclaimSweep) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	chapters, err := control.ListChaptersNeedingReclaim(ctx, chapterReclaimBatchMax, chapterReclaimFreshness)
	if err != nil {
		s.logger.WithError(err).Warn("Chapter reclaim sweep: list failed")
		return
	}
	for _, c := range chapters {
		chCtx, chCancel := context.WithTimeout(ctx, chapterReclaimPerArtifact)
		if err := control.WithDVRChapterMutationLock(chCtx, c.ArtifactHash, func() error {
			return s.reclaimChapter(chCtx, c)
		}); err != nil {
			s.logger.WithError(err).WithField("chapter_id", c.ChapterID).Warn("Chapter reclaim failed")
		}
		chCancel()
	}

	// Catch finalized chapters that never flipped dtsh_synced. The
	// Helmsman-side retry goroutine survives only that process; if
	// Helmsman restarted between finalize and DTSH success, the
	// chapter stays in 'finalized' indefinitely. Foghorn re-triggers
	// here so the freeze pipeline runs even across restarts. Helmsman's
	// vod SyncDtshOnly branch regenerates the sidecar on demand when
	// it isn't already on disk.
	s.retryFinalizedChapterDTSH(ctx)
}

func (s *ChapterReclaimSweep) retryFinalizedChapterDTSH(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.playback_artifact_hash, COALESCE(an.node_id, '')
		  FROM foghorn.dvr_chapters c
		  JOIN foghorn.artifacts a ON a.artifact_hash = c.playback_artifact_hash
		  LEFT JOIN foghorn.artifact_nodes an
		         ON an.artifact_hash = c.playback_artifact_hash
		        AND an.is_orphaned = false
		 WHERE c.state = 'finalized'
		   AND c.finalize_started_at IS NOT NULL
		   AND c.finalize_started_at < NOW() - INTERVAL '5 minutes'
		   AND a.origin_type = 'dvr_chapter'
		   AND COALESCE(a.dtsh_synced, false) = false
		 LIMIT 50
	`)
	if err != nil {
		s.logger.WithError(err).Warn("Chapter DTSH retry: list failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var artifactHash, nodeID string
		if err := rows.Scan(&artifactHash, &nodeID); err != nil {
			s.logger.WithError(err).Warn("Chapter DTSH retry: scan failed")
			continue
		}
		if nodeID == "" {
			continue
		}
		filePath := fmt.Sprintf("vod/%s.mkv", artifactHash)
		control.TriggerDtshSync(nodeID, artifactHash, "vod", filePath)
	}
	if err := rows.Err(); err != nil {
		s.logger.WithError(err).Warn("Chapter DTSH retry: iteration failed")
	}
}

// reclaimChapter runs the two-phase reclaim contract for one chapter:
//
//	Phase A — Helmsman side. Send ReclaimDVRSegment for each segment
//	          still 'uploaded'/'pending'. Helmsman deletes the local
//	          TS file and emits DVRSegmentDropped(was_uploaded=true),
//	          which transitions the ledger row to 'deleted_local'
//	          through the existing handler. No S3 or ledger writes
//	          from this sweep yet.
//
//	Phase B — Foghorn side. For segments now 'deleted_local' (i.e.
//	          Helmsman's local-delete acknowledgement landed), delete
//	          the recovery-bridge S3 object and transition the row to
//	          'reclaimed'. This step only proceeds AFTER Phase A acks,
//	          so the contract "delete both local and S3" is preserved
//	          even if Helmsman goes away mid-sweep.
//
// When all segments overlapping the chapter are 'reclaimed' or
// terminally lost after recovery, the chapter advances to
// state='reclaimed'.
func (s *ChapterReclaimSweep) reclaimChapter(ctx context.Context, c control.DVRChapterRow) error {
	started, err := control.MarkChapterReclaimStarted(ctx, c.ChapterID, chapterReclaimFreshness)
	if err != nil {
		return err
	}
	if !started {
		return nil
	}

	parentNode, err := s.readRecordingNode(ctx, c.ArtifactHash)
	if err != nil {
		return err
	}

	// Phase A: Helmsman-side local delete. With a live recording node
	// we send the Helmsman a delete order and let DVRSegmentDropped
	// transition the ledger row to deleted_local. Without one we mark
	// the segment orphan_unreachable Foghorn-side (distinct authority
	// from deleted_local) so Phase B can still clean up the S3 temp
	// objects; startup reconcile reconciles the disk on node rejoin.
	// Orphan-unreachable only kicks in once the chapter has been frozen
	// long enough that a brief node restart would have come back.
	if parentNode != "" {
		pending, listErr := s.listSegmentsAwaitingLocalDelete(ctx, c.ArtifactHash, c.StartMs, c.EndMs)
		if listErr != nil {
			return listErr
		}
		for i := 0; i < len(pending); i += chapterReclaimSegmentBatch {
			end := i + chapterReclaimSegmentBatch
			if end > len(pending) {
				end = len(pending)
			}
			batch := pending[i:end]
			names := make([]string, 0, len(batch))
			for _, seg := range batch {
				names = append(names, seg.name)
			}
			req := &ipcpb.ReclaimDVRSegment{
				RequestId:    fmt.Sprintf("chapter-reclaim-%s-%d", c.ChapterID, i),
				DvrHash:      c.ArtifactHash,
				SegmentNames: names,
			}
			if sendErr := control.SendReclaimDVRSegment(parentNode, req); sendErr != nil {
				s.logger.WithError(sendErr).WithFields(logging.Fields{
					"dvr_hash":  c.ArtifactHash,
					"node_id":   parentNode,
					"batch_len": len(names),
				}).Warn("Chapter reclaim: Helmsman send failed; will retry on next tick")
				return nil
			}
		}
	} else {
		// Recording node is gone. Wait until the chapter has been
		// FROZEN long enough that the node is presumed lost (not just
		// briefly restarting). frozen_at is the right anchor: a
		// long-running chapter that froze 30 seconds ago shouldn't
		// skip Phase A, and a short chapter that froze hours ago
		// shouldn't keep waiting. Once past the grace period, mark
		// the non-terminal overlapping segments deleted_local
		// directly so Phase B can clean up their S3 recovery objects.
		anchor := c.FrozenAt.Time
		if !c.FrozenAt.Valid {
			anchor = c.CreatedAt
		}
		if time.Since(anchor) < chapterReclaimAbandonNodeGrace {
			s.logger.WithFields(logging.Fields{
				"dvr_hash":      c.ArtifactHash,
				"chapter_id":    c.ChapterID,
				"grace_minutes": int(chapterReclaimAbandonNodeGrace.Minutes()),
			}).Debug("Chapter reclaim: no recording node, still within grace; awaiting next tick")
			return nil
		}
		pending, listErr := s.listSegmentsAwaitingLocalDelete(ctx, c.ArtifactHash, c.StartMs, c.EndMs)
		if listErr != nil {
			return listErr
		}
		if len(pending) > 0 {
			s.logger.WithFields(logging.Fields{
				"dvr_hash":   c.ArtifactHash,
				"chapter_id": c.ChapterID,
				"orphaned":   len(pending),
			}).Warn("Chapter reclaim: recording node gone past grace; marking segments orphan_unreachable")
		}
		for _, seg := range pending {
			if upErr := s.markSegmentOrphanUnreachable(ctx, c.ArtifactHash, seg.name); upErr != nil {
				s.logger.WithError(upErr).WithField("segment_name", seg.name).Warn("Chapter reclaim: orphan-unreachable ledger update failed")
			}
		}
	}

	// Phase B: delete the S3 recovery objects for segments whose local
	// file is no longer authoritative — deleted_local (Helmsman acked
	// the delete via DVRSegmentDropped) or orphan_unreachable (Foghorn
	// presumed the node gone past grace). Both states transition to
	// 'reclaimed' once the S3 object is gone.
	acked, err := s.listSegmentsAwaitingS3Delete(ctx, c.ArtifactHash, c.StartMs, c.EndMs)
	if err != nil {
		return err
	}
	for _, seg := range acked {
		if seg.s3Key != "" && s.s3 != nil {
			if delErr := s.s3.Delete(ctx, seg.s3Key); delErr != nil {
				s.logger.WithError(delErr).WithField("s3_key", seg.s3Key).Warn("Chapter reclaim: S3 delete failed; will retry")
				continue
			}
		}
		if upErr := s.markSegmentReclaimed(ctx, c.ArtifactHash, seg.name); upErr != nil {
			s.logger.WithError(upErr).WithField("segment_name", seg.name).Warn("Chapter reclaim: ledger update failed")
		}
	}

	return s.markReclaimedIfRangeComplete(ctx, c)
}

type evictableSegment struct {
	name  string
	s3Key string
}

// listSegmentsAwaitingLocalDelete returns segments overlapping
// [startMs, endMs) that:
//   - are still uploaded/pending (Helmsman hasn't ack'd a local delete), AND
//   - every overlapping chapter is in {frozen, reclaimed}.
//
// Phase A targets these — Foghorn sends ReclaimDVRSegment and waits
// for Helmsman's DVRSegmentDropped ack before touching S3 or ledger.
func (s *ChapterReclaimSweep) listSegmentsAwaitingLocalDelete(ctx context.Context, dvrHash string, startMs, endMs int64) ([]evictableSegment, error) {
	return s.querySegmentsByStatus(ctx, dvrHash, startMs, endMs, []string{"uploaded", "pending", "failed_upload"})
}

// listSegmentsAwaitingS3Delete returns segments overlapping
// [startMs, endMs) where the local file is no longer authoritative:
// either Helmsman acknowledged the delete (deleted_local), Foghorn
// presumed the node gone past grace (orphan_unreachable), or the local
// file was already lost before finalization (lost_local). Phase B deletes
// the recovery-bridge S3 object for each and transitions the row to
// 'reclaimed'. lost_local rows have no Phase A work because there is no
// local file left for Helmsman to delete.
func (s *ChapterReclaimSweep) listSegmentsAwaitingS3Delete(ctx context.Context, dvrHash string, startMs, endMs int64) ([]evictableSegment, error) {
	return s.querySegmentsByStatus(ctx, dvrHash, startMs, endMs, []string{"deleted_local", "orphan_unreachable", "lost_local"})
}

func (s *ChapterReclaimSweep) querySegmentsByStatus(ctx context.Context, dvrHash string, startMs, endMs int64, statuses []string) ([]evictableSegment, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH overlapping AS (
			SELECT s.segment_name,
			       s.s3_key,
			       s.status,
			       BOOL_AND(c.state IN ('frozen', 'reclaimed')) AS all_done
			  FROM foghorn.dvr_segments s
			  JOIN foghorn.dvr_chapters c
			    ON c.artifact_hash = s.artifact_hash
			   AND c.start_ms < s.media_end_ms
			   AND c.end_ms   > s.media_start_ms
			 WHERE s.artifact_hash = $1
			   AND s.media_start_ms < $3
			   AND s.media_end_ms   > $2
			   AND s.status = ANY($4)
			 GROUP BY s.segment_name, s.s3_key, s.status
		)
		SELECT segment_name, COALESCE(s3_key, '')
		  FROM overlapping
		 WHERE all_done = true
	`, dvrHash, startMs, endMs, pq.Array(statuses))
	if err != nil {
		return nil, fmt.Errorf("list segments by status %v: %w", statuses, err)
	}
	defer rows.Close()
	var out []evictableSegment
	for rows.Next() {
		var seg evictableSegment
		if err := rows.Scan(&seg.name, &seg.s3Key); err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, rows.Err()
}

func (s *ChapterReclaimSweep) markSegmentReclaimed(ctx context.Context, dvrHash, segmentName string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE foghorn.dvr_segments
		   SET status = 'reclaimed',
		       deleted_local_at = COALESCE(deleted_local_at, NOW())
		 WHERE artifact_hash = $1 AND segment_name = $2
	`, dvrHash, segmentName)
	return err
}

// markSegmentOrphanUnreachable is the recording-node-abandoned escape
// hatch: Foghorn marks segments orphan_unreachable when the node hosting
// the local TS file is gone past the chapter reclaim grace. The state
// is distinct from deleted_local — deleted_local means Helmsman
// acknowledged the local delete via DVRSegmentDropped — so the two
// authorities don't conflate. Phase B accepts both for S3 delete and
// the ledger row eventually flips to 'reclaimed' once S3 is gone.
// Startup reconcile sees orphan_unreachable + present file when the
// node rejoins and reconciles the disk to the ledger declaration.
//
// Restricted to non-terminal source-side statuses so we never re-touch
// already-resolved rows.
func (s *ChapterReclaimSweep) markSegmentOrphanUnreachable(ctx context.Context, dvrHash, segmentName string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE foghorn.dvr_segments
		   SET status = 'orphan_unreachable',
		       deleted_local_at = NOW()
		 WHERE artifact_hash = $1
		   AND segment_name = $2
		   AND status IN ('pending', 'uploaded', 'failed_upload')
	`, dvrHash, segmentName)
	return err
}

func (s *ChapterReclaimSweep) markReclaimedIfRangeComplete(ctx context.Context, c control.DVRChapterRow) error {
	var pending int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM foghorn.dvr_segments
		 WHERE artifact_hash = $1
		   AND media_start_ms < $3
		   AND media_end_ms   > $2
		   AND status != 'reclaimed'
	`, c.ArtifactHash, c.StartMs, c.EndMs).Scan(&pending)
	if err != nil {
		return err
	}
	if pending > 0 {
		// Helmsman ack-by-action: the next tick reads the updated rows.
		return nil
	}
	return control.MarkChapterReclaimed(ctx, c.ChapterID)
}

// readRecordingNode returns the node currently hosting the recording's
// local TS segments. Returns "" when the latest non-orphaned row is for
// a node that is no longer alive — the caller's abandon-grace branch
// then takes over so a stale artifact_nodes row can never wedge reclaim.
func (s *ChapterReclaimSweep) readRecordingNode(ctx context.Context, dvrHash string) (string, error) {
	var nodeID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(node_id, '')
		  FROM foghorn.artifact_nodes
		 WHERE artifact_hash = $1
		   AND is_orphaned = false
		 ORDER BY last_seen_at DESC NULLS LAST
		 LIMIT 1
	`, dvrHash).Scan(&nodeID); err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if nodeID == "" {
		return "", nil
	}
	if !nodeAlive(nodeID) {
		return "", nil
	}
	return nodeID, nil
}

func nodeAlive(nodeID string) bool {
	sm := state.DefaultManager()
	if sm == nil {
		return false
	}
	for _, id := range sm.AliveNodeIDs(60 * time.Second) {
		if id == nodeID {
			return true
		}
	}
	return false
}
