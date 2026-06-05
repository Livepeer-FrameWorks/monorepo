package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// Finalized-fact parser. Reads MistTrigger envelopes from the raw audit
// topic (republished by Decklog for the seven final/accounting trigger
// types — see api_firehose triggerTypesForRawJournal) and projects them
// into the per-meter *_final tables.
//
// The contract this file implements is in docs/architecture/meter-contracts.md:
//
//   - Append-only projections. Every parser pass writes a new row;
//     readers materialize via min/argMax on projection_version_ms.
//   - Stable source_event_id flows end-to-end and is the cross-pipeline
//     identity (also stamped on raw_mist_triggers).
//   - edge_received_at_ms = raw_mist_triggers.received_at_ms (Helmsman
//     WAL accept). billable_at_ms is derived on read, not stored.
//   - Pure replay is permitted and expected; material divergence beyond
//     per-meter epsilon must be recorded in projection_divergences before
//     the newer projection is accepted.

// triggerTypesWithFinalProjection mirrors api_firehose triggerTypesForRawJournal
// and api_balancing triggerTypesNeedingDurableAck. Keep in sync.
var triggerTypesWithFinalProjection = map[string]struct{}{
	"USER_END":                            {},
	"STREAM_END":                          {},
	"LIVEPEER_SEGMENT_COMPLETE":           {},
	"PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE": {},
	// PUSH_END, RECORDING_END, RECORDING_SEGMENT travel through the WAL
	// (so they're durable for audit) but are not projected into
	// final-fact tables because they don't drive a rated meter.
}

// projectFinalFact dispatches by trigger_type to the right *_final
// projector. Called from HandleRawMistTriggerMessage after the
// raw_mist_triggers audit row has been written.
//
// Returns nil on success or on a no-op (trigger type not in the
// projection set). Returns an error on write or correction-guardrail
// failures that should retry the Kafka message; JSON/proto parse errors
// are logged and swallowed because the audit row is already in
// raw_mist_triggers for forensic replay.
func (h *AnalyticsHandler) projectFinalFact(ctx context.Context, trigger *ipcpb.MistTrigger, sourceEventID string, edgeReceivedAtMS int64) error {
	if trigger == nil || sourceEventID == "" {
		return nil
	}
	triggerType := trigger.GetTriggerType()
	if _, ok := triggerTypesWithFinalProjection[triggerType]; !ok {
		return nil
	}

	projectionVersionMS := time.Now().UnixMilli()

	switch triggerType {
	case "USER_END":
		return h.projectViewerSessionFinal(ctx, trigger, sourceEventID, edgeReceivedAtMS, projectionVersionMS)
	case "STREAM_END":
		return h.projectStreamSessionFinal(ctx, trigger, sourceEventID, edgeReceivedAtMS, projectionVersionMS)
	case "LIVEPEER_SEGMENT_COMPLETE", "PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE":
		return h.projectProcessingSegmentFinal(ctx, trigger, sourceEventID, edgeReceivedAtMS, projectionVersionMS)
	}
	return nil
}

