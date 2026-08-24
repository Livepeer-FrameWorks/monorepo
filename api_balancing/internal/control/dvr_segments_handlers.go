package control

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DVR PER-SEGMENT LEDGER — control-stream handlers (Foghorn side)
// The sidecar reports each Mist RECORDING_SEGMENT to Foghorn before
// uploading to S3. Foghorn assigns a monotonic sequence, mints a presigned
// PUT URL, and persists a 'pending' row in foghorn.dvr_segments. After the
// upload completes the sidecar reports MarkDVRSegmentUploaded, transitioning
// the row to 'uploaded'. Forced evictions report DVRSegmentDropped — those
// transition the row to deleted_local (was_uploaded=true) or lost_local
// (was_uploaded=false). Chapters that overlap a lost row fail
// finalization with state=failed_source_missing.

// presignTTL is how long an issued presigned PUT URL is valid. The sidecar
// uploads immediately on receipt; allowing a generous TTL covers transient
// retries without re-asking Foghorn for a fresh URL.
const presignTTL = 15 * time.Minute

// processRecordDVRSegment handles RecordDVRSegmentRequest from the sidecar.
// Resolves the parent DVR's tenant + stream so the S3 prefix can be built,
// inserts a 'pending' ledger row (Foghorn-assigned sequence), mints a
// presigned PUT URL, and replies with the URL + s3_key + sequence.
func processRecordDVRSegment(
	req *ipcpb.RecordDVRSegmentRequest,
	nodeID string,
	stream ipcpb.HelmsmanControl_ConnectServer,
	logger logging.Logger,
) {
	requestID := req.GetRequestId()
	dvrHash := req.GetDvrHash()
	segmentName := req.GetSegmentName()

	if dvrHash == "" || segmentName == "" {
		sendRecordDVRSegmentResponse(stream, &ipcpb.RecordDVRSegmentResponse{
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
		sendRecordDVRSegmentResponse(stream, &ipcpb.RecordDVRSegmentResponse{
			RequestId:   requestID,
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			Accepted:    false,
			Reason:      "dvr_artifact_not_found",
		}, logger)
		return
	}

	// Bind the segment op to the dispatched recording owner BEFORE any mutation, strictly and scoped to
	// the owning tenant: only the node this DVR was dispatched to may revive artifact_nodes, insert a
	// ledger row, or mint a presigned PUT URL. A non-owner (stale/reassigned/foreign) node — or a
	// terminal row whose owner mismatches — is rejected with no side effect; fail closed on the
	// owner-lookup query error.
	if authorized, chkErr := dvrReportNodeAuthorized(ctx, dvrHash, tenantID, nodeID, dvrAuthStrict); chkErr != nil || !authorized {
		logger.WithError(chkErr).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"node_id":      nodeID,
		}).Warn("Rejecting DVR segment record: reporting node is not the dispatched recording owner (or lookup failed)")
		sendRecordDVRSegmentResponse(stream, &ipcpb.RecordDVRSegmentResponse{
			RequestId:   requestID,
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			Accepted:    false,
			Reason:      "not_recording_owner",
		}, logger)
		return
	}

	if db != nil && nodeID != "" {
		if err := foghorndb.New(db).TouchDVRRecordingNode(ctx, foghorndb.TouchDVRRecordingNodeParams{
			ArtifactHash: dvrHash, NodeID: nodeID,
		}); err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"dvr_hash": dvrHash,
				"node_id":  nodeID,
			}).Warn("Failed to refresh active DVR recording node from segment trigger")
		} else if rerr := RefreshNodeCopy(ctx, dvrHash, nodeID); rerr != nil {
			logger.WithError(rerr).WithField("dvr_hash", dvrHash).Warn("Failed to emit node-copy GAINED after DVR segment refresh")
		}
	}

	if s3Client == nil {
		sendRecordDVRSegmentResponse(stream, &ipcpb.RecordDVRSegmentResponse{
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
		tenantID,
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
		sendRecordDVRSegmentResponse(stream, &ipcpb.RecordDVRSegmentResponse{
			RequestId:   requestID,
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			Accepted:    false,
			Reason:      reason,
		}, logger)
		return
	}
	publishDVRSegmentProgress(ctx, dvrHash, "recording", sequence+1, 0, nodeID, logger)

	presignedURL, err := s3Client.GeneratePresignedPUT(s3Key, presignTTL)
	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
		}).Error("Failed to generate presigned PUT URL for DVR segment")
		sendRecordDVRSegmentResponse(stream, &ipcpb.RecordDVRSegmentResponse{
			RequestId:   requestID,
			DvrHash:     dvrHash,
			SegmentName: segmentName,
			Accepted:    false,
			Reason:      "presign_failed",
		}, logger)
		return
	}

	sendRecordDVRSegmentResponse(stream, &ipcpb.RecordDVRSegmentResponse{
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
func processMarkDVRSegmentUploaded(req *ipcpb.MarkDVRSegmentUploaded, nodeID string, logger logging.Logger) {
	dvrHash := req.GetDvrHash()
	segmentName := req.GetSegmentName()
	if dvrHash == "" || segmentName == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Resolve the owning tenant from the hash, then gate the (tenant-scoped) ledger mutation on the
	// dispatched recording owner. The ledger mutation keys on (artifact_hash, segment_name), so both the
	// owner check and the mutation are scoped to the tenant-owned parent. A non-owner reporting an upload
	// for another node's recording is rejected with no mutation; a hash with no local DVR row is refused.
	tenantID, found, ownErr := dvrOwnerTenant(ctx, dvrHash)
	if ownErr != nil || !found {
		logger.WithError(ownErr).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"node_id":      nodeID,
		}).Warn("Ignoring DVR segment uploaded: no tenant-owned DVR row for hash (or lookup failed)")
		return
	}
	if authorized, chkErr := dvrReportNodeAuthorized(ctx, dvrHash, tenantID, nodeID, dvrAuthStrict); chkErr != nil || !authorized {
		logger.WithError(chkErr).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"node_id":      nodeID,
		}).Warn("Ignoring DVR segment uploaded: reporting node is not the dispatched recording owner (or lookup failed)")
		return
	}
	if err := MarkDVRSegmentUploaded(ctx, tenantID, dvrHash, segmentName, int64(req.GetSizeBytes())); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"node_id":      nodeID,
		}).Error("Failed to mark DVR segment uploaded")
		return
	}
	count, size, err := DVRSegmentProgress(ctx, tenantID, dvrHash)
	if err != nil {
		logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Failed to aggregate DVR segment progress")
		return
	}
	publishDVRSegmentProgress(ctx, dvrHash, "recording", count, size, nodeID, logger)
}

