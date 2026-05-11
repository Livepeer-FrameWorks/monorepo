package control

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DVR PER-SEGMENT LEDGER — control-stream handlers (Foghorn side)
// The sidecar reports each Mist RECORDING_SEGMENT to Foghorn before
// uploading to S3. Foghorn assigns a monotonic sequence, mints a presigned
// PUT URL, and persists a 'pending' row in foghorn.dvr_segments. After the
// upload completes the sidecar reports MarkDVRSegmentUploaded, transitioning
// the row to 'uploaded'. Forced evictions report DVRSegmentDropped — those
// transition the row to deleted_local (was_uploaded=true) or lost_local
// (was_uploaded=false). Lost rows render as #EXT-X-GAP in chapter manifests.

// presignTTL is how long an issued presigned PUT URL is valid. The sidecar
// uploads immediately on receipt; allowing a generous TTL covers transient
// retries without re-asking Foghorn for a fresh URL.
const presignTTL = 15 * time.Minute

// processRecordDVRSegment handles RecordDVRSegmentRequest from the sidecar.
// Resolves the parent DVR's tenant + stream so the S3 prefix can be built,
// inserts a 'pending' ledger row (Foghorn-assigned sequence), mints a
// presigned PUT URL, and replies with the URL + s3_key + sequence.
func processRecordDVRSegment(
	req *pb.RecordDVRSegmentRequest,
	nodeID string,
	stream pb.HelmsmanControl_ConnectServer,
	logger logging.Logger,
) {
	requestID := req.GetRequestId()
	dvrHash := req.GetDvrHash()
	segmentName := req.GetSegmentName()

	if dvrHash == "" || segmentName == "" {
		sendRecordDVRSegmentResponse(stream, &pb.RecordDVRSegmentResponse{
			RequestId:   requestID,
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			Accepted:    false,
			Reason:      "missing_dvr_hash_or_segment_name",
		}, logger)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tenantID, streamName, ok := resolveDVRTenantAndStream(ctx, dvrHash, logger)
	if !ok {
		sendRecordDVRSegmentResponse(stream, &pb.RecordDVRSegmentResponse{
			RequestId:   requestID,
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			Accepted:    false,
			Reason:      "dvr_artifact_not_found",
		}, logger)
		return
	}

	if s3Client == nil {
		sendRecordDVRSegmentResponse(stream, &pb.RecordDVRSegmentResponse{
			RequestId:   requestID,
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			Accepted:    false,
			Reason:      "s3_client_unavailable",
		}, logger)
		return
	}
	s3Prefix := s3Client.BuildDVRS3Key(tenantID, streamName, dvrHash)
	s3Key := s3Prefix + "/segments/" + segmentName

	// Insert ledger row first so eviction-decision queries see the segment
	// even if the upload itself stalls or fails. Live trigger writes reject
	// terminal DVRs; startup reconciliation can opt in only after recovering
	// timing from a local DVR manifest.
	sequence, err := InsertDVRSegment(
		ctx,
		dvrHash, segmentName, s3Key,
		req.GetMediaStartMs(), req.GetMediaEndMs(), req.GetDurationMs(),
		req.GetRecoveryInsert(),
	)
	if err != nil {
		reason := "insert_failed"
		switch {
		case errors.Is(err, ErrDVRSegmentTerminal):
			reason = "dvr_terminal"
		case errors.Is(err, ErrDVRSegmentTimingMismatch):
			// Wrong file with the same name. Refuse to corrupt chapter
			// placement. Sidecar logs unreconciliable and leaves the ledger
			// alone.
			reason = "timing_mismatch"
			logger.WithFields(logging.Fields{
				"dvr_hash":       dvrHash,
				"segment_name":   segmentName,
				"node_id":        nodeID,
				"media_start_ms": req.GetMediaStartMs(),
				"media_end_ms":   req.GetMediaEndMs(),
				"duration_ms":    req.GetDurationMs(),
			}).Warn("Refused DVR segment record: timing does not match ledger row")
		default:
			logger.WithError(err).WithFields(logging.Fields{
				"dvr_hash":     dvrHash,
				"segment_name": segmentName,
				"node_id":      nodeID,
			}).Error("Failed to insert DVR segment ledger row")
		}
		sendRecordDVRSegmentResponse(stream, &pb.RecordDVRSegmentResponse{
			RequestId:   requestID,
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			Accepted:    false,
			Reason:      reason,
		}, logger)
		return
	}

	presignedURL, err := s3Client.GeneratePresignedPUT(s3Key, presignTTL)
	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
		}).Error("Failed to generate presigned PUT URL for DVR segment")
		sendRecordDVRSegmentResponse(stream, &pb.RecordDVRSegmentResponse{
			RequestId:   requestID,
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			Accepted:    false,
			Reason:      "presign_failed",
		}, logger)
		return
	}

	sendRecordDVRSegmentResponse(stream, &pb.RecordDVRSegmentResponse{
		RequestId:        requestID,
		DvrHash:          dvrHash,
		SegmentName:      segmentName,
		Accepted:         true,
		Sequence:         sequence,
		S3Key:            s3Key,
		PresignedPutUrl:  presignedURL,
		UrlExpirySeconds: int64(presignTTL.Seconds()),
	}, logger)
}