// projectViewerSessionFinal writes a viewer_sessions_final projection row.
// Multi-stream/connector/host sessions land with their per-element
// breakdown arrays preserved (stream_times / connector_times / host_times)
// so per-stream and marketplace attribution can split a single session.
func (h *AnalyticsHandler) projectViewerSessionFinal(ctx context.Context, trigger *ipcpb.MistTrigger, sourceEventID string, edgeReceivedAtMS, projectionVersionMS int64) error {
	vd := trigger.GetViewerDisconnect()
	if vd == nil {
		return nil
	}

	tenantID, ok := parseTenantID(trigger.GetTenantId())
	if !ok {
		// Tenant enrichment should have happened at Foghorn; reject
		// here to avoid polluting the final-fact table with unattributable rows.
		h.logger.WithFields(logging.Fields{
			"source_event_id": sourceEventID,
			"trigger_type":    "USER_END",
		}).Warn("Skipping USER_END projection: missing or invalid tenant_id")
		return nil
	}

	nodeID := trigger.GetNodeId()
	sessionID := vd.GetSessionId()
	if sessionID == "" {
		return nil
	}

	clusterID := trigger.GetClusterId()
	streamID := uuid.Nil
	if sid, err := uuid.Parse(vd.GetStreamId()); err == nil {
		streamID = sid
	}

	duration := vd.GetDuration()
	if duration < 0 {
		duration = 0
	}
	secondsConnected := vd.GetSecondsConnected()
	if secondsConnected > 0 && duration == 0 {
		duration = int64(secondsConnected)
	}

	sourceEndedAtMS := edgeReceivedAtMS
	sourceStartedAtMS := sourceEndedAtMS - duration*1000
	if sourceStartedAtMS < 0 {
		sourceStartedAtMS = 0
	}
	payloadRaw, err := proto.Marshal(trigger)
	if err != nil {
		return fmt.Errorf("marshal USER_END payload: %w", err)
	}

	mistUpBytes := vd.GetUpBytes()
	if mistUpBytes < 0 {
		mistUpBytes = 0
	}
	mistDownBytes := vd.GetDownBytes()
	if mistDownBytes < 0 {
		mistDownBytes = 0
	}

	row := viewerSessionFinalRow{
		tenantID:            tenantID,
		nodeID:              nodeID,
		sessionID:           sessionID,
		sourceEventID:       sourceEventID,
		clusterID:           clusterID,
		streamID:            streamID,
		streamName:          vd.GetStreamName(),
		connector:           vd.GetConnector(),
		host:                vd.GetHost(),
		countryCode:         normalizeCountryCode(vd.GetCountryCode()),
		city:                vd.GetCity(),
		latitude:            vd.GetLatitude(),
		longitude:           vd.GetLongitude(),
		tags:                vd.GetTags(),
		durationSeconds:     uint32(min64(duration, int64(^uint32(0)))),
		uploadedBytes:       uint64(mistDownBytes),
		downloadedBytes:     uint64(mistUpBytes),
		secondsConnected:    secondsConnected,
		sourceStartedAtMS:   sourceStartedAtMS,
		sourceEndedAtMS:     sourceEndedAtMS,
		edgeReceivedAtMS:    edgeReceivedAtMS,
		projectionVersionMS: projectionVersionMS,
		closedReason:        "final",
		streamTimes:         sessionTimeSharesToTuples(vd.GetStreamTimes()),
		connectorTimes:      sessionTimeSharesToTuples(vd.GetConnectorTimes()),
		hostTimes:           sessionTimeSharesToTuples(vd.GetHostTimes()),
		payloadRaw:          payloadRaw,
	}

	if divergenceErr := h.checkViewerSessionDivergence(ctx, row); divergenceErr != nil {
		return fmt.Errorf("viewer_sessions_final divergence guardrail: %w", divergenceErr)
	}

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.viewer_sessions_final (
			tenant_id, node_id, session_id, source_event_id,
			cluster_id, stream_id, stream_name, connector, host,
			country_code, city, latitude, longitude, tags,
			duration_seconds, uploaded_bytes, downloaded_bytes, seconds_connected,
			source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms,
			closed_reason, stream_times, connector_times, host_times, payload_raw
		)`)
	if err != nil {
		return fmt.Errorf("viewer_sessions_final prepare: %w", err)
	}
	defer closeClickHouseBatch(batch)
	if err := batch.Append(
		row.tenantID, row.nodeID, row.sessionID, row.sourceEventID,
		row.clusterID, row.streamID, row.streamName, row.connector, row.host,
		row.countryCode, row.city, row.latitude, row.longitude, row.tags,
		row.durationSeconds, row.uploadedBytes, row.downloadedBytes, row.secondsConnected,
		row.sourceStartedAtMS, row.sourceEndedAtMS, row.edgeReceivedAtMS, row.projectionVersionMS,
		row.closedReason, row.streamTimes, row.connectorTimes, row.hostTimes, row.payloadRaw,
	); err != nil {
		return fmt.Errorf("viewer_sessions_final append: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("viewer_sessions_final send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("viewer_sessions_final", "inserted").Inc()
	}
	return nil
}

// projectStreamSessionFinal writes a stream_sessions_final projection row.
func (h *AnalyticsHandler) projectStreamSessionFinal(ctx context.Context, trigger *ipcpb.MistTrigger, sourceEventID string, edgeReceivedAtMS, projectionVersionMS int64) error {
	se := trigger.GetStreamEnd()
	if se == nil {
		return nil
	}

	tenantID, ok := parseTenantID(trigger.GetTenantId())
	if !ok {
		h.logger.WithFields(logging.Fields{
			"source_event_id": sourceEventID,
			"trigger_type":    "STREAM_END",
		}).Warn("Skipping STREAM_END projection: missing or invalid tenant_id")
		return nil
	}

	streamID := uuid.Nil
	if rawStreamID := strings.TrimSpace(mistTriggerStreamID(trigger)); rawStreamID != "" {
		if parsedStreamID, parseErr := uuid.Parse(rawStreamID); parseErr == nil {
			streamID = parsedStreamID
		}
	}
	if streamID == uuid.Nil {
		h.logger.WithFields(logging.Fields{
			"source_event_id": sourceEventID,
			"trigger_type":    "STREAM_END",
			"stream_name":     se.GetStreamName(),
		}).Warn("Skipping STREAM_END projection: missing/invalid stream_id")
		return nil
	}

	nodeID := trigger.GetNodeId()
	if nodeID == "" {
		nodeID = se.GetNodeId()
	}

	clusterID := trigger.GetClusterId()

	streamName := se.GetStreamName()
	internalName := mist.ExtractInternalName(streamName)
	sourceEndedAtMS := edgeReceivedAtMS
	sourceStartedAtMS := h.lookupStreamStartedAtMS(ctx, streamStartLookup{
		tenantID:     tenantID,
		streamID:     streamID,
		nodeID:       nodeID,
		clusterID:    clusterID,
		internalName: internalName,
	}, sourceEndedAtMS)
	if sourceStartedAtMS < 0 {
		sourceStartedAtMS = 0
	}
	viewerSeconds := se.GetViewerSeconds()
	if viewerSeconds < 0 {
		viewerSeconds = 0
	}
	payloadRaw, err := proto.Marshal(trigger)
	if err != nil {
		return fmt.Errorf("marshal STREAM_END payload: %w", err)
	}

	row := streamSessionFinalRow{
		tenantID:            tenantID,
		nodeID:              nodeID,
		streamID:            streamID,
		sourceEventID:       sourceEventID,
		clusterID:           clusterID,
		streamName:          streamName,
		downloadedBytes:     se.GetDownloadedBytes(),
		uploadedBytes:       se.GetUploadedBytes(),
		totalViewers:        se.GetTotalViewers(),
		totalInputs:         se.GetTotalInputs(),
		totalOutputs:        se.GetTotalOutputs(),
		viewerSeconds:       viewerSeconds,
		sourceStartedAtMS:   sourceStartedAtMS,
		sourceEndedAtMS:     sourceEndedAtMS,
		edgeReceivedAtMS:    edgeReceivedAtMS,
		projectionVersionMS: projectionVersionMS,
		closedReason:        "final",
		payloadRaw:          payloadRaw,
	}
	if divergenceErr := h.checkStreamSessionDivergence(ctx, row); divergenceErr != nil {
		return fmt.Errorf("stream_sessions_final divergence guardrail: %w", divergenceErr)
	}

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.stream_sessions_final (
			tenant_id, node_id, stream_id, source_event_id,
			cluster_id, stream_name,
			downloaded_bytes, uploaded_bytes, total_viewers, total_inputs, total_outputs, viewer_seconds,
			source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms,
			closed_reason, payload_raw
		)`)
	if err != nil {
		return fmt.Errorf("stream_sessions_final prepare: %w", err)
	}
	defer closeClickHouseBatch(batch)
	if err := batch.Append(
		row.tenantID, row.nodeID, row.streamID, row.sourceEventID,
		row.clusterID, row.streamName,
		row.downloadedBytes, row.uploadedBytes, row.totalViewers, row.totalInputs, row.totalOutputs, row.viewerSeconds,
		row.sourceStartedAtMS, row.sourceEndedAtMS, row.edgeReceivedAtMS, row.projectionVersionMS,
		row.closedReason, row.payloadRaw,
	); err != nil {
		return fmt.Errorf("stream_sessions_final append: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("stream_sessions_final send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_sessions_final", "inserted").Inc()
	}
	return nil
}