// processDVRSegmentDropped records a sidecar-reported eviction. was_uploaded
// distinguishes safe local cleanup (deleted_local; chapter finalization
// recovers from S3) from data loss (lost_local; chapters overlapping the
// row transition to failed_source_missing).
func processDVRSegmentDropped(req *ipcpb.DVRSegmentDropped, nodeID string, logger logging.Logger) {
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
	// Same tenant-scoped owner gate as the uploaded path: the mutation keys on (artifact_hash,
	// segment_name), so a non-owner reporting a drop for another node's recording is rejected with no
	// mutation, and a hash with no local DVR row is refused. Fail closed on the owner-lookup query error.
	tenantID, found, ownErr := dvrOwnerTenant(ctx, dvrHash)
	if ownErr != nil || !found {
		logger.WithError(ownErr).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"node_id":      nodeID,
		}).Warn("Ignoring DVR segment dropped: no tenant-owned DVR row for hash (or lookup failed)")
		return
	}
	if authorized, chkErr := dvrReportNodeAuthorized(ctx, dvrHash, tenantID, nodeID, dvrAuthStrict); chkErr != nil || !authorized {
		logger.WithError(chkErr).WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"node_id":      nodeID,
		}).Warn("Ignoring DVR segment dropped: reporting node is not the dispatched recording owner (or lookup failed)")
		return
	}
	if err := MarkDVRSegmentDropped(
		ctx, tenantID, dvrHash, segmentName, reason, req.GetWasUploaded(),
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
		// lost_local with was_uploaded=false is the data-loss case;
		// surface at warn so ops sees it. Chapter finalization for any
		// overlapping chapter will fall through to failed_source_missing
		// if both local and S3 recovery are exhausted for this segment.
		logger.WithFields(logging.Fields{
			"dvr_hash":     dvrHash,
			"segment_name": segmentName,
			"reason":       reason,
			"duration_ms":  req.GetDurationMs(),
			"node_id":      nodeID,
		}).Warn("DVR segment lost before S3 upload; chapter finalization may degrade")
	}
}