// processMarkDVRSegmentUploaded transitions a ledger row to 'uploaded' after
// the sidecar's S3 PUT confirmed.
func processMarkDVRSegmentUploaded(req *pb.MarkDVRSegmentUploaded, nodeID string, logger logging.Logger) {
	dvrHash := req.GetDvrHash()
	segmentName := req.GetSegmentName()
	if dvrHash == "" || segmentName == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := MarkDVRSegmentUploaded(ctx, dvrHash, segmentName, int64(req.GetSizeBytes())); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"node_id":      nodeID,
		}).Error("Failed to mark DVR segment uploaded")
	}
}

// processDVRSegmentDropped records a sidecar-reported eviction. was_uploaded
// distinguishes safe local cleanup (deleted_local; chapter manifests remain
// playable) from data loss (lost_local; chapter manifests emit #EXT-X-GAP).
func processDVRSegmentDropped(req *pb.DVRSegmentDropped, nodeID string, logger logging.Logger) {
	dvrHash := req.GetDvrHash()
	segmentName := req.GetSegmentName()
	if dvrHash == "" || segmentName == "" {
		return
	}
	reason := req.GetReason()
	if reason == "" {
		reason = "unspecified"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := MarkDVRSegmentDropped(
		ctx, dvrHash, segmentName, reason, req.GetWasUploaded(),
		req.GetMediaStartMs(), req.GetMediaEndMs(), req.GetDurationMs(),
		int64(req.GetSizeBytes()),
	); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"reason":       reason,
			"node_id":      nodeID,
		}).Error("Failed to mark DVR segment dropped")
	}
	if !req.GetWasUploaded() {
		// lost_local is the data-loss case; surface at warn so ops sees it.
		logger.WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"reason":       reason,
			"duration_ms":  req.GetDurationMs(),
			"node_id":      nodeID,
		}).Warn("DVR segment lost before S3 upload; chapter manifest will include #EXT-X-GAP")
		// Invalidate any materialized chapters overlapping this segment so
		// the next chapter-sweep tick re-materializes them with #EXT-X-GAP.
		// Bounded: at most a handful of chapters overlap any single segment
		// for any sane chapter mode; index-backed via
		// idx_foghorn_dvr_chapters_overlap.
		if affected, flagErr := FlagChaptersOverlappingSegment(ctx, dvrHash, req.GetMediaStartMs(), req.GetMediaEndMs()); flagErr != nil {
			logger.WithError(flagErr).WithField("dvr_hash", dvrHash).Warn("Failed to flag overlapping chapters for has_gaps invalidation")
		} else if affected > 0 {
			logger.WithFields(logging.Fields{
				"dvr_hash":          dvrHash,
				"chapters_affected": affected,
			}).Info("Flagged chapters has_gaps=true for re-materialization")
		}
	}
}