// projectProcessingSegmentFinal handles both LIVEPEER_SEGMENT_COMPLETE
// and PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE. Empty output_codec triggers a
// quarantine; no row written here, audit row in raw_mist_triggers stands.
func (h *AnalyticsHandler) projectProcessingSegmentFinal(ctx context.Context, trigger *ipcpb.MistTrigger, sourceEventID string, edgeReceivedAtMS, projectionVersionMS int64) error {
	pb_ := trigger.GetProcessBilling()
	if pb_ == nil {
		return nil
	}

	tenantID, ok := parseTenantID(trigger.GetTenantId())
	if !ok {
		if pb_.GetTenantId() != "" {
			if t, perr := uuid.Parse(pb_.GetTenantId()); perr == nil && t != uuid.Nil {
				tenantID = t
				ok = true
			}
		}
	}
	if !ok {
		h.logger.WithFields(logging.Fields{
			"source_event_id": sourceEventID,
			"trigger_type":    trigger.GetTriggerType(),
		}).Warn("Skipping processing segment projection: missing or invalid tenant_id")
		return nil
	}

	processType := strings.TrimSpace(pb_.GetProcessType())
	outputCodec := strings.TrimSpace(pb_.GetOutputCodec())
	if outputCodec == "" && processType == "Livepeer" {
		// MistProcLivepeer profiles are H264-only in the local MistServer
		// implementation; the trigger predates the output_codec field.
		outputCodec = "h264"
	}
	if outputCodec == "" {
		// Quarantine: empty output_codec would silently bucket into ''
		// in the analytics ledger. Don't write to *_final.
		h.logger.WithFields(logging.Fields{
			"source_event_id": sourceEventID,
			"trigger_type":    trigger.GetTriggerType(),
			"process_type":    pb_.GetProcessType(),
		}).Warn("Quarantining processing segment with empty output_codec")
		if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("processing_segments_final", "quarantined_empty_codec").Inc()
		}
		return nil
	}

	streamID := uuid.Nil
	if pb_.GetStreamId() != "" {
		if sid, err := uuid.Parse(pb_.GetStreamId()); err == nil {
			streamID = sid
		}
	}
	nodeID := pb_.GetNodeId()
	if nodeID == "" {
		nodeID = trigger.GetNodeId()
	}
	clusterID := strings.TrimSpace(pb_.GetClusterId())
	if clusterID == "" {
		clusterID = strings.TrimSpace(trigger.GetClusterId())
	}

	// media_seconds = input media duration. For AV virtual segments
	// MistServer emits both wall-clock seconds_since_last (in DurationMs)
	// and the input-media advance (in SourceAdvancedMs). The latter is
	// the correct billable quantity: wall-clock diverges from media when
	// the processor stalls, catches up, or runs faster/slower than
	// realtime. For Livepeer segment events DurationMs is already the
	// per-segment input duration so we keep it. See
	// docs/architecture/meter-contracts.md.
	var billableDurationMS int64
	if processType == "AV" && pb_.SourceAdvancedMs != nil {
		billableDurationMS = pb_.GetSourceAdvancedMs()
	} else {
		billableDurationMS = pb_.GetDurationMs()
	}
	if billableDurationMS < 0 {
		billableDurationMS = 0
	}
	rawDurationSeconds := float64(billableDurationMS) / 1000.0

	sourceEndedAtMS := edgeReceivedAtMS
	sourceStartedAtMS := sourceEndedAtMS - billableDurationMS
	if sourceStartedAtMS < 0 {
		sourceStartedAtMS = 0
	}
	payloadRaw, err := proto.Marshal(trigger)
	if err != nil {
		return fmt.Errorf("marshal processing segment payload: %w", err)
	}

	isFinal := uint8(0)
	if pb_.GetIsFinal() {
		isFinal = 1
	}

	trackType := strings.TrimSpace(pb_.GetTrackType())
	// segment_number is informational only; the natural key for dedupe
	// is source_event_id (see processing_segments_final ORDER BY /
	// processing_segments_final_v GROUP BY). AV virtual segments don't
	// carry a real segment_number and that's fine: distinct AV
	// triggers have distinct source_event_ids and can't collapse.
	segmentNumber := pb_.GetSegmentNumber()
	row := processingSegmentFinalRow{
		tenantID:      tenantID,
		nodeID:        nodeID,
		streamID:      streamID,
		processType:   processType,
		outputCodec:   outputCodec,
		trackType:     trackType,
		sourceEventID: sourceEventID,
		clusterID:     clusterID,
		mediaSeconds:  rawDurationSeconds,
	}
	if divergenceErr := h.checkProcessingSegmentDivergence(ctx, row); divergenceErr != nil {
		return fmt.Errorf("processing_segments_final divergence guardrail: %w", divergenceErr)
	}

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.processing_segments_final (
			tenant_id, node_id, stream_id, process_type, output_codec, track_type, segment_number,
			source_event_id,
			cluster_id, stream_name, input_codec, media_seconds,
			width, height, rendition_count, input_bytes, output_bytes_total, turnaround_ms, speed_factor, livepeer_session_id,
			input_frames, output_frames, input_frames_delta, output_frames_delta, input_bytes_delta, output_bytes_delta,
			rtf_in, rtf_out, is_final,
			source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms,
			payload_raw
		)`)
	if err != nil {
		return fmt.Errorf("processing_segments_final prepare: %w", err)
	}
	defer closeClickHouseBatch(batch)
	if err := batch.Append(
		tenantID, nodeID, streamID, processType, outputCodec, trackType, segmentNumber,
		sourceEventID,
		clusterID, pb_.GetStreamName(), pb_.GetInputCodec(), rawDurationSeconds,
		pb_.GetWidth(), pb_.GetHeight(), pb_.GetRenditionCount(), pb_.GetInputBytes(), pb_.GetOutputBytesTotal(), pb_.GetTurnaroundMs(), pb_.GetSpeedFactor(), pb_.GetLivepeerSessionId(),
		pb_.GetInputFrames(), pb_.GetOutputFrames(), pb_.GetInputFramesDelta(), pb_.GetOutputFramesDelta(), pb_.GetInputBytesDelta(), pb_.GetOutputBytesDelta(),
		pb_.GetRtfIn(), pb_.GetRtfOut(), isFinal,
		sourceStartedAtMS, sourceEndedAtMS, edgeReceivedAtMS, projectionVersionMS,
		payloadRaw,
	); err != nil {
		return fmt.Errorf("processing_segments_final append: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("processing_segments_final send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("processing_segments_final", "inserted").Inc()
	}
	return nil
}

// --- divergence detection ---

// viewerSessionFinalRow mirrors the storage shape of a single projection
// row so the divergence helper has typed access to the rated fields.
type viewerSessionFinalRow struct {
	tenantID            uuid.UUID
	nodeID              string
	sessionID           string
	sourceEventID       string
	clusterID           string
	streamID            uuid.UUID
	streamName          string
	connector           string
	host                string
	countryCode         string
	city                string
	latitude            float64
	longitude           float64
	tags                string
	durationSeconds     uint32
	uploadedBytes       uint64
	downloadedBytes     uint64
	secondsConnected    uint64
	sourceStartedAtMS   int64
	sourceEndedAtMS     int64
	edgeReceivedAtMS    int64
	projectionVersionMS int64
	closedReason        string
	streamTimes         [][]any
	connectorTimes      [][]any
	hostTimes           [][]any
	payloadRaw          []byte
}

type streamSessionFinalRow struct {
	tenantID            uuid.UUID
	nodeID              string
	streamID            uuid.UUID
	sourceEventID       string
	clusterID           string
	streamName          string
	downloadedBytes     int64
	uploadedBytes       int64
	totalViewers        int64
	totalInputs         int64
	totalOutputs        int64
	viewerSeconds       int64
	sourceStartedAtMS   int64
	sourceEndedAtMS     int64
	edgeReceivedAtMS    int64
	projectionVersionMS int64
	closedReason        string
	payloadRaw          []byte
}

// checkViewerSessionDivergence looks up the prior materialized row for
// this natural key and, if found, compares rated fields to the new
// projection. Material divergence must be durably recorded before the
// newer projection is written because the billing cursor does not revisit
// already-seen logical facts.
func (h *AnalyticsHandler) checkViewerSessionDivergence(ctx context.Context, row viewerSessionFinalRow) error {
	rows, err := h.clickhouse.Query(ctx, `
		SELECT
			argMax(duration_seconds,    projection_version_ms),
			argMax(uploaded_bytes,      projection_version_ms),
			argMax(downloaded_bytes,    projection_version_ms),
			argMax(cluster_id,          projection_version_ms)
		FROM periscope.viewer_sessions_final
		WHERE tenant_id = ? AND node_id = ? AND session_id = ?
		GROUP BY tenant_id, node_id, session_id`,
		row.tenantID, row.nodeID, row.sessionID)
	if err != nil {
		return fmt.Errorf("lookup prior viewer session projection: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if iterErr := rows.Err(); iterErr != nil {
			return fmt.Errorf("iterate prior viewer session projection: %w", iterErr)
		}
		return nil
	}
	var priorDuration uint32
	var priorUp, priorDown uint64
	var priorCluster string
	if scanErr := rows.Scan(&priorDuration, &priorUp, &priorDown, &priorCluster); scanErr != nil {
		return fmt.Errorf("scan prior viewer session projection: %w", scanErr)
	}

	type divergence struct {
		field string
		prior any
		newer any
	}
	var found []divergence

	const byteEpsilon = uint64(1024) // 1 KiB
	clusterChanged := priorCluster != row.clusterID
	if clusterChanged {
		found = append(found, divergence{
			"cluster_id",
			map[string]any{
				"cluster_id":       priorCluster,
				"duration_seconds": priorDuration,
				"uploaded_bytes":   priorUp,
				"downloaded_bytes": priorDown,
			},
			map[string]any{
				"cluster_id":       row.clusterID,
				"duration_seconds": row.durationSeconds,
				"uploaded_bytes":   row.uploadedBytes,
				"downloaded_bytes": row.downloadedBytes,
			},
		})
	} else {
		if absDeltaUint32(priorDuration, row.durationSeconds) >= 1 {
			found = append(found, divergence{"duration_seconds", priorDuration, row.durationSeconds})
		}
		if absDeltaUint64(priorUp, row.uploadedBytes) >= byteEpsilon {
			found = append(found, divergence{"uploaded_bytes", priorUp, row.uploadedBytes})
		}
		if absDeltaUint64(priorDown, row.downloadedBytes) >= byteEpsilon {
			found = append(found, divergence{"downloaded_bytes", priorDown, row.downloadedBytes})
		}
	}

	if len(found) == 0 {
		return nil
	}

	naturalKey := map[string]string{
		"tenant_id":  row.tenantID.String(),
		"node_id":    row.nodeID,
		"session_id": row.sessionID,
		"cluster_id": row.clusterID,
	}
	naturalKeyJSON, err := json.Marshal(naturalKey)
	if err != nil {
		return fmt.Errorf("marshal viewer session divergence natural key: %w", err)
	}

	observedAtMS := time.Now().UnixMilli()
	for _, d := range found {
		priorJSON, err := json.Marshal(d.prior)
		if err != nil {
			return fmt.Errorf("marshal viewer session divergence prior value: %w", err)
		}
		newJSON, err := json.Marshal(d.newer)
		if err != nil {
			return fmt.Errorf("marshal viewer session divergence new value: %w", err)
		}
		if err := h.recordProjectionDivergence(ctx, observedAtMS, "viewer_sessions_final", "delivered_minutes", d.field, string(naturalKeyJSON), string(priorJSON), string(newJSON), row.sourceEventID); err != nil {
			return fmt.Errorf("record viewer session divergence for field %s: %w", d.field, err)
		}
		if h.metrics != nil && h.metrics.ProjectionDivergences != nil {
			h.metrics.ProjectionDivergences.WithLabelValues("viewer_sessions_final", "delivered_minutes", d.field).Inc()
		}
	}
	return nil
}

func (h *AnalyticsHandler) checkStreamSessionDivergence(ctx context.Context, row streamSessionFinalRow) error {
	rows, err := h.clickhouse.Query(ctx, `
		SELECT
			argMax(cluster_id,           projection_version_ms),
			argMax(source_started_at_ms, projection_version_ms),
			argMax(source_ended_at_ms,   projection_version_ms)
		FROM periscope.stream_sessions_final
		WHERE tenant_id = ? AND node_id = ? AND stream_id = ? AND source_event_id = ?
		GROUP BY tenant_id, node_id, stream_id, source_event_id`,
		row.tenantID, row.nodeID, row.streamID, row.sourceEventID)
	if err != nil {
		return fmt.Errorf("lookup prior stream session projection: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if iterErr := rows.Err(); iterErr != nil {
			return fmt.Errorf("iterate prior stream session projection: %w", iterErr)
		}
		return nil
	}

	var priorCluster string
	var priorStartedAtMS, priorEndedAtMS int64
	if scanErr := rows.Scan(&priorCluster, &priorStartedAtMS, &priorEndedAtMS); scanErr != nil {
		return fmt.Errorf("scan prior stream session projection: %w", scanErr)
	}

	priorSeconds := sourceRuntimeSeconds(priorStartedAtMS, priorEndedAtMS)
	newSeconds := sourceRuntimeSeconds(row.sourceStartedAtMS, row.sourceEndedAtMS)
	if priorCluster == row.clusterID && math.Abs(priorSeconds-newSeconds) < 1 {
		return nil
	}

	field := "runtime_seconds"
	var prior any = priorSeconds
	var newer any = newSeconds
	if priorCluster != row.clusterID {
		field = "cluster_id"
		prior = map[string]any{"cluster_id": priorCluster, "runtime_seconds": priorSeconds}
		newer = map[string]any{"cluster_id": row.clusterID, "runtime_seconds": newSeconds}
	}

	naturalKeyJSON, err := json.Marshal(map[string]string{
		"tenant_id":       row.tenantID.String(),
		"node_id":         row.nodeID,
		"stream_id":       row.streamID.String(),
		"source_event_id": row.sourceEventID,
		"cluster_id":      row.clusterID,
	})
	if err != nil {
		return fmt.Errorf("marshal stream session divergence natural key: %w", err)
	}
	priorJSON, err := json.Marshal(prior)
	if err != nil {
		return fmt.Errorf("marshal stream session divergence prior value: %w", err)
	}
	newJSON, err := json.Marshal(newer)
	if err != nil {
		return fmt.Errorf("marshal stream session divergence new value: %w", err)
	}
	if err := h.recordProjectionDivergence(ctx, time.Now().UnixMilli(), "stream_sessions_final", "stream_runtime_seconds", field, string(naturalKeyJSON), string(priorJSON), string(newJSON), row.sourceEventID); err != nil {
		return fmt.Errorf("record stream session divergence for field %s: %w", field, err)
	}
	if h.metrics != nil && h.metrics.ProjectionDivergences != nil {
		h.metrics.ProjectionDivergences.WithLabelValues("stream_sessions_final", "stream_runtime_seconds", field).Inc()
	}
	return nil
}

func sourceRuntimeSeconds(startedAtMS, endedAtMS int64) float64 {
	if endedAtMS <= startedAtMS {
		return 0
	}
	return float64(endedAtMS-startedAtMS) / 1000.0
}

// processingSegmentFinalRow carries the rated fields used by the
// processing-segment divergence check.
type processingSegmentFinalRow struct {
	tenantID                 uuid.UUID
	nodeID                   string
	streamID                 uuid.UUID
	processType, outputCodec string
	trackType                string
	sourceEventID            string
	clusterID                string
	mediaSeconds             float64
}

type processingDivergence struct {
	field string
	prior any
	newer any
}

// checkProcessingSegmentDivergence mirrors the viewer guardrail for
// processing_segments_final. source_event_id is the logical fact identity;
// process_type, output_codec, track_type, media_seconds, and cluster_id are
// materialized fields that can change if parser attribution is corrected.
func (h *AnalyticsHandler) checkProcessingSegmentDivergence(ctx context.Context, row processingSegmentFinalRow) error {
	rows, err := h.clickhouse.Query(ctx, `
		SELECT
			argMax(process_type,   projection_version_ms),
			argMax(output_codec,   projection_version_ms),
			argMax(track_type,     projection_version_ms),
			argMax(media_seconds, projection_version_ms),
			argMax(cluster_id,    projection_version_ms)
		FROM periscope.processing_segments_final
		WHERE tenant_id = ? AND node_id = ? AND stream_id = ?
		  AND source_event_id = ?
		GROUP BY tenant_id, node_id, stream_id, source_event_id`,
		row.tenantID, row.nodeID, row.streamID, row.sourceEventID)
	if err != nil {
		return fmt.Errorf("lookup prior processing segment projection: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate prior processing segment projection: %w", err)
		}
		return nil
	}
	var priorProcessType, priorOutputCodec, priorTrackType string
	var priorMediaSeconds float64
	var priorCluster string
	if scanErr := rows.Scan(&priorProcessType, &priorOutputCodec, &priorTrackType, &priorMediaSeconds, &priorCluster); scanErr != nil {
		return fmt.Errorf("scan prior processing segment projection: %w", scanErr)
	}

	var found []processingDivergence
	const mediaSecondsEpsilon = 0.05 // 50ms
	identityChanged := priorProcessType != row.processType || priorOutputCodec != row.outputCodec || priorTrackType != row.trackType
	if identityChanged {
		found = append(found, processingDivergence{
			"identity",
			map[string]any{
				"cluster_id":      priorCluster,
				"media_seconds":   priorMediaSeconds,
				"process_type":    priorProcessType,
				"output_codec":    priorOutputCodec,
				"track_type":      priorTrackType,
				"source_event_id": row.sourceEventID,
			},
			map[string]any{
				"cluster_id":      row.clusterID,
				"media_seconds":   row.mediaSeconds,
				"process_type":    row.processType,
				"output_codec":    row.outputCodec,
				"track_type":      row.trackType,
				"source_event_id": row.sourceEventID,
			},
		})
		return h.recordProcessingSegmentDivergences(ctx, row, found)
	}
	clusterChanged := priorCluster != row.clusterID
	if clusterChanged {
		found = append(found, processingDivergence{
			"cluster_id",
			map[string]any{
				"cluster_id":      priorCluster,
				"media_seconds":   priorMediaSeconds,
				"process_type":    row.processType,
				"output_codec":    row.outputCodec,
				"source_event_id": row.sourceEventID,
			},
			map[string]any{
				"cluster_id":      row.clusterID,
				"media_seconds":   row.mediaSeconds,
				"process_type":    row.processType,
				"output_codec":    row.outputCodec,
				"source_event_id": row.sourceEventID,
			},
		})
	} else if absDeltaFloat64(priorMediaSeconds, row.mediaSeconds) >= mediaSecondsEpsilon {
		found = append(found, processingDivergence{"media_seconds", priorMediaSeconds, row.mediaSeconds})
	}
	if len(found) == 0 {
		return nil
	}
	return h.recordProcessingSegmentDivergences(ctx, row, found)
}

func (h *AnalyticsHandler) recordProcessingSegmentDivergences(ctx context.Context, row processingSegmentFinalRow, found []processingDivergence) error {
	naturalKey := map[string]string{
		"tenant_id":       row.tenantID.String(),
		"node_id":         row.nodeID,
		"stream_id":       row.streamID.String(),
		"cluster_id":      row.clusterID,
		"process_type":    row.processType,
		"output_codec":    row.outputCodec,
		"track_type":      row.trackType,
		"source_event_id": row.sourceEventID,
	}
	naturalKeyJSON, err := json.Marshal(naturalKey)
	if err != nil {
		return fmt.Errorf("marshal processing segment divergence natural key: %w", err)
	}
	observedAtMS := time.Now().UnixMilli()
	for _, d := range found {
		priorJSON, mErr := json.Marshal(d.prior)
		if mErr != nil {
			return fmt.Errorf("marshal processing segment divergence prior value: %w", mErr)
		}
		newJSON, mErr := json.Marshal(d.newer)
		if mErr != nil {
			return fmt.Errorf("marshal processing segment divergence new value: %w", mErr)
		}
		if rErr := h.recordProjectionDivergence(ctx, observedAtMS, "processing_segments_final", "media_seconds", d.field, string(naturalKeyJSON), string(priorJSON), string(newJSON), row.sourceEventID); rErr != nil {
			return fmt.Errorf("record processing segment divergence for field %s: %w", d.field, rErr)
		}
		if h.metrics != nil && h.metrics.ProjectionDivergences != nil {
			h.metrics.ProjectionDivergences.WithLabelValues("processing_segments_final", "media_seconds", d.field).Inc()
		}
	}
	return nil
}

func (h *AnalyticsHandler) recordProjectionDivergence(ctx context.Context, observedAtMS int64, tableName, meter, field, naturalKeyJSON, priorJSON, newJSON, sourceEventID string) error {
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.projection_divergences (
			observed_at_ms, table_name, meter, field,
			natural_key_json, prior_value_json, new_value_json, source_event_id
		)`)
	if err != nil {
		return err
	}
	defer closeClickHouseBatch(batch)
	if err := batch.Append(observedAtMS, tableName, meter, field, naturalKeyJSON, priorJSON, newJSON, sourceEventID); err != nil {
		return err
	}
	return batch.Send()
}

