package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/hls"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// ReconcileDVRDirectoriesAtStartup brings each local DVR directory's segment
// ledger and on-disk inventory into agreement, classifying every segment
// per the reconciliation matrix:
//
//	ledger          file       action
//	-----------------------------------------------------------------------
//	uploaded            present  no-op
//	uploaded            missing  no-op (S3 authoritative; normal cache eviction)
//	deleted_local       present  no-op (Helmsman ack already recorded)
//	deleted_local       missing  no-op
//	orphan_unreachable  present  delete file + DVRSegmentDropped(was_uploaded=true)
//	                             so the row reconciles to deleted_local and Phase B
//	                             can finish the S3 cleanup
//	orphan_unreachable  missing  no-op (ledger declaration matches reality)
//	pending             present  upload via existing path
//	pending             missing  DVRSegmentDropped(was_uploaded=false) -> lost_local
//	failed_upload       present  retry upload via existing path
//	failed_upload       missing  DVRSegmentDropped(was_uploaded=false) -> lost_local
//	lost_local          present + matching PDT  heal via RecordDVRSegment + upload
//	lost_local          present + no PDT match  log unreconciliable
//	lost_local          missing  no-op
//	(no row)            present + PDT           RecordDVRSegment + upload (rebuild ledger)
//	(no row)        present + no PDT        log unreconciliable
//	(no row)        missing + PDT           RecordDVRSegment then DVRSegmentDropped (tombstone)
//	(no row)        missing + no PDT        log unreconciliable
//
// Critical invariants:
//   - uploaded/deleted_local + missing file is NEVER transitioned to lost_local
//     (would corrupt the model — S3 is authoritative for uploaded; eviction is
//     normal for deleted_local).
//   - All lost_local transitions happen only for rows whose ledger state proves
//     the segment was never uploaded.
//   - Heal and rebuild paths route through RecordDVRSegment, which enforces
//     strict (media_start_ms, media_end_ms, duration_ms) match — a wrong file
//     with the same name cannot corrupt chapter placement.
//
// Disk-driven, not artifact-driven: sidecar reconciles what is local. Foghorn
// has no playlist or PDT and cannot reconstruct local ledger state itself.
//
// Active recordings: a DVR currently being recorded skips reconciliation —
// the active recorder owns the in-memory state and is the canonical source
// of new segment events. Only inactive (post-finalize / on-disk-only) DVRs
// run through the matrix.
func ReconcileDVRDirectoriesAtStartup(ctx context.Context, basePath string, logger logging.Logger) error {
	initDVRManager()
	dvrRoot := filepath.Join(basePath, "dvr")
	streamDirs, err := os.ReadDir(dvrRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, streamDir := range streamDirs {
		if !streamDir.IsDir() {
			continue
		}
		streamName := streamDir.Name()
		artifactDirs, err := os.ReadDir(filepath.Join(dvrRoot, streamName))
		if err != nil {
			continue
		}
		for _, artifactDir := range artifactDirs {
			if !artifactDir.IsDir() {
				continue
			}
			dvrHash := artifactDir.Name()
			if IsActiveDVR(dvrHash) {
				// Active recorder owns the per-segment trigger flow.
				continue
			}
			dvrDir := filepath.Join(dvrRoot, streamName, dvrHash)
			if err := reconcileSingleDVR(ctx, dvrHash, dvrDir, logger); err != nil {
				logger.WithError(err).WithFields(logging.Fields{
					"dvr_hash":    dvrHash,
					"stream_name": streamName,
				}).Warn("DVR startup reconciliation failed; continuing")
			}
		}
	}
	return nil
}

// reconcileSingleDVR runs the matrix against one DVR artifact directory.
func reconcileSingleDVR(ctx context.Context, dvrHash, dvrDir string, logger logging.Logger) error {
	segmentsDir := filepath.Join(dvrDir, "segments")
	manifestPath := filepath.Join(dvrDir, dvrHash+".m3u8")

	// Build playlist entries (segment name -> timing). Missing playlist is
	// not fatal — the union below still considers on-disk and ledger entries.
	type playlistEntry struct {
		hasPDT       bool
		mediaStartMs int64
		mediaEndMs   int64
		durationMs   int64
	}
	playlist := make(map[string]playlistEntry)
	if manifestBody, err := os.ReadFile(manifestPath); err == nil {
		if parsed, perr := hls.Parse(string(manifestBody)); perr == nil && parsed != nil {
			var nextClockMs int64
			for _, seg := range parsed.Segments {
				durationMs := int64(seg.Duration * 1000.0)
				mediaStartMs := seg.ProgramDateTimeMs
				if mediaStartMs <= 0 && nextClockMs > 0 {
					mediaStartMs = nextClockMs
				}
				if mediaStartMs > 0 {
					nextClockMs = mediaStartMs + durationMs
				}
				pe := playlistEntry{durationMs: durationMs}
				if mediaStartMs > 0 {
					pe.hasPDT = true
					pe.mediaStartMs = mediaStartMs
					pe.mediaEndMs = mediaStartMs + durationMs
				}
				playlist[seg.Name] = pe
			}
		}
	}

	// On-disk segment inventory.
	type diskEntry struct {
		present bool
		path    string
		size    int64
	}
	disk := make(map[string]diskEntry)
	if segEntries, err := os.ReadDir(segmentsDir); err == nil {
		for _, e := range segEntries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			disk[e.Name()] = diskEntry{present: true, path: filepath.Join(segmentsDir, e.Name()), size: info.Size()}
		}
	}

	// Build union of names to consider.
	names := make([]string, 0, len(disk)+len(playlist))
	seen := make(map[string]bool, len(disk)+len(playlist))
	for n := range disk {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for n := range playlist {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}

	// Ask Foghorn for current ledger state, batched.
	ledger := make(map[string]*pb.DVRSegmentRef, len(names))
	const batchSize = 500
	for i := 0; i < len(names); i += batchSize {
		end := i + batchSize
		if end > len(names) {
			end = len(names)
		}
		resp, err := SendRestoreLocalSegmentIndex(ctx, dvrHash, names[i:end])
		if err != nil {
			return fmt.Errorf("restore segment index: %w", err)
		}
		for _, s := range resp.GetSegments() {
			ledger[s.GetSegmentName()] = s
		}
	}

	// Walk the matrix.
	for _, name := range names {
		d := disk[name]
		pe, pePresent := playlist[name]
		lr := ledger[name]
		status := ""
		if lr != nil {
			status = lr.GetStatus()
		}

		switch status {
		case "uploaded", "deleted_local":
			// uploaded: S3 is authoritative; no transition.
			// deleted_local: Helmsman previously acknowledged the local
			// delete. No further action needed at startup.
			continue
		case "orphan_unreachable":
			// Foghorn declared the local file presumed-gone while this
			// node was absent past the chapter-reclaim grace. We've now
			// rejoined and the file may or may not still be on disk. If
			// it is, reconcile to the ledger by deleting + emitting
			// DVRSegmentDropped(was_uploaded=true) so the row flips to
			// deleted_local (Helmsman ack landed) and Phase B can finish
			// the S3 cleanup. If the file is gone, the ledger declaration
			// already matches reality.
			if d.present {
				if err := os.Remove(d.path); err != nil && !os.IsNotExist(err) {
					logger.WithError(err).WithFields(logging.Fields{
						"dvr_hash": dvrHash,
						"segment":  name,
					}).Warn("Startup reconcile: failed to delete orphan_unreachable segment file")
					continue
				}
				if dropErr := SendDVRSegmentDropped(dvrHash, name, "orphan_reconciled",
					d.path, lr.GetMediaStartMs(), lr.GetMediaEndMs(), lr.GetDurationMs(), uint64(d.size), true); dropErr != nil {
					logger.WithError(dropErr).WithFields(logging.Fields{
						"dvr_hash": dvrHash, "segment": name,
					}).Warn("Startup reconcile: DVRSegmentDropped(orphan_reconciled) emit failed")
				}
			}
			continue
		case "pending", "failed_upload":
			if d.present {
				// Try the upload path. RecordDVRSegment will reuse the
				// existing sequence (strict timing match) and the upload
				// can proceed.
				reconcileUploadExisting(ctx, dvrHash, name, d.path, d.size, lr, logger)
			} else {
				// Pre-upload data loss → lost_local.
				if err := SendDVRSegmentDropped(dvrHash, name, "missing_pre_upload", "",
					lr.GetMediaStartMs(), lr.GetMediaEndMs(), lr.GetDurationMs(), 0, false); err != nil {
					logger.WithError(err).WithFields(logging.Fields{
						"dvr_hash": dvrHash, "segment": name,
					}).Warn("Startup reconcile: DVRSegmentDropped(lost_local) emit failed")
				}
			}
		case "lost_local":
			if !d.present {
				continue
			}
			// Heal only if timing matches. The RecordDVRSegment path is the
			// only sanctioned entry — InsertDVRSegment validates timing
			// before flipping lost_local -> pending.
			if !pePresent || !pe.hasPDT {
				logger.WithFields(logging.Fields{
					"dvr_hash": dvrHash,
					"segment":  name,
				}).Warn("Cannot heal lost_local segment: no playlist PDT timing")
				continue
			}
			if pe.mediaStartMs != lr.GetMediaStartMs() ||
				pe.mediaEndMs != lr.GetMediaEndMs() ||
				pe.durationMs != lr.GetDurationMs() {
				logger.WithFields(logging.Fields{
					"dvr_hash": dvrHash,
					"segment":  name,
				}).Warn("Cannot heal lost_local segment: playlist timing does not match ledger row")
				continue
			}
			reconcileHealAndUpload(ctx, dvrHash, name, d.path, d.size, pe.mediaStartMs, pe.mediaEndMs, pe.durationMs, logger)
		default:
			// No ledger row.
			if d.present {
				if !pePresent || !pe.hasPDT {
					logger.WithFields(logging.Fields{
						"dvr_hash": dvrHash,
						"segment":  name,
					}).Warn("Untracked segment file with no playlist PDT timing; skipping (cannot fabricate)")
					continue
				}
				reconcileInsertAndUpload(ctx, dvrHash, name, d.path, d.size, pe.mediaStartMs, pe.mediaEndMs, pe.durationMs, logger)
			} else {
				// File missing AND no ledger row. Only tombstone if the
				// playlist gives us trustworthy timing.
				if !pePresent || !pe.hasPDT {
					logger.WithFields(logging.Fields{
						"dvr_hash": dvrHash,
						"segment":  name,
					}).Warn("Missing segment with no playlist PDT timing; cannot create tombstone (skipping)")
					continue
				}
				reconcileInsertAndDrop(ctx, dvrHash, name, pe.mediaStartMs, pe.mediaEndMs, pe.durationMs, logger)
			}
		}
	}
	return nil
}

// reconcileUploadExisting drives the upload for a ledger row already in
// pending/failed_upload. Foghorn issues a fresh presigned URL.
func reconcileUploadExisting(ctx context.Context, dvrHash, segmentName, localPath string, size int64, lr *pb.DVRSegmentRef, logger logging.Logger) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dm := GetDVRManager()
	if dm == nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: DVR uploader unavailable; leaving segment retryable")
		return
	}
	resp, err := RecordDVRSegment(reqCtx, dvrHash, segmentName, localPath,
		lr.GetMediaStartMs(), lr.GetMediaEndMs(), lr.GetDurationMs())
	if err != nil || resp == nil || !resp.GetAccepted() || resp.GetPresignedPutUrl() == "" {
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"dvr_hash": dvrHash, "segment": segmentName,
			}).Warn("Startup reconcile: RecordDVRSegment failed for retry")
		} else if resp != nil && !resp.GetAccepted() {
			logger.WithFields(logging.Fields{
				"dvr_hash": dvrHash, "segment": segmentName, "reason": resp.GetReason(),
			}).Warn("Startup reconcile: RecordDVRSegment rejected for retry")
		}
		return
	}
	if upErr := dm.uploadSegmentToS3(reqCtx, localPath, resp.GetPresignedPutUrl()); upErr != nil {
		logger.WithError(upErr).WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: upload failed; leaving retryable")
		return
	}
	if markErr := SendMarkDVRSegmentUploaded(dvrHash, segmentName, uint64(size)); markErr != nil {
		logger.WithError(markErr).WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: MarkDVRSegmentUploaded failed")
	}
}