// processEvictableSegmentsRequest answers an authoritative "which segments
// are safe to evict" query during sidecar storage-pressure passes.
func processEvictableSegmentsRequest(
	req *ipcpb.EvictableSegmentsRequest,
	nodeID string,
	stream ipcpb.HelmsmanControl_ConnectServer,
	logger logging.Logger,
) {
	requestID := req.GetRequestId()
	dvrHash := req.GetDvrHash()
	if dvrHash == "" {
		sendEvictableSegmentsResponse(stream, &ipcpb.EvictableSegmentsResponse{
			RequestId: requestID,
			DvrHash:   dvrHash,
		}, logger)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Only the dispatched recording owner may drive local eviction for this DVR; a non-owner (or a hash
	// with no local DVR row) gets an empty (evict-nothing) answer. The owner check and the evictable
	// query are scoped to the owning tenant. Fail closed on the owner-lookup query error.
	tenantID, found, ownErr := dvrOwnerTenant(ctx, dvrHash)
	if ownErr != nil || !found {
		sendEvictableSegmentsResponse(stream, &ipcpb.EvictableSegmentsResponse{
			RequestId: requestID,
			DvrHash:   dvrHash,
		}, logger)
		return
	}
	if authorized, chkErr := dvrReportNodeAuthorized(ctx, dvrHash, tenantID, nodeID, dvrAuthStrict); chkErr != nil || !authorized {
		logger.WithError(chkErr).WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"node_id":  nodeID,
		}).Warn("Refusing evictable-segments answer: reporting node is not the dispatched recording owner (or lookup failed)")
		sendEvictableSegmentsResponse(stream, &ipcpb.EvictableSegmentsResponse{
			RequestId: requestID,
			DvrHash:   dvrHash,
		}, logger)
		return
	}

	// Effective window comes from dvr_window_seconds stamped on the artifact
	// at DVR start. Retention is post-end only and must not influence local
	// active-recording eviction. The final answer is clamped to uploaded rows
	// older than now-window, so the worst case is "evicts nothing".
	windowSeconds := int(4 * time.Hour / time.Second)
	if w := dvrEffectiveWindowSeconds(ctx, dvrHash); w > 0 {
		windowSeconds = w
	}

	names, err := ListEvictableDVRSegments(ctx, tenantID, dvrHash, windowSeconds, int(req.GetMaxCount()))
	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash": dvrHash,
			"node_id":  nodeID,
		}).Error("Failed to list evictable DVR segments")
		sendEvictableSegmentsResponse(stream, &ipcpb.EvictableSegmentsResponse{
			RequestId: requestID,
			DvrHash:   dvrHash,
		}, logger)
		return
	}
	sendEvictableSegmentsResponse(stream, &ipcpb.EvictableSegmentsResponse{
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
		row, err := foghorndb.New(db).GetDVRTenantAndStream(ctx, dvrHash)
		if err == nil {
			tenantID = strings.TrimSpace(row.TenantID)
			streamName = strings.TrimSpace(row.StreamInternalName.String)
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

func publishDVRSegmentProgress(ctx context.Context, dvrHash, status string, segmentCount, sizeBytes int64, nodeID string, logger logging.Logger) {
	if dvrHash == "" || segmentCount < 0 || sizeBytes < 0 {
		return
	}
	if _, err := state.DefaultManager().ApplyDVRProgress(ctx, dvrHash, status, uint64(sizeBytes), uint32(segmentCount), nodeID); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash":      dvrHash,
			"status":        status,
			"segment_count": segmentCount,
			"size_bytes":    sizeBytes,
		}).Warn("Failed to publish DVR segment progress")
	}
}

// dvrEffectiveWindowSeconds returns the resolved live seek window stamped
// onto the artifact at start time. Retention is independent and applies only
// after the DVR session ends.
func dvrEffectiveWindowSeconds(ctx context.Context, dvrHash string) int {
	if db == nil {
		return 0
	}
	window, err := foghorndb.New(db).GetDVREffectiveWindowSeconds(ctx, dvrHash)
	if err != nil {
		return 0
	}
	if !window.Valid || window.Int32 <= 0 {
		return 0
	}
	return int(window.Int32)
}