// --- helpers ---

func parseTenantID(s string) (uuid.UUID, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return uuid.Nil, false
	}
	t, err := uuid.Parse(s)
	if err != nil || t == uuid.Nil {
		return uuid.Nil, false
	}
	return t, true
}

type streamStartLookup struct {
	tenantID     uuid.UUID
	streamID     uuid.UUID
	nodeID       string
	clusterID    string
	internalName string
}

func (h *AnalyticsHandler) lookupStreamStartedAtMS(ctx context.Context, lookup streamStartLookup, fallbackEndedAtMS int64) int64 {
	startedFromEvents := h.lookupStreamStartedAtMSFromEventLog(ctx, lookup, fallbackEndedAtMS)
	if startedFromEvents > 0 && startedFromEvents < fallbackEndedAtMS {
		return startedFromEvents
	}

	endedAt := time.UnixMilli(fallbackEndedAtMS).UTC()
	rows, err := h.clickhouse.Query(ctx, `
		SELECT toUnixTimestamp(ifNull(started_at, updated_at)) * 1000
		FROM periscope.stream_state_current FINAL
		WHERE tenant_id = ? AND stream_id = ?
		  AND started_at IS NOT NULL
		  AND updated_at <= ?
		  AND (? = '' OR node_id = ?)
		  AND (? = '' OR internal_name = ?)
		LIMIT 1`,
		lookup.tenantID, lookup.streamID, endedAt,
		lookup.nodeID, lookup.nodeID,
		lookup.internalName, lookup.internalName)
	if err != nil {
		h.logger.WithError(err).Debug("Stream start current-state lookup failed")
		return 0
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0
	}
	var startedAtMS int64
	if err := rows.Scan(&startedAtMS); err != nil {
		h.logger.WithError(err).Debug("Stream start current-state lookup scan failed")
		return 0
	}
	if startedAtMS <= 0 || startedAtMS > fallbackEndedAtMS {
		return startedFromEvents
	}
	return startedAtMS
}