// reconcileHealAndUpload heals a lost_local row (via timing-validated
// RecordDVRSegment) and uploads the reappeared file.
func reconcileHealAndUpload(ctx context.Context, dvrHash, segmentName, localPath string, size int64, mediaStartMs, mediaEndMs, durationMs int64, logger logging.Logger) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dm := GetDVRManager()
	if dm == nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: DVR uploader unavailable; leaving lost_local segment unchanged")
		return
	}
	resp, err := RecordRecoveredDVRSegment(reqCtx, dvrHash, segmentName, localPath, mediaStartMs, mediaEndMs, durationMs)
	if err != nil || resp == nil || !resp.GetAccepted() {
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"dvr_hash": dvrHash, "segment": segmentName,
			}).Warn("Startup reconcile: heal RecordDVRSegment failed")
			return
		}
		if resp != nil {
			reason := resp.GetReason()
			if reason == "timing_mismatch" {
				logger.WithFields(logging.Fields{
					"dvr_hash": dvrHash, "segment": segmentName,
				}).Warn("Startup reconcile: cannot heal lost_local — timing mismatch (logged as unreconciliable)")
			} else {
				logger.WithFields(logging.Fields{
					"dvr_hash": dvrHash, "segment": segmentName, "reason": reason,
				}).Warn("Startup reconcile: heal RecordDVRSegment rejected")
			}
		}
		return
	}
	if resp.GetPresignedPutUrl() == "" {
		return
	}
	if upErr := dm.uploadSegmentToS3(reqCtx, localPath, resp.GetPresignedPutUrl()); upErr != nil {
		logger.WithError(upErr).WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: heal upload failed")
		return
	}
	if markErr := SendMarkDVRSegmentUploaded(dvrHash, segmentName, uint64(size)); markErr != nil {
		logger.WithError(markErr).WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: heal MarkDVRSegmentUploaded failed")
	} else {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Info("Startup reconcile: healed lost_local segment with reappeared file")
	}
}