// processEvictableSegmentsRequest answers an authoritative "which segments
// are safe to evict" query during sidecar storage-pressure passes.
func processEvictableSegmentsRequest(
	req *pb.EvictableSegmentsRequest,
	nodeID string,
	stream pb.HelmsmanControl_ConnectServer,
	logger logging.Logger,
) {
	requestID := req.GetRequestId()
	dvrHash := req.GetDvrHash()
	if dvrHash == "" {
		sendEvictableSegmentsResponse(stream, &pb.EvictableSegmentsResponse{
			RequestId: requestID,
			DvrHash:   dvrHash,
		}, logger)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Effective window comes from dvr_window_seconds stamped on the artifact
	// at DVR start. Retention is post-end only and must not influence local
	// active-recording eviction. The final answer is clamped to uploaded rows
	// older than now-window, so the worst case is "evicts nothing".
	windowSeconds := int(4 * time.Hour / time.Second)
	if w := dvrEffectiveWindowSeconds(ctx, dvrHash); w > 0 {
		windowSeconds = w
	}

	names, err := ListEvictableDVRSegments(ctx, dvrHash, windowSeconds, int(req.GetMaxCount()))
	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"node_id":  nodeID,
		}).Error("Failed to list evictable DVR segments")
		sendEvictableSegmentsResponse(stream, &pb.EvictableSegmentsResponse{
			RequestId: requestID,
			DvrHash:   dvrHash,
		}, logger)
		return
	}
	sendEvictableSegmentsResponse(stream, &pb.EvictableSegmentsResponse{
		RequestId:    requestID,
		DvrHash:      dvrHash,
		SegmentNames: names,
	}, logger)
}

// resolveDVRTenantAndStream looks up tenant + stream from the artifacts row,
// falling back to Commodore.ResolveDVRHash if the local row is missing.
// Both are needed to construct the S3 prefix for the segment.
func resolveDVRTenantAndStream(ctx context.Context, dvrHash string, logger logging.Logger) (tenantID, streamName string, ok bool) {
	if db != nil {
		var t, s sql.NullString
		err := db.QueryRowContext(ctx, `
			SELECT tenant_id::text, stream_internal_name
			  FROM foghorn.artifacts
			 WHERE artifact_hash = $1 AND artifact_type = 'dvr'
		`, dvrHash).Scan(&t, &s)
		if err == nil {
			tenantID = strings.TrimSpace(t.String)
			streamName = strings.TrimSpace(s.String)
			if tenantID != "" && streamName != "" {
				return tenantID, streamName, true
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("artifact lookup failed")
		}
	}
	if CommodoreClient != nil {
		resp, resolveErr := CommodoreClient.ResolveDVRHash(ctx, dvrHash)
		if resolveErr == nil && resp.Found {
			return resp.TenantId, resp.StreamInternalName, resp.TenantId != "" && resp.StreamInternalName != ""
		}
	}
	return "", "", false
}

// dvrEffectiveWindowSeconds returns the resolved live seek window stamped
// onto the artifact at start time. Retention is independent and applies only
// after the DVR session ends.
func dvrEffectiveWindowSeconds(ctx context.Context, dvrHash string) int {
	if db == nil {
		return 0
	}
	var window sql.NullInt32
	if err := db.QueryRowContext(ctx, `
		SELECT dvr_window_seconds
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1 AND artifact_type = 'dvr'
	`, dvrHash).Scan(&window); err != nil {
		return 0
	}
	if !window.Valid || window.Int32 <= 0 {
		return 0
	}
	return int(window.Int32)
}

func sendRecordDVRSegmentResponse(stream pb.HelmsmanControl_ConnectServer, resp *pb.RecordDVRSegmentResponse, logger logging.Logger) {
	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_RecordDvrSegmentResponse{RecordDvrSegmentResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash":     resp.GetDvrHash(),
			"segment_name": resp.GetSegmentName(),
		}).Error("Failed to send RecordDVRSegmentResponse")
	}
}