func (h *AnalyticsHandler) lookupStreamStartedAtMSFromEventLog(ctx context.Context, lookup streamStartLookup, fallbackEndedAtMS int64) int64 {
	type filterScope struct {
		nodeID       string
		clusterID    string
		internalName string
	}
	scopes := []filterScope{
		{nodeID: lookup.nodeID, clusterID: lookup.clusterID, internalName: lookup.internalName},
		{nodeID: lookup.nodeID, clusterID: lookup.clusterID},
		{nodeID: lookup.nodeID, internalName: lookup.internalName},
		{clusterID: lookup.clusterID, internalName: lookup.internalName},
		{nodeID: lookup.nodeID},
		{clusterID: lookup.clusterID},
		{internalName: lookup.internalName},
		{},
	}
	seen := make(map[filterScope]struct{}, len(scopes))
	for _, scope := range scopes {
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		if startedAtMS := h.lookupStreamStartedAtMSFromEventLogScope(ctx, lookup, fallbackEndedAtMS, scope.nodeID, scope.clusterID, scope.internalName); startedAtMS > 0 && startedAtMS < fallbackEndedAtMS {
			return startedAtMS
		}
	}
	return 0
}

func (h *AnalyticsHandler) lookupStreamStartedAtMSFromEventLogScope(ctx context.Context, lookup streamStartLookup, fallbackEndedAtMS int64, nodeID, clusterID, internalName string) int64 {
	endedAt := time.UnixMilli(fallbackEndedAtMS).UTC()
	rows, err := h.clickhouse.Query(ctx, `
		SELECT toUnixTimestamp(min(timestamp)) * 1000
		FROM periscope.stream_event_log
		WHERE tenant_id = ?
		  AND stream_id = ?
		  AND (? = '' OR node_id = ?)
		  AND (? = '' OR cluster_id = ?)
		  AND (? = '' OR internal_name = ?)
		  AND timestamp <= ?
		  AND timestamp > ifNull((
		      SELECT max(timestamp)
		      FROM periscope.stream_event_log
		      WHERE tenant_id = ?
		        AND stream_id = ?
		        AND (? = '' OR node_id = ?)
		        AND (? = '' OR cluster_id = ?)
		        AND (? = '' OR internal_name = ?)
		        AND event_type = 'stream_end'
		        AND timestamp < ?
		  ), toDateTime(0))
		  AND (
		      event_type = 'stream_start'
		      OR (event_type IN ('stream_lifecycle', 'stream_buffer', 'track_list_update') AND status = 'live')
		  )
		LIMIT 1`,
		lookup.tenantID, lookup.streamID,
		nodeID, nodeID,
		clusterID, clusterID,
		internalName, internalName,
		endedAt,
		lookup.tenantID, lookup.streamID,
		nodeID, nodeID,
		clusterID, clusterID,
		internalName, internalName,
		endedAt)
	if err != nil {
		h.logger.WithError(err).Debug("Stream start event-log lookup failed")
		return 0
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0
	}
	var startedAtMS int64
	if err := rows.Scan(&startedAtMS); err != nil {
		h.logger.WithError(err).Debug("Stream start event-log lookup scan failed")
		return 0
	}
	if startedAtMS <= 0 || startedAtMS > fallbackEndedAtMS {
		return 0
	}
	return startedAtMS
}

func normalizeCountryCode(cc string) string {
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if len(cc) == 0 {
		return "\x00\x00"
	}
	if len(cc) == 1 {
		return cc + "\x00"
	}
	return cc[:2]
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func absDeltaUint32(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

func absDeltaUint64(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

func absDeltaFloat64(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

// sessionTimeSharesToTuples turns the proto's repeated SessionTimeShare into
// the [][]any shape the ClickHouse driver writes into
// Array(Tuple(name LowCardinality(String), seconds UInt32)). Returns nil if
// the input is empty so the column default ([]) applies.
func sessionTimeSharesToTuples(shares []*ipcpb.SessionTimeShare) [][]any {
	if len(shares) == 0 {
		return nil
	}
	out := make([][]any, 0, len(shares))
	for _, s := range shares {
		if s == nil {
			continue
		}
		out = append(out, []any{s.GetName(), s.GetSeconds()})
	}
	return out
}