// reconcileInsertAndUpload creates a new ledger row for an untracked file
// found on disk, then uploads.
func reconcileInsertAndUpload(ctx context.Context, dvrHash, segmentName, localPath string, size int64, mediaStartMs, mediaEndMs, durationMs int64, logger logging.Logger) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dm := GetDVRManager()
	if dm == nil {
		logger.WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: DVR uploader unavailable; skipping untracked segment")
		return
	}
	resp, err := RecordRecoveredDVRSegment(reqCtx, dvrHash, segmentName, localPath, mediaStartMs, mediaEndMs, durationMs)
	if err != nil || resp == nil || !resp.GetAccepted() || resp.GetPresignedPutUrl() == "" {
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			logger.WithError(err).WithFields(logging.Fields{
				"dvr_hash": dvrHash, "segment": segmentName,
			}).Warn("Startup reconcile: insert RecordDVRSegment failed")
		}
		return
	}
	if upErr := dm.uploadSegmentToS3(reqCtx, localPath, resp.GetPresignedPutUrl()); upErr != nil {
		logger.WithError(upErr).WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: new-row upload failed")
		return
	}
	if markErr := SendMarkDVRSegmentUploaded(dvrHash, segmentName, uint64(size)); markErr != nil {
		logger.WithError(markErr).WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: insert-and-upload MarkDVRSegmentUploaded failed")
	}
}

// reconcileInsertAndDrop creates a 'pending' ledger row for a missing file
// (manifest knew about it; on-disk gone) and immediately marks it
// lost_local. Any chapter whose range overlaps a lost_local row without
// a successful S3 upload will fail finalization with state=
// failed_source_missing.
func reconcileInsertAndDrop(ctx context.Context, dvrHash, segmentName string, mediaStartMs, mediaEndMs, durationMs int64, logger logging.Logger) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := RecordRecoveredDVRSegment(reqCtx, dvrHash, segmentName, "", mediaStartMs, mediaEndMs, durationMs)
	if err != nil || resp == nil || !resp.GetAccepted() {
		if err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"dvr_hash": dvrHash, "segment": segmentName,
			}).Warn("Startup reconcile: tombstone RecordDVRSegment failed")
		}
		return
	}
	if dropErr := SendDVRSegmentDropped(dvrHash, segmentName, "missing_at_startup", "",
		mediaStartMs, mediaEndMs, durationMs, 0, false); dropErr != nil {
		logger.WithError(dropErr).WithFields(logging.Fields{
			"dvr_hash": dvrHash, "segment": segmentName,
		}).Warn("Startup reconcile: tombstone DVRSegmentDropped failed")
	}
}