// processRestoreLocalSegmentIndexRequest answers a sidecar restart
// reconciliation request. Bounded by the request size — the sidecar batches
// discovered (artifact_hash, segment_name) pairs from local disk and asks
// for ledger state per batch. Foghorn does a single bounded query and
// returns one DVRSegmentRef per matching ledger row. Names not in the
// ledger are omitted from the response (the sidecar interprets the absence
// as "this local file isn't tracked; treat as orphan and consider for
// cleanup").
func processRestoreLocalSegmentIndexRequest(
	req *pb.RestoreLocalSegmentIndexRequest,
	nodeID string,
	stream pb.HelmsmanControl_ConnectServer,
	logger logging.Logger,
) {
	requestID := req.GetRequestId()
	dvrHash := req.GetDvrHash()
	names := req.GetSegmentNames()

	resp := &pb.RestoreLocalSegmentIndexResponse{
		RequestId: requestID,
		DvrHash:   dvrHash,
	}

	if dvrHash == "" || len(names) == 0 {
		sendRestoreLocalSegmentIndexResponse(stream, resp, logger)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := LookupDVRSegmentsByName(ctx, dvrHash, names)
	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash":   dvrHash,
			"node_id":    nodeID,
			"name_count": len(names),
		}).Error("Failed to look up DVR segments by name for restart reconciliation")
		sendRestoreLocalSegmentIndexResponse(stream, resp, logger)
		return
	}

	resp.Segments = make([]*pb.DVRSegmentRef, 0, len(rows))
	for _, r := range rows {
		var size int64
		if r.SizeBytes.Valid {
			size = r.SizeBytes.Int64
		}
		resp.Segments = append(resp.Segments, &pb.DVRSegmentRef{
			SegmentName:  r.SegmentName,
			S3Key:        r.S3Key,
			MediaStartMs: r.MediaStartMs,
			MediaEndMs:   r.MediaEndMs,
			DurationMs:   r.DurationMs,
			Status:       r.Status,
			Sequence:     r.Sequence,
			// presigned_get_url intentionally empty — restart reconciliation
			// is a metadata refresh, not a download request. The sidecar
			// only needs to know what's tracked vs. orphan and what state
			// each tracked segment is in.
		})
		_ = size
	}

	sendRestoreLocalSegmentIndexResponse(stream, resp, logger)
}

func sendRestoreLocalSegmentIndexResponse(stream pb.HelmsmanControl_ConnectServer, resp *pb.RestoreLocalSegmentIndexResponse, logger logging.Logger) {
	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_RestoreLocalSegmentIndexResponse{RestoreLocalSegmentIndexResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).WithField("dvr_hash", resp.GetDvrHash()).Error("Failed to send RestoreLocalSegmentIndexResponse")
	}
}

// SendRetryDVRSegmentUpload pushes a RetryDVRSegmentUpload to a sidecar by
// node_id. Best-effort: if the node isn't connected the call returns
// ErrNotConnected and the caller should treat the segment as still pending
// (FinalizeDVR will time it out into lost_local).
func SendRetryDVRSegmentUpload(nodeID string, req *pb.RetryDVRSegmentUpload) error {
	if nodeID == "" {
		return ErrNotConnected
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_RetryDvrSegmentUpload{RetryDvrSegmentUpload: req},
	}
	return c.stream.Send(msg)
}

func sendEvictableSegmentsResponse(stream pb.HelmsmanControl_ConnectServer, resp *pb.EvictableSegmentsResponse, logger logging.Logger) {
	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_EvictableSegmentsResponse{EvictableSegmentsResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).WithField("dvr_hash", resp.GetDvrHash()).Error("Failed to send EvictableSegmentsResponse")
	}
}