func sendRecordDVRSegmentResponse(stream ipcpb.HelmsmanControl_ConnectServer, resp *ipcpb.RecordDVRSegmentResponse, logger logging.Logger) {
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_RecordDvrSegmentResponse{RecordDvrSegmentResponse: resp},
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
	req *ipcpb.RestoreLocalSegmentIndexRequest,
	nodeID string,
	stream ipcpb.HelmsmanControl_ConnectServer,
	logger logging.Logger,
) {
	requestID := req.GetRequestId()
	dvrHash := req.GetDvrHash()
	names := req.GetSegmentNames()

	resp := &ipcpb.RestoreLocalSegmentIndexResponse{
		RequestId: requestID,
		DvrHash:   dvrHash,
	}

	if dvrHash == "" || len(names) == 0 {
		sendRestoreLocalSegmentIndexResponse(stream, resp, logger)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Restart reconciliation reads this DVR's ledger; only the dispatched recording owner may, scoped to
	// the owning tenant. A non-owner (or a hash with no local DVR row) gets an empty response (no tracked
	// segments disclosed). Fail closed on the owner-lookup query error.
	tenantID, found, ownErr := dvrOwnerTenant(ctx, dvrHash)
	if ownErr != nil || !found {
		sendRestoreLocalSegmentIndexResponse(stream, resp, logger)
		return
	}
	if authorized, chkErr := dvrReportNodeAuthorized(ctx, dvrHash, tenantID, nodeID, dvrAuthStrict); chkErr != nil || !authorized {
		logger.WithError(chkErr).WithFields(logging.Fields{
			"dvr_hash":   dvrHash,
			"node_id":    nodeID,
			"name_count": len(names),
		}).Warn("Refusing restart reconciliation: reporting node is not the dispatched recording owner (or lookup failed)")
		sendRestoreLocalSegmentIndexResponse(stream, resp, logger)
		return
	}

	rows, err := LookupDVRSegmentsByName(ctx, tenantID, dvrHash, names)
	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"dvr_hash":   dvrHash,
			"node_id":    nodeID,
			"name_count": len(names),
		}).Error("Failed to look up DVR segments by name for restart reconciliation")
		sendRestoreLocalSegmentIndexResponse(stream, resp, logger)
		return
	}

	resp.Segments = make([]*ipcpb.DVRSegmentRef, 0, len(rows))
	for _, r := range rows {
		var size int64
		if r.SizeBytes.Valid {
			size = r.SizeBytes.Int64
		}
		resp.Segments = append(resp.Segments, &ipcpb.DVRSegmentRef{
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

func sendRestoreLocalSegmentIndexResponse(stream ipcpb.HelmsmanControl_ConnectServer, resp *ipcpb.RestoreLocalSegmentIndexResponse, logger logging.Logger) {
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_RestoreLocalSegmentIndexResponse{RestoreLocalSegmentIndexResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).WithField("dvr_hash", resp.GetDvrHash()).Error("Failed to send RestoreLocalSegmentIndexResponse")
	}
}

// SendRetryDVRSegmentUpload pushes a RetryDVRSegmentUpload to a sidecar by
// node_id. Best-effort: if the node isn't connected the call returns
// ErrNotConnected and the caller should treat the segment as still pending
// (FinalizeDVR will time it out into lost_local).
func SendRetryDVRSegmentUpload(nodeID string, req *ipcpb.RetryDVRSegmentUpload) error {
	if nodeID == "" {
		return ErrNotConnected
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_RetryDvrSegmentUpload{RetryDvrSegmentUpload: req},
	}
	return c.stream.Send(msg)
}

// SendReclaimDVRSegment asks the recording-origin Helmsman to delete
// local TS segment files after every overlapping chapter has reached
// state='frozen'. Idempotent on the sidecar side.
func SendReclaimDVRSegment(nodeID string, req *ipcpb.ReclaimDVRSegment) error {
	if nodeID == "" {
		return ErrNotConnected
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_ReclaimDvrSegment{ReclaimDvrSegment: req},
	}
	return c.stream.Send(msg)
}

func sendEvictableSegmentsResponse(stream ipcpb.HelmsmanControl_ConnectServer, resp *ipcpb.EvictableSegmentsResponse, logger logging.Logger) {
	msg := &ipcpb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &ipcpb.ControlMessage_EvictableSegmentsResponse{EvictableSegmentsResponse: resp},
	}
	if err := stream.Send(msg); err != nil {
		logger.WithError(err).WithField("dvr_hash", resp.GetDvrHash()).Error("Failed to send EvictableSegmentsResponse")
	}
}
