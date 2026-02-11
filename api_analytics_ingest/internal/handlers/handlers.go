package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/pkg/database"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// PeriscopeMetrics holds all Prometheus metrics for Periscope Ingest
type PeriscopeMetrics struct {
	AnalyticsEvents         *prometheus.CounterVec
	BatchProcessingDuration *prometheus.HistogramVec
	ClickHouseInserts       *prometheus.CounterVec
	DuplicateEvents         *prometheus.CounterVec
	DLQMessages             *prometheus.CounterVec
	KafkaMessages           *prometheus.CounterVec
	KafkaDuration           *prometheus.HistogramVec
	KafkaLag                *prometheus.GaugeVec
}

// AnalyticsHandler handles analytics events
type AnalyticsHandler struct {
	clickhouse clickhouseConn
	logger     logging.Logger
	metrics    *PeriscopeMetrics
}

type clickhouseBatch interface {
	Append(v ...interface{}) error
	Send() error
}

type clickhouseRows interface {
	Next() bool
	Close() error
}

type clickhouseConn interface {
	PrepareBatch(ctx context.Context, query string) (clickhouseBatch, error)
	Query(ctx context.Context, query string, args ...interface{}) (clickhouseRows, error)
}

type clickhouseNativeConn struct {
	conn database.ClickHouseNativeConn
}

func (c clickhouseNativeConn) PrepareBatch(ctx context.Context, query string) (clickhouseBatch, error) {
	return c.conn.PrepareBatch(ctx, query)
}

func (c clickhouseNativeConn) Query(ctx context.Context, query string, args ...interface{}) (clickhouseRows, error) {
	return c.conn.Query(ctx, query, args...)
}

// NewAnalyticsHandler creates a new analytics handler
func NewAnalyticsHandler(clickhouse database.ClickHouseNativeConn, logger logging.Logger, metrics *PeriscopeMetrics) *AnalyticsHandler {
	return &AnalyticsHandler{
		clickhouse: clickhouseNativeConn{conn: clickhouse},
		logger:     logger,
		metrics:    metrics,
	}
}

// parseProtobufData parses the transparent protobuf JSON data from the Kafka event
func (h *AnalyticsHandler) parseProtobufData(event kafka.AnalyticsEvent, target proto.Message) error {
	// Convert the Data map back to JSON
	jsonData, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

	// Parse JSON using protojson to maintain proper protobuf semantics
	unmarshaler := protojson.UnmarshalOptions{
		DiscardUnknown: false,
	}

	return unmarshaler.Unmarshal(jsonData, target)
}

// HandleAnalyticsEvent processes analytics events and writes to ClickHouse
func (h *AnalyticsHandler) HandleAnalyticsEvent(event kafka.AnalyticsEvent) error {
	start := time.Now()
	ctx := context.Background()

	// Track analytics event received
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "received").Inc()
		// Detect missing enrichment early.
		if !isValidUUIDString(event.TenantID) {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "tenant_missing").Inc()
		}
	}

	// Strict enforcement: drop + DLQ missing/invalid tenant_id
	if err := h.requireTenantID(ctx, event); err != nil {
		return err
	}

	// Process based on event type using direct protobuf parsing
	var err error
	switch event.EventType {
	// Unified naming (no legacy)
	case "viewer_connect":
		err = h.processViewerConnection(ctx, event, true)
	case "viewer_disconnect":
		err = h.processViewerConnection(ctx, event, false)
	case "stream_buffer":
		err = h.processStreamBuffer(ctx, event)
	case "stream_end":
		err = h.processStreamEnd(ctx, event)
	case "push_rewrite":
		err = h.processPushRewrite(ctx, event)
	case "play_rewrite":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "stream_source":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "push_end":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "push_out_start":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "stream_track_list":
		err = h.processTrackList(ctx, event)
	case "recording_complete":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "recording_segment":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "stream_lifecycle_update":
		err = h.processStreamLifecycle(ctx, event)
	case "node_lifecycle_update":
		err = h.processNodeLifecycle(ctx, event)
	case "client_lifecycle_update":
		err = h.processClientLifecycle(ctx, event)
	case "load_balancing":
		err = h.processLoadBalancing(ctx, event)
	case "clip_lifecycle":
		err = h.processClipLifecycle(ctx, event)
	case "dvr_lifecycle":
		err = h.processDVRLifecycle(ctx, event)
	case "storage_lifecycle":
		err = h.processStorageLifecycle(ctx, event)
	case "storage_snapshot":
		err = h.processStorageSnapshot(ctx, event)
	case "process_billing":
		err = h.processProcessBilling(ctx, event)
	case "vod_lifecycle":
		err = h.processVodLifecycle(ctx, event)
	case "api_request_batch":
		err = h.processAPIRequestBatch(ctx, event)
	default:
		h.logger.WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
		}).Info("Unknown event type, skipping")
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "skipped").Inc()
		}
		return nil
	}

	if err != nil {
		if errors.Is(err, errDropped) {
			return nil
		}
		if errors.Is(err, errMissingTenantID) {
			return err
		}
		h.writeIngestError(ctx, event, "", "handler_error", err)
		h.logger.WithError(err).WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
		}).Error("Failed to process event")
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "error").Inc()
		}
		return err
	}

	// Track success
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "processed").Inc()
		h.metrics.BatchProcessingDuration.WithLabelValues(event.Source).Observe(time.Since(start).Seconds())
	}

	return nil
}

// HandleServiceEvent processes service-plane events from the service_events topic.
func (h *AnalyticsHandler) HandleServiceEvent(event kafka.ServiceEvent) error {
	start := time.Now()
	ctx := context.Background()

	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "received").Inc()
	}

	var err error
	switch event.EventType {
	case "api_request_batch":
		err = h.processServiceAPIRequestBatch(ctx, event)
	case "tenant_created":
		if err = h.processTenantCreated(ctx, event); err != nil {
			break
		}
		err = h.processServiceEventAudit(ctx, event)
	default:
		err = h.processServiceEventAudit(ctx, event)
	}

	if err != nil {
		if errors.Is(err, errDropped) {
			return nil
		}
		h.logger.WithError(err).WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
		}).Error("Failed to process service event")
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "processed").Inc()
		h.metrics.BatchProcessingDuration.WithLabelValues(event.Source).Observe(time.Since(start).Seconds())
	}

	return nil
}

func (h *AnalyticsHandler) skipEvent(event kafka.AnalyticsEvent, reason string) error {
	fields := logging.Fields{
		"event_type": event.EventType,
		"event_id":   event.EventID,
	}
	if reason != "" {
		fields["reason"] = reason
	}
	h.logger.WithFields(fields).Info("Skipping analytics event")
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "skipped").Inc()
	}
	return nil
}

// processStorageSnapshot handles StorageSnapshot events
func (h *AnalyticsHandler) processStorageSnapshot(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing storage snapshot event: %s", event.EventID)

	// Parse MistTrigger envelope -> StorageSnapshot
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_StorageSnapshot)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for storage_snapshot")
	}
	storageSnapshot := tp.StorageSnapshot

	// Write to ClickHouse for each tenant's usage in the snapshot
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO storage_snapshots (
			timestamp, node_id, tenant_id, storage_scope,
			total_bytes, file_count, dvr_bytes, clip_bytes, vod_bytes,
			frozen_dvr_bytes, frozen_clip_bytes, frozen_vod_bytes
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch for storage_snapshots: %v", err)
		return err
	}

	storageScope := storageSnapshot.GetStorageScope()
	if storageScope == "" {
		storageScope = "hot"
	}

	snapshotTimestamp := event.Timestamp
	if ts := storageSnapshot.GetTimestamp(); ts > 0 {
		snapshotTimestamp = time.Unix(ts, 0)
	}

	for _, usage := range storageSnapshot.GetUsage() {
		if !isValidUUIDString(usage.GetTenantId()) {
			h.logger.WithFields(logging.Fields{
				"event_id":  event.EventID,
				"tenant_id": usage.GetTenantId(),
				"node_id":   storageSnapshot.GetNodeId(),
			}).Warn("Skipping storage snapshot row: missing or invalid tenant_id")
			continue
		}
		if err := batch.Append(
			snapshotTimestamp,
			storageSnapshot.GetNodeId(),
			usage.GetTenantId(),
			storageScope,
			usage.GetTotalBytes(),
			usage.GetFileCount(),
			usage.GetDvrBytes(),
			usage.GetClipBytes(),
			usage.GetVodBytes(),
			usage.GetFrozenDvrBytes(),
			usage.GetFrozenClipBytes(),
			usage.GetFrozenVodBytes(),
		); err != nil {
			h.logger.Errorf("Failed to append to storage_snapshots batch: %v", err)
			return err
		}
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send storage_snapshots batch: %v", err)
		return err
	}

	return nil
}

// processStreamLifecycle handles stream lifecycle events
// Dual-writes to: live_streams (current state) + stream_events (historical log)
func (h *AnalyticsHandler) processStreamLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> StreamLifecycleUpdate
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if !isValidUUIDString(mt.GetStreamId()) {
		h.logger.WithFields(logging.Fields{
			"event_id":  event.EventID,
			"tenant_id": event.TenantID,
			"stream_id": mt.GetStreamId(),
		}).Warn("Stream lifecycle event missing or invalid stream_id; skipping to avoid corrupting current state")
		return nil
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_StreamLifecycleUpdate)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for stream_lifecycle_update")
	}
	streamLifecycle := tp.StreamLifecycleUpdate
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(streamLifecycle.GetInternalName())

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "attempt").Inc()
	}

	// 1. Write to live_streams (current state - ReplacingMergeTree)
	// This is the primary source of truth for stream status
	stateBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_state_current (
			tenant_id, stream_id, internal_name, node_id, status, buffer_state,
			current_viewers, total_inputs, uploaded_bytes, downloaded_bytes,
			viewer_seconds, has_issues, issues_description,
			track_count, quality_tier, primary_width, primary_height,
			primary_fps, primary_codec, primary_bitrate,
			packets_sent, packets_lost, packets_retransmitted,
			started_at, updated_at
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_streams batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "error").Inc()
		}
		return err
	}

	// Derive status from buffer state
	status := "live"
	if streamLifecycle.GetStatus() != "" {
		status = streamLifecycle.GetStatus()
	}

	// Derive buffer_state if not set but buffer_ms is available.
	// live_streams.buffer_state is non-nullable; prefer a reasonable default over empty.
	bufferState := streamLifecycle.GetBufferState()
	if bufferState == "" && streamLifecycle.GetBufferMs() > 0 {
		bufferState = "FULL"
	}

	// Convert started_at unix timestamp to time.Time if present
	var startedAt interface{}
	if streamLifecycle.StartedAt != nil && *streamLifecycle.StartedAt > 0 {
		startedAt = time.Unix(*streamLifecycle.StartedAt, 0)
	}

	if appendErr := stateBatch.Append(
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetNodeId(),
		status,
		bufferState,
		streamLifecycle.GetTotalViewers(),
		uint16(streamLifecycle.GetTotalInputs()),
		streamLifecycle.GetUploadedBytes(),
		streamLifecycle.GetDownloadedBytes(),
		streamLifecycle.GetViewerSeconds(),
		nilIfZeroBool(streamLifecycle.GetHasIssues()),
		nilIfEmptyString(streamLifecycle.GetIssuesDescription()),
		nilIfZeroUint16(streamLifecycle.GetTrackCount()),
		nilIfEmptyString(streamLifecycle.GetQualityTier()),
		nilIfZeroUint16(streamLifecycle.GetPrimaryWidth()),
		nilIfZeroUint16(streamLifecycle.GetPrimaryHeight()),
		nilIfZeroFloat32(streamLifecycle.GetPrimaryFps()),
		nilIfEmptyString(streamLifecycle.GetPrimaryCodec()),
		nilIfZeroUint32(uint32(streamLifecycle.GetPrimaryBitrate())),
		valueOrNilUint64Ptr(streamLifecycle.PacketsSent),
		valueOrNilUint64Ptr(streamLifecycle.PacketsLost),
		valueOrNilUint64Ptr(streamLifecycle.PacketsRetransmitted),
		startedAt,
		event.Timestamp,
	); appendErr != nil {
		h.logger.Errorf("Failed to append to live_streams batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_streams batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "attempt").Inc()
	}

	// 2. Write to stream_events (historical log - MergeTree)
	eventBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_event_log (
			timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status,
			buffer_state, downloaded_bytes, uploaded_bytes, total_viewers, total_inputs,
			total_outputs, viewer_seconds, has_issues, issues_description,
			track_count, quality_tier, primary_width, primary_height, primary_fps, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_events batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return err
	}

	if appendErr := eventBatch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetNodeId(),
		mt.GetClusterId(),
		"stream_lifecycle",
		status,
		streamLifecycle.GetBufferState(),
		streamLifecycle.GetDownloadedBytes(),
		streamLifecycle.GetUploadedBytes(),
		streamLifecycle.GetTotalViewers(),
		streamLifecycle.GetTotalInputs(),
		0, // total_outputs not in StreamLifecycleUpdate
		streamLifecycle.GetViewerSeconds(),
		nilIfZeroBool(streamLifecycle.GetHasIssues()),
		nilIfEmptyString(streamLifecycle.GetIssuesDescription()),
		nilIfZeroUint16(streamLifecycle.GetTrackCount()),
		nilIfEmptyString(streamLifecycle.GetQualityTier()),
		nilIfZeroUint16(streamLifecycle.GetPrimaryWidth()),
		nilIfZeroUint16(streamLifecycle.GetPrimaryHeight()),
		nilIfZeroFloat32(streamLifecycle.GetPrimaryFps()),
		marshalTypedEventData(&streamLifecycle),
	); appendErr != nil {
		h.logger.Errorf("Failed to append to stream_events batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return appendErr
	}

	if sendErr := eventBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send stream_events batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "success").Inc()
	}

	// 3. Write to stream_health_metrics (for health charts - every 10s sample)
	// This provides continuous health data from polling, not just sparse STREAM_BUFFER triggers
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "attempt").Inc()
	}

	healthBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_health_samples (
			timestamp, tenant_id, stream_id, internal_name, node_id,
			bitrate, fps, width, height, codec, quality_tier,
			buffer_state, buffer_size, buffer_health,
			has_issues, issues_description, track_count,
			track_metadata,
			audio_channels, audio_sample_rate, audio_codec, audio_bitrate
	)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_health_metrics batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "error").Inc()
		}
		return err
	}

	// Calculate buffer_health ratio (0.0-1.0): buffer_ms / max_keepaway_ms.
	var bufferHealth interface{}
	if streamLifecycle.GetBufferMs() > 0 && streamLifecycle.GetMaxKeepawayMs() > 0 {
		ratio := float32(streamLifecycle.GetBufferMs()) / float32(streamLifecycle.GetMaxKeepawayMs())
		if ratio > 1 {
			ratio = 1
		}
		bufferHealth = ratio
	}

	// ClickHouse JSON type expects an object at the top level. Store track details under { "tracks": [...] }.
	trackMetadataJSON := "{}"
	if raw := strings.TrimSpace(streamLifecycle.GetTrackDetailsJson()); raw != "" {
		if strings.HasPrefix(raw, "{") {
			trackMetadataJSON = raw
		} else if strings.HasPrefix(raw, "[") {
			trackMetadataJSON = fmt.Sprintf("{\"tracks\":%s}", raw)
		}
	}

	var audioChannels interface{}
	if v := streamLifecycle.GetAudioChannels(); v > 0 {
		audioChannels = uint8(v)
	}

	if err := healthBatch.Append(
		event.Timestamp,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetNodeId(),
		nilIfZeroUint32(uint32(streamLifecycle.GetPrimaryBitrate())),
		nilIfZeroFloat32(streamLifecycle.GetPrimaryFps()),
		nilIfZeroUint16(streamLifecycle.GetPrimaryWidth()),
		nilIfZeroUint16(streamLifecycle.GetPrimaryHeight()),
		nilIfEmptyString(streamLifecycle.GetPrimaryCodec()),
		nilIfEmptyString(streamLifecycle.GetQualityTier()),
		bufferState,
		nilIfZeroUint32(streamLifecycle.GetBufferMs()),
		bufferHealth,
		nilIfZeroBool(streamLifecycle.GetHasIssues()),
		nilIfEmptyString(streamLifecycle.GetIssuesDescription()),
		nilIfZeroUint16(streamLifecycle.GetTrackCount()),
		trackMetadataJSON,
		audioChannels,
		nilIfZeroUint32(streamLifecycle.GetAudioSampleRate()),
		nilIfEmptyString(streamLifecycle.GetAudioCodec()),
		nilIfZeroUint32(streamLifecycle.GetAudioBitrate()),
	); err != nil {
		h.logger.Errorf("Failed to append to stream_health_metrics: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "error").Inc()
		}
		return err
	}

	if err := healthBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send stream_health_metrics: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "success").Inc()
	}

	return nil
}

// processViewerConnection writes connection_events (connect/disconnect) to ClickHouse
func (h *AnalyticsHandler) processViewerConnection(ctx context.Context, event kafka.AnalyticsEvent, isConnect bool) error {
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mt.GetStreamId()); err != nil {
		return err
	}
	if h.isDuplicateEvent(ctx, "viewer_connection_events", parseUUID(event.EventID), event.EventType) {
		return nil
	}

	var streamName, sessionID, connector, nodeID, host, requestURL string
	var duration, upBytes, downBytes int64
	var secondsConnected uint64
	countryCode := "--"
	city := ""
	latitude := float64(0)
	longitude := float64(0)
	var clientBucketH3 interface{}
	var clientBucketRes interface{}
	var nodeBucketH3 interface{}
	var nodeBucketRes interface{}

	payloadIsConnect := false
	payloadType := ""
	switch p := mt.GetTriggerPayload().(type) {
	case *pb.MistTrigger_ViewerConnect:
		payloadIsConnect = true
		payloadType = "viewer_connect"
		vc := p.ViewerConnect
		streamName = vc.GetStreamName()
		sessionID = vc.GetSessionId()
		connector = vc.GetConnector()
		host = vc.GetHost()
		requestURL = vc.GetRequestUrl()
		nodeID = mt.GetNodeId()
		if vc.GetClientCountry() != "" {
			countryCode = vc.GetClientCountry()
		}
		if vc.GetClientCity() != "" {
			city = vc.GetClientCity()
		}
		if vc.GetClientLatitude() != 0 {
			latitude = vc.GetClientLatitude()
		}
		if vc.GetClientLongitude() != 0 {
			longitude = vc.GetClientLongitude()
		}
		if bucket := vc.GetClientBucket(); bucket != nil && bucket.H3Index != 0 {
			clientBucketH3 = bucket.H3Index
			clientBucketRes = uint8(bucket.Resolution)
		}
		if bucket := vc.GetNodeBucket(); bucket != nil && bucket.H3Index != 0 {
			nodeBucketH3 = bucket.H3Index
			nodeBucketRes = uint8(bucket.Resolution)
		}
	case *pb.MistTrigger_ViewerDisconnect:
		payloadIsConnect = false
		payloadType = "viewer_disconnect"
		vd := p.ViewerDisconnect
		streamName = vd.GetStreamName()
		sessionID = vd.GetSessionId()
		connector = vd.GetConnector()
		host = vd.GetHost()
		nodeID = vd.GetNodeId()
		duration = vd.GetDuration()
		secondsConnected = vd.GetSecondsConnected()
		upBytes = vd.GetUpBytes()
		downBytes = vd.GetDownBytes()
		if vd.GetCountryCode() != "" {
			countryCode = vd.GetCountryCode()
		}
		if vd.GetCity() != "" {
			city = vd.GetCity()
		}
		if vd.GetLatitude() != 0 {
			latitude = vd.GetLatitude()
		}
		if vd.GetLongitude() != 0 {
			longitude = vd.GetLongitude()
		}
		if bucket := vd.GetClientBucket(); bucket != nil && bucket.H3Index != 0 {
			clientBucketH3 = bucket.H3Index
			clientBucketRes = uint8(bucket.Resolution)
		}
		if bucket := vd.GetNodeBucket(); bucket != nil && bucket.H3Index != 0 {
			nodeBucketH3 = bucket.H3Index
			nodeBucketRes = uint8(bucket.Resolution)
		}
	default:
		return fmt.Errorf("unexpected payload for viewer connection")
	}
	if payloadIsConnect != isConnect {
		expectedType := map[bool]string{true: "viewer_connect", false: "viewer_disconnect"}[isConnect]
		return fmt.Errorf("viewer connection payload mismatch: expected %s, got %s", expectedType, payloadType)
	}

	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	streamName = mist.ExtractInternalName(streamName)

	clusterID := mt.GetClusterId()
	originClusterID := mt.GetOriginClusterId()

	batch, err := h.clickhouse.PrepareBatch(ctx, `
        INSERT INTO viewer_connection_events (
            event_id, timestamp, tenant_id, stream_id, internal_name,
            session_id, connection_addr, connector, node_id,
            cluster_id, origin_cluster_id,
            request_url,
            country_code, city, latitude, longitude,
            client_bucket_h3, client_bucket_res, node_bucket_h3, node_bucket_res,
            event_type, session_duration, bytes_transferred
        )`)
	if err != nil {
		return err
	}

	eventType := map[bool]string{true: "connect", false: "disconnect"}[isConnect]
	durationUI := uint32(0)
	bytesTransferred := uint64(0)
	if !isConnect {
		if duration > 0 {
			durationUI = uint32(duration)
		} else if secondsConnected > 0 {
			durationUI = uint32(secondsConnected)
		}
		bytesTransferred = uint64(max64(0, upBytes) + max64(0, downBytes))
	}

	if err := batch.Append(
		parseUUID(event.EventID),
		event.Timestamp,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		streamName,
		sessionID,
		host,
		connector,
		nodeID,
		clusterID,
		originClusterID,
		requestURL,
		countryCode,
		city,
		latitude,
		longitude,
		clientBucketH3,
		clientBucketRes,
		nodeBucketH3,
		nodeBucketRes,
		eventType,
		durationUI,
		bytesTransferred,
	); err != nil {
		return err
	}
	return batch.Send()
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func nilIfZeroFloat32(v float32) interface{} {
	if v == 0 {
		return nil
	}
	return v
}
func nilIfZeroBool(v bool) interface{} {
	if !v {
		return nil
	}
	return v
}
func nilIfZeroUint64(v uint64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}
func nilIfZeroUint32(v uint32) interface{} {
	if v == 0 {
		return nil
	}
	return v
}
func nilIfZeroUint8(v int32) interface{} {
	// Proto returns int32, convert to uint8 for ClickHouse
	if v == 0 {
		return nil
	}
	return uint8(v)
}
func nilIfZeroUint16(v int32) interface{} {
	// Proto returns int32, convert to uint16 for ClickHouse
	if v == 0 {
		return nil
	}
	return uint16(v)
}
func nilIfEmptyString(v string) interface{} {
	if v == "" {
		return nil
	}
	return v
}
func parseUUID(value string) uuid.UUID {
	if value == "" {
		return uuid.Nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}

func parseUUIDOrNil(value string) interface{} {
	if value == "" {
		return nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil
	}
	if parsed == uuid.Nil {
		return nil
	}
	return parsed
}

func (h *AnalyticsHandler) isDuplicateEvent(ctx context.Context, table string, eventID uuid.UUID, eventType string) bool {
	if eventID == uuid.Nil {
		return false
	}
	rows, err := h.clickhouse.Query(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE event_id = ? LIMIT 1", table), eventID)
	if err != nil {
		h.logger.WithError(err).WithField("event_id", eventID).Warn("Failed to check for duplicate event")
		return false
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		h.logger.WithField("event_id", eventID).WithField("table", table).Debug("Skipping duplicate event")
		if h.metrics != nil && h.metrics.DuplicateEvents != nil {
			h.metrics.DuplicateEvents.WithLabelValues(eventType).Inc()
		}
		return true
	}
	return false
}

func isValidUUIDString(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return false
	}
	return parsed != uuid.Nil
}

func getStringFromMap(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	value, ok := data[key]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func getInt64FromMap(data map[string]interface{}, key string) (int64, bool) {
	if data == nil {
		return 0, false
	}
	value, ok := data[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case float32:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func getUint64FromMap(data map[string]interface{}, key string) uint64 {
	if data == nil {
		return 0
	}
	value, ok := data[key]
	if !ok {
		return 0
	}
	switch v := value.(type) {
	case uint64:
		return v
	case uint32:
		return uint64(v)
	case int64:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case int:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case float64:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case float32:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil || n < 0 {
			return 0
		}
		return uint64(n)
	default:
		return 0
	}
}

var (
	errDropped         = errors.New("dropped")
	errMissingTenantID = errors.New("missing_or_invalid_tenant_id")
)

func (h *AnalyticsHandler) writeIngestError(ctx context.Context, event kafka.AnalyticsEvent, streamID string, reason string, cause error) {
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "attempt").Inc()
	}

	payloadJSON := "{}"
	if event.Data != nil {
		if bytes, err := json.Marshal(event.Data); err == nil {
			payloadJSON = string(bytes)
		} else {
			// Preserve reason but note payload marshal failure.
			reason = fmt.Sprintf("%s (payload_marshal_error: %v)", reason, err)
		}
	}

	errorMessage := reason
	if cause != nil {
		errorMessage = fmt.Sprintf("%s: %v", reason, cause)
	}

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO ingest_errors (
			received_at, event_id, event_type, source, tenant_id, stream_id, error, payload_json
		)`)
	if err != nil {
		h.logger.WithError(err).Error("Failed to prepare ingest_errors batch")
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "error").Inc()
		}
		return
	}

	if appendErr := batch.Append(
		event.Timestamp,
		event.EventID,
		event.EventType,
		event.Source,
		event.TenantID,
		streamID,
		errorMessage,
		payloadJSON,
	); appendErr != nil {
		h.logger.WithError(appendErr).Error("Failed to append ingest_errors batch")
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "error").Inc()
		}
		return
	}

	if err := batch.Send(); err != nil {
		h.logger.WithError(err).Error("Failed to send ingest_errors batch")
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "error").Inc()
		}
		return
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "success").Inc()
	}
}

func (h *AnalyticsHandler) requireTenantID(ctx context.Context, event kafka.AnalyticsEvent) error {
	if isValidUUIDString(event.TenantID) {
		return nil
	}

	h.writeIngestError(ctx, event, "", "missing_or_invalid_tenant_id", nil)
	h.logger.WithFields(logging.Fields{
		"event_type": event.EventType,
		"event_id":   event.EventID,
		"tenant_id":  event.TenantID,
	}).Warn("Dropping analytics event: missing or invalid tenant_id")
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "dropped").Inc()
	}
	return errMissingTenantID
}

func (h *AnalyticsHandler) requireStreamID(ctx context.Context, event kafka.AnalyticsEvent, streamID string) error {
	if isValidUUIDString(streamID) {
		return nil
	}

	h.writeIngestError(ctx, event, streamID, "missing_or_invalid_stream_id", nil)
	h.logger.WithFields(logging.Fields{
		"event_type": event.EventType,
		"event_id":   event.EventID,
		"tenant_id":  event.TenantID,
		"stream_id":  streamID,
	}).Warn("Dropping analytics event: missing or invalid stream_id")
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "dropped").Inc()
	}
	return errDropped
}

// processPushRewrite handles PUSH_REWRITE events (publisher ingest start)
func (h *AnalyticsHandler) processPushRewrite(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing push rewrite event: %s", event.EventID)
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_PushRewrite)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for push_rewrite")
	}
	pr := tp.PushRewrite
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(pr.GetStreamName())

	batch, err := h.clickhouse.PrepareBatch(ctx, `
        INSERT INTO stream_event_log (
            timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status,
            stream_key, request_url, protocol,
            latitude, longitude, location, country_code, city,
            event_data
        )`)
	if err != nil {
		return err
	}

	var prot interface{}
	if pr.Protocol != nil && *pr.Protocol != "" {
		prot = *pr.Protocol
	}
	// Prefer publisher geo (client-side) when available; otherwise fall back to node geo.
	var lat interface{}
	if pr.PublisherLatitude != nil && *pr.PublisherLatitude != 0 {
		lat = *pr.PublisherLatitude
	} else if pr.Latitude != nil && *pr.Latitude != 0 {
		lat = *pr.Latitude
	}
	var lon interface{}
	if pr.PublisherLongitude != nil && *pr.PublisherLongitude != 0 {
		lon = *pr.PublisherLongitude
	} else if pr.Longitude != nil && *pr.Longitude != 0 {
		lon = *pr.Longitude
	}
	// Publisher location (where encoder is running, from GeoIP)
	var pubCountry interface{}
	if pr.PublisherCountryCode != nil && *pr.PublisherCountryCode != "" {
		pubCountry = *pr.PublisherCountryCode
	}
	var pubCity interface{}
	if pr.PublisherCity != nil && *pr.PublisherCity != "" {
		pubCity = *pr.PublisherCity
	}

	if appendErr := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetNodeId(),
		mt.GetClusterId(),
		"stream_start",
		"live",
		pr.GetStreamName(), // Original stream_key for reference
		pr.GetPushUrl(),
		prot,
		lat,
		lon,
		nil, // location reserved for client geo; node location is in event_data
		pubCountry,
		pubCity,
		marshalTypedEventData(pr),
	); appendErr != nil {
		return appendErr
	}
	return batch.Send()
}

// processLoadBalancing handles load balancing events
func (h *AnalyticsHandler) processLoadBalancing(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing load balancing event: %s", event.EventID)

	// Parse MistTrigger envelope
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mt.GetStreamId()); err != nil {
		return err
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_LoadBalancingData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for load_balancing")
	}
	loadBalancing := tp.LoadBalancingData
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(loadBalancing.GetInternalName())

	// Write to ClickHouse routing_events table - using ACTUAL fields from LoadBalancingPayload
	batch, err := h.clickhouse.PrepareBatch(ctx, `
        INSERT INTO routing_decisions (
            timestamp, tenant_id, stream_id, internal_name, selected_node, status, details, score,
            client_ip, client_country, client_latitude, client_longitude, client_bucket_h3, client_bucket_res,
            node_latitude, node_longitude, node_name, node_bucket_h3, node_bucket_res,
            selected_node_id, routing_distance_km,
            stream_tenant_id, cluster_id, latency_ms,
            candidates_count, event_type, source
        )`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	var selID interface{}
	if loadBalancing.SelectedNodeId != nil && *loadBalancing.SelectedNodeId != "" {
		selID = *loadBalancing.SelectedNodeId
	}
	var routeKm interface{}
	if loadBalancing.RoutingDistanceKm != nil && *loadBalancing.RoutingDistanceKm != 0 {
		routeKm = *loadBalancing.RoutingDistanceKm
	}

	var clientBucketH3 interface{}
	var clientBucketRes interface{}
	if loadBalancing.ClientBucket != nil && loadBalancing.ClientBucket.H3Index != 0 {
		clientBucketH3 = loadBalancing.ClientBucket.H3Index
		clientBucketRes = uint8(loadBalancing.ClientBucket.Resolution)
	}
	var nodeBucketH3 interface{}
	var nodeBucketRes interface{}
	if loadBalancing.NodeBucket != nil && loadBalancing.NodeBucket.H3Index != 0 {
		nodeBucketH3 = loadBalancing.NodeBucket.H3Index
		nodeBucketRes = uint8(loadBalancing.NodeBucket.Resolution)
	}

	// Dual-tenant attribution (RFC: routing-events-dual-tenant-attribution)
	var streamTenantID interface{}
	if loadBalancing.StreamTenantId != nil && *loadBalancing.StreamTenantId != "" {
		if parsed, err := uuid.Parse(*loadBalancing.StreamTenantId); err == nil {
			streamTenantID = parsed
		}
	}
	clusterID := loadBalancing.GetClusterId()
	var candidatesCount interface{}
	if loadBalancing.CandidatesCount != nil && *loadBalancing.CandidatesCount > 0 {
		candidatesCount = int32(*loadBalancing.CandidatesCount)
	}
	eventType := loadBalancing.GetEventType()
	source := loadBalancing.GetSource()

	clientCountry := loadBalancing.GetClientCountry()
	if clientCountry == "" {
		clientCountry = "--"
	}

	if appendErr := batch.Append(
		event.Timestamp,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		loadBalancing.GetSelectedNode(),
		loadBalancing.GetStatus(),
		loadBalancing.GetDetails(),
		int64(loadBalancing.GetScore()),
		loadBalancing.GetClientIp(),
		clientCountry,
		loadBalancing.GetLatitude(),
		loadBalancing.GetLongitude(),
		clientBucketH3,
		clientBucketRes,
		loadBalancing.GetNodeLatitude(),
		loadBalancing.GetNodeLongitude(),
		loadBalancing.GetNodeName(),
		nodeBucketH3,
		nodeBucketRes,
		selID,
		routeKm,
		streamTenantID,
		clusterID,
		loadBalancing.GetLatencyMs(),
		candidatesCount,
		nilIfEmptyString(eventType),
		nilIfEmptyString(source),
	); appendErr != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", appendErr)
		return appendErr
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	// Load balancing events go ONLY to routing_events table
	// They represent routing requests, NOT viewers
	// Viewer counts come from USER_NEW/USER_END (connection_events)

	h.logger.WithFields(logging.Fields{
		"event_id":    event.EventID,
		"stream_name": loadBalancing.GetInternalName(),
	}).Info("Processed load balancing event")

	return nil
}

// processClientLifecycle handles per-client bandwidth and connection metrics
func (h *AnalyticsHandler) processClientLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing client lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> ClientLifecycleUpdate
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_ClientLifecycleUpdate)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for client_lifecycle_update")
	}
	clientLifecycle := tp.ClientLifecycleUpdate
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(clientLifecycle.GetInternalName())

	// Calculate connection quality if packets were sent
	var connectionQuality *float32
	if clientLifecycle.GetPacketsSent() > 0 {
		quality := float32(1.0 - (float64(clientLifecycle.GetPacketsLost()) / float64(clientLifecycle.GetPacketsSent())))
		connectionQuality = &quality
	}

	// Write to client_metrics table (not stream_health_metrics)
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO client_qoe_samples (
			timestamp, tenant_id, stream_id, internal_name, session_id, node_id, protocol, host,
			connection_time, position, bandwidth_in, bandwidth_out, bytes_downloaded, bytes_uploaded,
			packets_sent, packets_lost, packets_retransmitted, connection_quality
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if appendErr := batch.Append(
		event.Timestamp,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		clientLifecycle.GetSessionId(),
		mt.GetNodeId(),
		clientLifecycle.GetProtocol(),
		clientLifecycle.GetHost(),
		clientLifecycle.GetConnectionTime(),
		clientLifecycle.GetPosition(),
		uint64(clientLifecycle.GetBandwidthInBps()),
		uint64(clientLifecycle.GetBandwidthOutBps()),
		uint64(clientLifecycle.GetBytesDownloaded()),
		uint64(clientLifecycle.GetBytesUploaded()),
		uint64(clientLifecycle.GetPacketsSent()),
		uint64(clientLifecycle.GetPacketsLost()),
		uint64(clientLifecycle.GetPacketsRetransmitted()),
		connectionQuality,
	); appendErr != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", appendErr)
		return appendErr
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	// Note: viewer_metrics are NOT created from client-lifecycle events
	// Client-lifecycle tracks per-client performance, not viewers
	// Viewer counts come from USER_NEW/USER_END (connection_events)

	return nil
}

// processNodeLifecycle handles node health and resource metrics
// Dual-writes to: live_nodes (current state) + node_metrics (historical log)
func (h *AnalyticsHandler) processNodeLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing node lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> NodeLifecycleUpdate
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_NodeLifecycleUpdate)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for node_lifecycle_update")
	}
	nodeLifecycle := tp.NodeLifecycleUpdate

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "attempt").Inc()
	}

	// 1. Write to live_nodes (current state - ReplacingMergeTree)
	stateBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO node_state_current (
			tenant_id, cluster_id, node_id, cpu_percent, ram_used_bytes, ram_total_bytes,
			disk_used_bytes, disk_total_bytes, up_speed, down_speed,
			active_streams, is_healthy, latitude, longitude, location, metadata, updated_at
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_nodes batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "error").Inc()
		}
		return err
	}

	cpuPercent := float32(nodeLifecycle.GetCpuTenths()) / 10.0

	// Build operational metadata only - skip bulk data (streams, artifacts, storage)
	metadata := map[string]interface{}{}
	if caps := nodeLifecycle.GetCapabilities(); caps != nil {
		metadata["capabilities"] = caps
	}
	if limits := nodeLifecycle.GetLimits(); limits != nil {
		metadata["limits"] = limits
	}
	if bwLimit := nodeLifecycle.GetBwLimit(); bwLimit > 0 {
		metadata["bw_limit"] = bwLimit
	}
	if baseUrl := nodeLifecycle.GetBaseUrl(); baseUrl != "" {
		metadata["base_url"] = baseUrl
	}
	metadataJSON, _ := json.Marshal(metadata)

	clusterID := mt.GetClusterId()
	if appendErr := stateBatch.Append(
		event.TenantID,
		clusterID,
		nodeLifecycle.GetNodeId(),
		cpuPercent,
		uint64(nodeLifecycle.GetRamCurrent()),
		uint64(nodeLifecycle.GetRamMax()),
		uint64(nodeLifecycle.GetDiskUsedBytes()),
		uint64(nodeLifecycle.GetDiskTotalBytes()),
		uint64(nodeLifecycle.GetUpSpeed()),
		uint64(nodeLifecycle.GetDownSpeed()),
		uint32(nodeLifecycle.GetActiveStreams()),
		boolToUint8(nodeLifecycle.GetIsHealthy()),
		nodeLifecycle.GetLatitude(),
		nodeLifecycle.GetLongitude(),
		nodeLifecycle.GetLocation(),
		metadataJSON,
		event.Timestamp,
	); appendErr != nil {
		h.logger.Errorf("Failed to append to live_nodes batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_nodes batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "attempt").Inc()
	}

	// 2. Write to node_metrics (historical log - MergeTree)
	metricsBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO node_metrics_samples (
			timestamp, tenant_id, cluster_id, node_id, cpu_usage, ram_max, ram_current,
			shm_total_bytes, shm_used_bytes, disk_total_bytes, disk_used_bytes,
			bandwidth_in, bandwidth_out, up_speed, down_speed, connections_current,
			stream_count, is_healthy, latitude, longitude, metadata
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare node_metrics batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "error").Inc()
		}
		return err
	}

	if err := metricsBatch.Append(
		event.Timestamp,
		event.TenantID,
		clusterID,
		nodeLifecycle.GetNodeId(),
		cpuPercent,
		int64(nodeLifecycle.GetRamMax()),
		int64(nodeLifecycle.GetRamCurrent()),
		uint64(nodeLifecycle.GetShmTotalBytes()),
		uint64(nodeLifecycle.GetShmUsedBytes()),
		uint64(nodeLifecycle.GetDiskTotalBytes()),
		uint64(nodeLifecycle.GetDiskUsedBytes()),
		uint64(nodeLifecycle.GetBandwidthInTotal()),   // cumulative bytes received
		uint64(nodeLifecycle.GetBandwidthOutTotal()),  // cumulative bytes sent
		int64(nodeLifecycle.GetUpSpeed()),             // rate: bytes/sec
		int64(nodeLifecycle.GetDownSpeed()),           // rate: bytes/sec
		uint32(nodeLifecycle.GetConnectionsCurrent()), // current viewer connections
		int(nodeLifecycle.GetActiveStreams()),
		nodeLifecycle.GetIsHealthy(),
		nodeLifecycle.GetLatitude(),
		nodeLifecycle.GetLongitude(),
		metadataJSON,
	); err != nil {
		h.logger.Errorf("Failed to append to node_metrics batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "error").Inc()
		}
		return err
	}

	if err := metricsBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send node_metrics batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "success").Inc()
	}

	return nil
}

func boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func getUint64SliceFromMap(data map[string]interface{}, key string) []uint64 {
	if data == nil {
		return nil
	}
	value, ok := data[key]
	if !ok || value == nil {
		return nil
	}

	switch v := value.(type) {
	case []uint64:
		return v
	case []interface{}:
		out := make([]uint64, 0, len(v))
		for _, raw := range v {
			switch n := raw.(type) {
			case uint64:
				out = append(out, n)
			case uint32:
				out = append(out, uint64(n))
			case int64:
				if n >= 0 {
					out = append(out, uint64(n))
				}
			case int:
				if n >= 0 {
					out = append(out, uint64(n))
				}
			case float64:
				if n >= 0 {
					out = append(out, uint64(n))
				}
			case json.Number:
				if parsed, err := n.Int64(); err == nil && parsed >= 0 {
					out = append(out, uint64(parsed))
				}
			}
		}
		return out
	default:
		return nil
	}
}

// processStreamBuffer handles STREAM_BUFFER webhook events with rich health metrics
func (h *AnalyticsHandler) processStreamBuffer(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream buffer event: %s", event.EventID)

	// Parse MistTrigger envelope and extract StreamBufferTrigger
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mt.GetStreamId()); err != nil {
		return err
	}
	if h.isDuplicateEvent(ctx, "stream_event_log", parseUUID(event.EventID), event.EventType) {
		return nil
	}
	payload, ok := mt.GetTriggerPayload().(*pb.MistTrigger_StreamBuffer)
	if !ok || payload == nil {
		return fmt.Errorf("unexpected payload for stream_buffer")
	}
	streamBuffer := payload.StreamBuffer
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(streamBuffer.GetStreamName())

	// Extract primary video and audio tracks for dedicated columns
	primaryVideo, primaryAudio := extractPrimaryTracks(streamBuffer.GetTracks())

	// Extract primary video track fields
	var (
		bitrate                      *uint32
		fps                          *float32
		width, height                *uint16
		codec                        *string
		frameMsMax, frameMsMin       *float32
		keyframeMsMax, keyframeMsMin *float32
		framesMax, framesMin         *uint32
		gopSize                      *uint16
	)
	if primaryVideo != nil {
		if v := primaryVideo.GetBitrateKbps(); v > 0 {
			b := uint32(v)
			bitrate = &b
		}
		if v := primaryVideo.GetFps(); v > 0 {
			f := float32(v)
			fps = &f
		}
		if v := primaryVideo.GetWidth(); v > 0 {
			w := uint16(v)
			width = &w
		}
		if v := primaryVideo.GetHeight(); v > 0 {
			ht := uint16(v)
			height = &ht
		}
		if v := primaryVideo.GetCodec(); v != "" {
			codec = &v
		}
		if v := primaryVideo.GetFrameMsMax(); v > 0 {
			f := float32(v)
			frameMsMax = &f
		}
		if v := primaryVideo.GetFrameMsMin(); v > 0 {
			f := float32(v)
			frameMsMin = &f
		}
		if v := primaryVideo.GetKeyframeMsMax(); v > 0 {
			f := float32(v)
			keyframeMsMax = &f
		}
		if v := primaryVideo.GetKeyframeMsMin(); v > 0 {
			f := float32(v)
			keyframeMsMin = &f
		}
		if v := primaryVideo.GetFramesMax(); v > 0 {
			f := uint32(v)
			framesMax = &f
			// Map frames_max (GOP length) to gop_size
			gs := uint16(v)
			gopSize = &gs
		}
		if v := primaryVideo.GetFramesMin(); v > 0 {
			f := uint32(v)
			framesMin = &f
		}
	}

	// Extract stream-wide buffer metrics
	// Note: 0 is a valid buffer value during DRY/rebuffering states.
	// IMPORTANT: only set buffer_size when the optional stream_buffer_ms field is present.
	// Otherwise we turn "unknown" into a hard 0 which corrupts analytics.
	var bufferSize *uint32
	bufferMsPtr := streamBuffer.StreamBufferMs
	if bufferMsPtr != nil {
		bs := uint32(*bufferMsPtr)
		bufferSize = &bs
	}

	// Calculate buffer_health as ratio of current buffer to max allowed distance from live
	// A healthy stream has buffer_health close to 1.0 (buffer full relative to maxkeepaway)
	var bufferHealth *float32
	if bufferSize != nil && streamBuffer.GetMaxKeepawayMs() > 0 {
		bh := float32(*bufferSize) / float32(streamBuffer.GetMaxKeepawayMs())
		if bh > 1.0 {
			bh = 1.0 // Clamp to max 1.0
		}
		bufferHealth = &bh
	}

	// Extract primary audio track fields
	var (
		audioChannels   *uint8
		audioSampleRate *uint32
		audioCodec      *string
		audioBitrate    *uint32
	)
	if primaryAudio != nil {
		if v := primaryAudio.GetChannels(); v > 0 {
			c := uint8(v)
			audioChannels = &c
		}
		if v := primaryAudio.GetSampleRate(); v > 0 {
			sr := uint32(v)
			audioSampleRate = &sr
		}
		if v := primaryAudio.GetCodec(); v != "" {
			audioCodec = &v
		}
		if v := primaryAudio.GetBitrateKbps(); v > 0 {
			b := uint32(v)
			audioBitrate = &b
		}
	}

	// Write to ClickHouse stream_events table
	streamEventsBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_event_log (
			timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status,
			buffer_state, has_issues, issues_description, track_count,
			quality_tier, primary_width, primary_height, primary_fps, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_events batch: %v", err)
		return err
	}

	if appendErr := streamEventsBatch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetNodeId(),
		mt.GetClusterId(),
		"stream_buffer",
		"live", // stream_buffer events only fire for live streams
		streamBuffer.GetBufferState(),
		nilIfZeroBool(streamBuffer.GetHasIssues()),
		nilIfEmptyString(streamBuffer.GetIssuesDescription()),
		nilIfZeroUint16(streamBuffer.GetTrackCount()),
		nilIfEmptyString(streamBuffer.GetQualityTier()),
		width,
		height,
		fps,
		marshalTypedEventData(&streamBuffer),
	); appendErr != nil {
		h.logger.Errorf("Failed to append to stream_events batch: %v", appendErr)
		return appendErr
	}

	if sendErr := streamEventsBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send stream_events batch: %v", sendErr)
		return sendErr
	}

	// Serialize tracks to JSON for track_metadata column (ClickHouse JSON requires object, not array)
	trackMetadataJSON := "{}"
	if tracks := streamBuffer.GetTracks(); len(tracks) > 0 {
		if jsonBytes, marshalErr := json.Marshal(map[string]interface{}{"tracks": tracks}); marshalErr == nil {
			trackMetadataJSON = string(jsonBytes)
		}
	}

	// ALSO write to stream_health_metrics table for detailed health tracking and rebuffering_events MV
	healthBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_health_samples (
			timestamp, tenant_id, stream_id, internal_name, node_id, buffer_state,
			has_issues, issues_description, track_count, track_metadata,
			bitrate, fps, width, height, codec, quality_tier,
			frame_ms_max, frame_ms_min, keyframe_ms_max, keyframe_ms_min, frame_jitter_ms,
			frames_max, frames_min, gop_size, buffer_size, buffer_health,
			audio_channels, audio_sample_rate, audio_codec, audio_bitrate
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_health_metrics batch: %v", err)
		return err
	}

	if appendErr := healthBatch.Append(
		event.Timestamp,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetNodeId(),
		streamBuffer.GetBufferState(),
		nilIfZeroBool(streamBuffer.GetHasIssues()),
		nilIfEmptyString(streamBuffer.GetIssuesDescription()),
		nilIfZeroUint16(streamBuffer.GetTrackCount()),
		trackMetadataJSON,
		bitrate,
		fps,
		width,
		height,
		codec,
		nilIfEmptyString(streamBuffer.GetQualityTier()),
		frameMsMax,
		frameMsMin,
		keyframeMsMax,
		keyframeMsMin,
		nilIfZeroFloat32(float32(streamBuffer.GetStreamJitterMs())),
		framesMax,
		framesMin,
		gopSize,
		bufferSize,
		bufferHealth,
		audioChannels,
		audioSampleRate,
		audioCodec,
		audioBitrate,
	); appendErr != nil {
		h.logger.Errorf("Failed to append to stream_health_metrics batch: %v", appendErr)
		return appendErr
	}

	if sendErr := healthBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send stream_health_metrics batch: %v", sendErr)
		return sendErr
	}

	h.logger.Debugf("Successfully processed stream buffer event for stream: %s (written to both stream_events and stream_health_metrics)", streamBuffer.GetStreamName())
	return nil
}

// extractPrimaryTracks finds the first video and audio tracks from a list of StreamTracks
func extractPrimaryTracks(tracks []*pb.StreamTrack) (video, audio *pb.StreamTrack) {
	for _, t := range tracks {
		if t.GetTrackType() == "video" && video == nil {
			video = t
		}
		if t.GetTrackType() == "audio" && audio == nil {
			audio = t
		}
		if video != nil && audio != nil {
			break
		}
	}
	return
}

// processStreamEnd handles STREAM_END webhook events
func (h *AnalyticsHandler) processStreamEnd(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream end event: %s", event.EventID)

	// Parse MistTrigger envelope and extract StreamEndTrigger
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mt.GetStreamId()); err != nil {
		return err
	}
	if h.isDuplicateEvent(ctx, "stream_event_log", parseUUID(event.EventID), event.EventType) {
		return nil
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_StreamEnd)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for stream_end")
	}
	streamEnd := tp.StreamEnd
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(streamEnd.GetStreamName())

	// Write to ClickHouse stream_events table using ONLY end-specific fields
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_event_log (
			timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type,
			downloaded_bytes, uploaded_bytes, total_viewers, total_inputs, total_outputs,
			viewer_seconds, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	var downloaded, uploaded, totalViewers, totalInputs, totalOutputs, viewerSeconds interface{}
	if streamEnd.DownloadedBytes != nil {
		downloaded = streamEnd.GetDownloadedBytes()
	}
	if streamEnd.UploadedBytes != nil {
		uploaded = streamEnd.GetUploadedBytes()
	}
	if streamEnd.TotalViewers != nil {
		totalViewers = streamEnd.GetTotalViewers()
	}
	if streamEnd.TotalInputs != nil {
		totalInputs = streamEnd.GetTotalInputs()
	}
	if streamEnd.TotalOutputs != nil {
		totalOutputs = streamEnd.GetTotalOutputs()
	}
	if streamEnd.ViewerSeconds != nil {
		viewerSeconds = streamEnd.GetViewerSeconds()
	}

	if appendErr := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetNodeId(),
		mt.GetClusterId(),
		"stream_end",
		downloaded,
		uploaded,
		totalViewers,
		totalInputs,
		totalOutputs,
		viewerSeconds,
		marshalTypedEventData(&streamEnd),
	); appendErr != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", appendErr)
		return appendErr
	}

	if sendErr := batch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", sendErr)
		return sendErr
	}

	h.logger.Debugf("Successfully processed stream end event for stream: %s", streamEnd.GetStreamName())
	return nil
}

// processTrackList handles track list events with quality metrics
func (h *AnalyticsHandler) processTrackList(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing track list event: %s", event.EventID)

	// Parse LiveTrackListTrigger from protobuf
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mt.GetStreamId()); err != nil {
		return err
	}
	eventID := parseUUID(event.EventID)
	if h.isDuplicateEvent(ctx, "track_list_events", eventID, event.EventType) ||
		h.isDuplicateEvent(ctx, "stream_event_log", eventID, event.EventType) {
		return nil
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_TrackList)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for track_list")
	}
	trackList := tp.TrackList
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(trackList.GetStreamName())

	// Write to track_list_events with enhanced quality metrics using typed data
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO track_list_events (
			timestamp, event_id, tenant_id, stream_id, internal_name, node_id,
			track_list, track_count, video_track_count, audio_track_count,
			primary_width, primary_height, primary_fps, primary_video_codec, primary_video_bitrate,
			quality_tier, primary_audio_channels, primary_audio_sample_rate, 
			primary_audio_codec, primary_audio_bitrate
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare track list batch: %v", err)
		return err
	}

	if appendErr := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetNodeId(),
		marshalTypedEventData(trackList.GetTracks()), // serialize tracks as JSON
		uint16(trackList.GetTotalTracks()),           // track_count - required
		uint16(trackList.GetVideoTrackCount()),       // video_track_count - required
		uint16(trackList.GetAudioTrackCount()),       // audio_track_count - required
		// Video fields (Nullable - may not exist for audio-only streams)
		nilIfZeroUint16(trackList.GetPrimaryWidth()),
		nilIfZeroUint16(trackList.GetPrimaryHeight()),
		nilIfZeroFloat32(float32(trackList.GetPrimaryFps())),
		nilIfEmptyString(trackList.GetPrimaryVideoCodec()),
		nilIfZeroUint32(uint32(trackList.GetPrimaryVideoBitrate())),
		nilIfEmptyString(trackList.GetQualityTier()),
		// Audio fields (Nullable - may not exist for video-only streams)
		nilIfZeroUint8(trackList.GetPrimaryAudioChannels()),
		nilIfZeroUint32(uint32(trackList.GetPrimaryAudioSampleRate())),
		nilIfEmptyString(trackList.GetPrimaryAudioCodec()),
		nilIfZeroUint32(uint32(trackList.GetPrimaryAudioBitrate())),
	); appendErr != nil {
		h.logger.Errorf("Failed to append track list data: %v", appendErr)
		return appendErr
	}

	if sendErr := batch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send track list batch: %v", sendErr)
		return sendErr
	}

	// Also write a canonical stream event for lifecycle timelines
	eventBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_event_log (
			timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status,
			event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream events batch (track list): %v", err)
		return err
	}

	if appendErr := eventBatch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetNodeId(),
		mt.GetClusterId(),
		"track_list_update",
		"live",
		marshalTypedEventData(trackList),
	); appendErr != nil {
		h.logger.Errorf("Failed to append stream event (track list): %v", appendErr)
		return appendErr
	}

	if sendErr := eventBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send stream event (track list): %v", sendErr)
		return sendErr
	}

	h.logger.Debugf("Successfully processed track list for stream: %s", trackList.GetStreamName())
	return nil
}

// marshalTypedEventData marshals any typed event data structure to JSON string
func marshalTypedEventData(data interface{}) string {
	if data == nil {
		return "{}"
	}
	b, _ := json.Marshal(data)
	return string(b)
}

func (h *AnalyticsHandler) processClipLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing clip lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> ClipLifecycleData
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mt.GetStreamId()); err != nil {
		return err
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_ClipLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for clip_lifecycle")
	}
	cl := tp.ClipLifecycleData

	// Required - normalize internal name by stripping any prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(cl.GetInternalName())
	tenantID := event.TenantID

	// Prefer clip_hash as the canonical artifact identifier; fall back to request_id if missing.
	requestID := cl.GetClipHash()
	if requestID == "" {
		requestID = cl.GetRequestId()
	}

	// Optional - extract from enriched ClipLifecycleData
	// Note: Only extract fields that exist in clip_events ClickHouse schema
	var (
		startUnix, stopUnix      interface{}
		ingestNode, percent      interface{}
		message, filePath, s3url interface{}
		sizeBytes                interface{}
		expiresAt                interface{} // int64 for clip_events
		expiresAtTime            interface{} // time.Time for live_artifacts
	)
	// Clip time boundaries (enriched by Foghorn from original ClipPullRequest)
	if cl.GetStartUnix() != 0 {
		startUnix = cl.GetStartUnix()
	}
	if cl.GetStopUnix() != 0 {
		stopUnix = cl.GetStopUnix()
	}
	// Processing info
	if cl.GetNodeId() != "" {
		ingestNode = cl.GetNodeId()
	}
	if cl.GetProgressPercent() != 0 {
		percent = cl.GetProgressPercent()
	}
	if cl.GetError() != "" {
		message = cl.GetError()
	}
	if cl.GetFilePath() != "" {
		filePath = cl.GetFilePath()
	}
	if cl.GetS3Url() != "" {
		s3url = cl.GetS3Url()
	}
	if cl.GetSizeBytes() != 0 {
		sizeBytes = cl.GetSizeBytes()
	}
	if cl.GetExpiresAt() != 0 {
		expiresAt = cl.GetExpiresAt()
		expiresAtTime = time.Unix(cl.GetExpiresAt(), 0)
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "attempt").Inc()
	}

	// 1. Write to live_artifacts (current state - ReplacingMergeTree)
	stateBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO artifact_state_current (
			tenant_id, stream_id, request_id, internal_name, filename, content_type, stage,
			progress_percent, error_message, requested_at, started_at, completed_at,
			clip_start_unix, clip_stop_unix, file_path, s3_url, size_bytes,
			processing_node_id, updated_at, expires_at
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}

	// Map stage string for consistency - convert STAGE_DONE -> done, STAGE_FAILED -> failed, etc.
	stageStr := strings.ToLower(strings.TrimPrefix(cl.GetStage().String(), "STAGE_"))

	if appendErr := stateBatch.Append(
		tenantID,
		parseUUID(mt.GetStreamId()),
		requestID,
		internalName,
		nil,
		"clip",
		stageStr,
		uint8(cl.GetProgressPercent()),
		nilIfEmptyString(cl.GetError()),
		event.Timestamp, // requested_at = first event timestamp
		nilIfZeroInt64(cl.GetStartedAt()),
		nilIfZeroInt64(cl.GetCompletedAt()),
		nilIfZeroInt64(cl.GetStartUnix()), // clip time boundaries (enriched by Foghorn)
		nilIfZeroInt64(cl.GetStopUnix()),
		nilIfEmptyString(cl.GetFilePath()),
		nilIfEmptyString(cl.GetS3Url()),
		nilIfZeroUint64(cl.GetSizeBytes()),
		nilIfEmptyString(cl.GetNodeId()),
		event.Timestamp,
		expiresAtTime,
	); appendErr != nil {
		h.logger.Errorf("Failed to append to live_artifacts batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_artifacts batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "attempt").Inc()
	}

	// 2. Write to clip_events (historical log - MergeTree)
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO artifact_events (
			timestamp, tenant_id, stream_id, internal_name, cluster_id, origin_cluster_id,
			filename, request_id, stage, content_type,
			start_unix, stop_unix, ingest_node_id,
			percent, message, file_path, s3_url, size_bytes, expires_at
		)`)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		tenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetClusterId(),
		mt.GetOriginClusterId(),
		nil,
		requestID,
		stageStr,
		"clip",
		startUnix,
		stopUnix,
		ingestNode,
		percent,
		message,
		filePath,
		s3url,
		sizeBytes,
		expiresAt,
	); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if err := batch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "success").Inc()
	}

	return nil
}

// processDVRLifecycle handles DVR lifecycle events
// Dual-writes to: live_artifacts (current state) + clip_events (historical log)
func (h *AnalyticsHandler) processDVRLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing DVR lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> DVRLifecycleData
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mt.GetStreamId()); err != nil {
		return err
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_DvrLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for dvr_lifecycle")
	}
	dvrData := tp.DvrLifecycleData

	tenantID := event.TenantID
	var internalName string
	if dvrData.TenantId != nil && *dvrData.TenantId != "" {
		tenantID = *dvrData.TenantId
	}
	if dvrData.InternalName != nil {
		// Normalize internal name by stripping any prefix for consistent analytics keys
		internalName = mist.ExtractInternalName(*dvrData.InternalName)
	}

	// Map status to stage (normalize proto enum to lowercase for ClickHouse)
	stageStr := normalizeDVRStage(dvrData.GetStatus())

	var expiresAt interface{}
	var expiresAtTime interface{}
	if dvrData.GetExpiresAt() != 0 {
		expiresAt = dvrData.GetExpiresAt()
		expiresAtTime = time.Unix(dvrData.GetExpiresAt(), 0)
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "attempt").Inc()
	}

	// 1. Write to live_artifacts (current state - ReplacingMergeTree)
	stateBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO artifact_state_current (
			tenant_id, stream_id, request_id, internal_name, filename, content_type, stage,
			progress_percent, error_message, requested_at, started_at, completed_at,
			segment_count, manifest_path, file_path, size_bytes, processing_node_id, updated_at, expires_at
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}

	if appendErr := stateBatch.Append(
		tenantID,
		parseUUID(mt.GetStreamId()),
		dvrData.GetDvrHash(),
		internalName,
		nil,
		"dvr",
		stageStr,
		uint8(0), // progress_percent - not in DVRLifecycleData
		nilIfEmptyString(dvrData.GetError()),
		event.Timestamp,                        // requested_at = first event timestamp
		nilIfZeroInt64(dvrData.GetStartedAt()), // DVR time boundaries
		nilIfZeroInt64(dvrData.GetEndedAt()),
		nilIfZeroInt32ToUint32(dvrData.GetSegmentCount()),
		nilIfEmptyString(dvrData.GetManifestPath()),
		nilIfEmptyString(dvrData.GetManifestPath()), // file_path = manifest_path for DVR
		nilIfZeroUint64(dvrData.GetSizeBytes()),
		nilIfEmptyString(mt.GetNodeId()),
		event.Timestamp,
		expiresAtTime,
	); appendErr != nil {
		h.logger.Errorf("Failed to append to live_artifacts batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_artifacts batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "attempt").Inc()
	}

	// 2. Write to clip_events (historical log - MergeTree)
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO artifact_events (
			timestamp, tenant_id, stream_id, internal_name, cluster_id, origin_cluster_id,
			filename, request_id, stage, content_type,
			start_unix, stop_unix, ingest_node_id, file_path, size_bytes, message, expires_at
		)`)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	var message interface{}
	if dvrData.GetError() != "" {
		message = dvrData.GetError()
	}

	if err := batch.Append(
		event.Timestamp,
		tenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		mt.GetClusterId(),
		mt.GetOriginClusterId(),
		nil,
		dvrData.GetDvrHash(),                   // request_id
		stageStr,                               // stage
		"dvr",                                  // content_type
		nilIfZeroInt64(dvrData.GetStartedAt()), // start_unix = DVR started_at
		nilIfZeroInt64(dvrData.GetEndedAt()),   // stop_unix = DVR ended_at
		mt.GetNodeId(),                         // ingest_node_id
		dvrData.GetManifestPath(),              // file_path
		nilIfZeroUint64(dvrData.GetSizeBytes()),
		message, // message
		expiresAt,
	); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if err := batch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "success").Inc()
	}

	return nil
}

// processVodLifecycle handles VOD upload lifecycle events
// Writes to: live_artifacts (current state) + clip_events (historical log)
// VOD differs from clips/DVR in that uploads happen directly to S3 via presigned URLs,
// with Foghorn tracking the lifecycle and emitting events to Kafka.
func (h *AnalyticsHandler) processVodLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing VOD lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> VodLifecycleData
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_VodLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for vod_lifecycle")
	}
	vodData := tp.VodLifecycleData

	tenantID := event.TenantID
	if vodData.TenantId != nil && *vodData.TenantId != "" {
		tenantID = *vodData.TenantId
	}

	// Map status to stage string (normalize proto enum to lowercase for ClickHouse)
	stageStr := normalizeVodStage(vodData.GetStatus())

	var expiresAtTime interface{}
	if vodData.GetExpiresAt() != 0 {
		expiresAtTime = time.Unix(vodData.GetExpiresAt(), 0)
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "attempt").Inc()
	}

	// 1. Write to live_artifacts (current state - ReplacingMergeTree)
	// VOD uses vod_hash as request_id, and content_type='vod'
	stateBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO artifact_state_current (
			tenant_id, stream_id, request_id, internal_name, filename, content_type, stage,
			progress_percent, error_message, requested_at, started_at, completed_at,
			file_path, s3_url, size_bytes, processing_node_id, updated_at, expires_at
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_artifacts batch for VOD: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}

	internalName := vodData.GetVodHash()
	var filename *string
	if vodData.Filename != nil && *vodData.Filename != "" {
		filename = vodData.Filename
	}

	if appendErr := stateBatch.Append(
		tenantID,
		parseUUID(mt.GetStreamId()),
		vodData.GetVodHash(), // request_id = vod_hash
		internalName,         // internal_name = vod_hash for VOD
		filename,
		"vod",    // content_type
		stageStr, // stage
		uint8(0), // progress_percent - not tracked for VOD
		nilIfEmptyStringPtr(vodData.Error),
		event.Timestamp,                        // requested_at
		nilIfZeroInt64Ptr(vodData.StartedAt),   // started_at
		nilIfZeroInt64Ptr(vodData.CompletedAt), // completed_at
		nilIfEmptyStringPtr(vodData.FilePath),  // file_path
		nilIfEmptyStringPtr(vodData.S3Url),     // s3_url
		nilIfZeroUint64Ptr(vodData.SizeBytes),  // size_bytes
		nilIfEmptyStringPtr(vodData.NodeId),    // processing_node_id
		event.Timestamp,                        // updated_at
		expiresAtTime,                          // expires_at
	); appendErr != nil {
		h.logger.Errorf("Failed to append to live_artifacts batch for VOD: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_artifacts batch for VOD: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "attempt").Inc()
	}

	// 2. Write to clip_events (historical log - MergeTree)
	// Reuse clip_events table for VOD lifecycle events (content_type differentiates)
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO artifact_events (
			timestamp, tenant_id, stream_id, internal_name, cluster_id, origin_cluster_id,
			filename, request_id, stage, content_type,
			ingest_node_id, file_path, s3_url, size_bytes, message, expires_at
		)`)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	var expiresAt interface{}
	if vodData.GetExpiresAt() != 0 {
		expiresAt = vodData.GetExpiresAt()
	}

	if err := batch.Append(
		event.Timestamp,
		tenantID,
		parseUUID(mt.GetStreamId()),
		internalName, // internal_name = vod_hash
		mt.GetClusterId(),
		mt.GetOriginClusterId(),
		filename,
		vodData.GetVodHash(),                  // request_id = vod_hash
		stageStr,                              // stage
		"vod",                                 // content_type
		nilIfEmptyStringPtr(vodData.NodeId),   // ingest_node_id
		nilIfEmptyStringPtr(vodData.FilePath), // file_path
		nilIfEmptyStringPtr(vodData.S3Url),    // s3_url
		nilIfZeroUint64Ptr(vodData.SizeBytes), // size_bytes
		nilIfEmptyStringPtr(vodData.Error),    // message (error message)
		expiresAt,                             // expires_at
	); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if err := batch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "success").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"vod_hash":  vodData.GetVodHash(),
		"tenant_id": tenantID,
		"stage":     stageStr,
	}).Info("VOD lifecycle event processed")

	return nil
}

// normalizeVodStage maps VodLifecycleData.Status enum to lowercase stage string
func normalizeVodStage(status pb.VodLifecycleData_Status) string {
	switch status {
	case pb.VodLifecycleData_STATUS_REQUESTED:
		return "requested"
	case pb.VodLifecycleData_STATUS_UPLOADING:
		return "uploading"
	case pb.VodLifecycleData_STATUS_PROCESSING:
		return "processing"
	case pb.VodLifecycleData_STATUS_COMPLETED:
		return "completed"
	case pb.VodLifecycleData_STATUS_FAILED:
		return "failed"
	case pb.VodLifecycleData_STATUS_DELETED:
		return "deleted"
	default:
		return "unknown"
	}
}

// processStorageLifecycle handles storage lifecycle events
func (h *AnalyticsHandler) processStorageLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing storage lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> StorageLifecycleData
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_StorageLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for storage_lifecycle")
	}
	sld := tp.StorageLifecycleData
	if !isValidUUIDString(mt.GetStreamId()) {
		h.logger.WithFields(logging.Fields{
			"event_id":   event.EventID,
			"tenant_id":  event.TenantID,
			"stream_id":  mt.GetStreamId(),
			"asset_hash": sld.GetAssetHash(),
		}).Warn("Storage lifecycle event missing or invalid stream_id")
	}

	// Normalize internal name
	internalName := ""
	if sld.InternalName != nil {
		internalName = mist.ExtractInternalName(sld.GetInternalName())
	}

	actionStr := strings.ToLower(strings.TrimPrefix(sld.GetAction().String(), "ACTION_"))

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO storage_events (
			timestamp, tenant_id, stream_id, internal_name, asset_hash,
			action, asset_type, size_bytes, s3_url, local_path,
			node_id, duration_ms, warm_duration_ms, error
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.TenantID,
		parseUUID(mt.GetStreamId()),
		internalName,
		sld.GetAssetHash(),
		actionStr,
		sld.GetAssetType(),
		sld.GetSizeBytes(),
		nilIfEmptyString(sld.GetS3Url()),
		nilIfEmptyString(sld.GetLocalPath()),
		nilIfEmptyString(mt.GetNodeId()),
		nilIfZeroInt64(sld.GetDurationMs()),
		nilIfZeroInt64(sld.GetWarmDurationMs()),
		nilIfEmptyString(sld.GetError()),
	); err != nil {
		h.logger.Errorf("Failed to append to storage_events batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send storage_events batch: %v", err)
		return err
	}

	return nil
}

func nilIfZeroInt64(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func nilIfZeroInt32ToUint32(v int32) interface{} {
	if v == 0 {
		return nil
	}
	return uint32(v)
}

// normalizeDVRStage converts proto DVRLifecycleData_Status to lowercase stage string for ClickHouse
func normalizeDVRStage(status pb.DVRLifecycleData_Status) string {
	switch status {
	case pb.DVRLifecycleData_STATUS_STARTED:
		return "started"
	case pb.DVRLifecycleData_STATUS_RECORDING:
		return "recording"
	case pb.DVRLifecycleData_STATUS_STOPPED:
		return "stopped"
	case pb.DVRLifecycleData_STATUS_FAILED:
		return "failed"
	case pb.DVRLifecycleData_STATUS_DELETED:
		return "deleted"
	default:
		return "unknown"
	}
}

// processProcessBilling handles process billing events from Helmsman
// These track transcoding operations (Livepeer Gateway, MistProcAV) for billing
func (h *AnalyticsHandler) processProcessBilling(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing process billing event: %s", event.EventID)

	// Parse MistTrigger envelope -> ProcessBillingEvent
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_ProcessBilling)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for process_billing")
	}
	pbe := tp.ProcessBilling

	// Normalize stream name by stripping live+/vod+ prefix for consistent analytics keys
	streamName := mist.ExtractInternalName(pbe.GetStreamName())

	// Use tenant_id from event envelope (already enriched by Decklog)
	tenantID := event.TenantID

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "attempt").Inc()
	}

	// Write to process_billing table
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO processing_events (
			timestamp, tenant_id, node_id, stream_id, internal_name,
			process_type, track_type, duration_ms,
			input_codec, output_codec,
			segment_number, width, height, rendition_count, broadcaster_url, upload_time_us,
			livepeer_session_id, segment_start_ms, input_bytes, output_bytes_total,
			attempt_count, turnaround_ms, speed_factor, renditions_json,
			input_frames, output_frames, decode_us_per_frame, transform_us_per_frame, encode_us_per_frame, is_final,
			input_frames_delta, output_frames_delta, input_bytes_delta, output_bytes_delta,
			input_width, input_height, output_width, output_height,
			input_fpks, output_fps_measured, sample_rate, channels,
			source_timestamp_ms, sink_timestamp_ms, source_advanced_ms, sink_advanced_ms,
			rtf_in, rtf_out, pipeline_lag_ms, output_bitrate_bps
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare process_billing batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "error").Inc()
		}
		return err
	}

	trackType := pbe.GetTrackType()
	if trackType == "" {
		trackType = "unknown"
	}

	if err := batch.Append(
		event.Timestamp,
		tenantID,
		pbe.GetNodeId(),
		parseUUID(mt.GetStreamId()),
		streamName,
		// Process info
		pbe.GetProcessType(),
		trackType,
		pbe.GetDurationMs(),
		// Codec info
		nilIfEmptyStringPtr(pbe.InputCodec),
		nilIfEmptyStringPtr(pbe.OutputCodec),
		// Livepeer-specific fields
		nilIfZeroInt32Ptr(pbe.SegmentNumber),
		nilIfZeroInt32Ptr(pbe.Width),
		nilIfZeroInt32Ptr(pbe.Height),
		nilIfZeroInt32Ptr(pbe.RenditionCount),
		nilIfEmptyStringPtr(pbe.BroadcasterUrl),
		nilIfZeroInt64Ptr(pbe.UploadTimeUs),
		nilIfEmptyStringPtr(pbe.LivepeerSessionId),
		nilIfZeroInt64Ptr(pbe.SegmentStartMs),
		nilIfZeroInt64Ptr(pbe.InputBytes),
		nilIfZeroInt64Ptr(pbe.OutputBytesTotal),
		nilIfZeroInt32Ptr(pbe.AttemptCount),
		nilIfZeroInt64Ptr(pbe.TurnaroundMs),
		nilIfZeroFloat64Ptr(pbe.SpeedFactor),
		nilIfEmptyStringPtr(pbe.RenditionsJson),
		// MistProcAV cumulative/timing
		nilIfZeroInt64Ptr(pbe.InputFrames),
		nilIfZeroInt64Ptr(pbe.OutputFrames),
		nilIfZeroInt64Ptr(pbe.DecodeUsPerFrame),
		nilIfZeroInt64Ptr(pbe.TransformUsPerFrame),
		nilIfZeroInt64Ptr(pbe.EncodeUsPerFrame),
		boolToNullableUInt8(pbe.IsFinal),
		// MistProcAV delta values
		nilIfZeroInt64Ptr(pbe.InputFramesDelta),
		nilIfZeroInt64Ptr(pbe.OutputFramesDelta),
		nilIfZeroInt64Ptr(pbe.InputBytesDelta),
		nilIfZeroInt64Ptr(pbe.OutputBytesDelta),
		// MistProcAV dimensions
		nilIfZeroInt32Ptr(pbe.InputWidth),
		nilIfZeroInt32Ptr(pbe.InputHeight),
		nilIfZeroInt32Ptr(pbe.OutputWidth),
		nilIfZeroInt32Ptr(pbe.OutputHeight),
		// MistProcAV frame/audio info
		nilIfZeroInt32Ptr(pbe.InputFpks),
		nilIfZeroFloat64Ptr(pbe.OutputFpsMeasured),
		nilIfZeroInt32Ptr(pbe.SampleRate),
		nilIfZeroInt32Ptr(pbe.Channels),
		// MistProcAV timing
		nilIfZeroInt64Ptr(pbe.SourceTimestampMs),
		nilIfZeroInt64Ptr(pbe.SinkTimestampMs),
		nilIfZeroInt64Ptr(pbe.SourceAdvancedMs),
		nilIfZeroInt64Ptr(pbe.SinkAdvancedMs),
		// MistProcAV performance
		nilIfZeroFloat64Ptr(pbe.RtfIn),
		nilIfZeroFloat64Ptr(pbe.RtfOut),
		nilIfZeroInt64Ptr(pbe.PipelineLagMs),
		nilIfZeroInt64Ptr(pbe.OutputBitrateBps),
	); err != nil {
		h.logger.Errorf("Failed to append to process_billing batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "error").Inc()
		}
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send process_billing batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "success").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"stream":       streamName,
		"process_type": pbe.GetProcessType(),
		"track_type":   pbe.GetTrackType(),
		"duration_ms":  pbe.GetDurationMs(),
	}).Debug("Successfully processed process billing event")

	return nil
}

// processAPIRequestBatch handles aggregated API request batches from Gateway
// These track GraphQL API usage for analytics (RFC: x402 Agent Access)
func (h *AnalyticsHandler) processAPIRequestBatch(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Debugf("Processing API request batch event: %s", event.EventID)

	// Parse MistTrigger envelope -> APIRequestBatch
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_ApiRequestBatch)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for api_request_batch")
	}
	batch := tp.ApiRequestBatch

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "attempt").Inc()
	}

	// Prepare batch insert to api_requests table
	// Each aggregate becomes one row with request_count > 1
	chBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO api_requests (
			timestamp, tenant_id, source_node,
			auth_type, operation_name, operation_type,
			request_count, error_count, total_duration_ms, total_complexity,
			user_hashes, token_hashes
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare api_request_batch batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return err
	}

	batchTimestamp := time.Unix(batch.GetTimestamp(), 0)
	sourceNode := batch.GetSourceNode()
	appendErrors := 0
	rowCount := 0

	for _, agg := range batch.GetAggregates() {
		timestamp := batchTimestamp
		if aggTimestamp := agg.GetTimestamp(); aggTimestamp > 0 {
			timestamp = time.Unix(aggTimestamp, 0)
		}
		tenantID := parseUUID(agg.GetTenantId())
		if tenantID == uuid.Nil {
			// Skip invalid tenant IDs
			continue
		}

		// Use nil for empty operation names (Nullable column)
		var operationName interface{}
		if name := agg.GetOperationName(); name != "" {
			operationName = name
		}

		userHashes := agg.GetUserHashes()
		if userHashes == nil {
			userHashes = []uint64{}
		}
		tokenHashes := agg.GetTokenHashes()
		if tokenHashes == nil {
			tokenHashes = []uint64{}
		}

		if err := chBatch.Append(
			timestamp,
			tenantID,
			sourceNode,
			agg.GetAuthType(),
			operationName,
			agg.GetOperationType(),
			agg.GetRequestCount(),
			agg.GetErrorCount(),
			agg.GetTotalDurationMs(),
			agg.GetTotalComplexity(),
			userHashes,
			tokenHashes,
		); err != nil {
			h.logger.WithFields(logging.Fields{
				"tenant_id": agg.GetTenantId(),
				"error":     err,
			}).Warn("Failed to append aggregate to api_request_batch")
			appendErrors++
			continue
		}
		rowCount++
	}

	if rowCount == 0 {
		// If everything was filtered out (empty/invalid payload), treat as a no-op.
		// Returning an error would cause the Kafka consumer to retry forever and stall the partition.
		if appendErrors > 0 {
			if h.metrics != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
			}
			return fmt.Errorf("api_request_batch append failures: %d", appendErrors)
		}

		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "skip").Inc()
		}
		h.logger.WithFields(logging.Fields{
			"source_node": sourceNode,
		}).Debug("api_request_batch had no valid aggregates; skipping")
		return nil
	}

	if appendErrors > 0 {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return fmt.Errorf("api_request_batch append failures: %d", appendErrors)
	}

	if err := chBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send api_request_batch batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "success").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"source_node":     sourceNode,
		"aggregate_count": rowCount,
	}).Debug("Successfully processed API request batch")

	return nil
}

// processServiceAPIRequestBatch handles API usage aggregates from ServiceEvent payloads.
func (h *AnalyticsHandler) processServiceAPIRequestBatch(ctx context.Context, event kafka.ServiceEvent) error {
	h.logger.Debugf("Processing service API request batch event: %s", event.EventID)

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "attempt").Inc()
	}

	timestamp := event.Timestamp
	if ts, ok := getInt64FromMap(event.Data, "timestamp"); ok {
		timestamp = time.Unix(ts, 0)
	}
	sourceNode := getStringFromMap(event.Data, "source_node")

	aggregatesRaw, ok := event.Data["aggregates"]
	if !ok {
		return fmt.Errorf("missing aggregates in api_request_batch service event")
	}

	aggregatesSlice, ok := aggregatesRaw.([]interface{})
	if !ok {
		return fmt.Errorf("invalid aggregates type in api_request_batch service event")
	}

	chBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO api_requests (
			timestamp, tenant_id, source_node,
			auth_type, operation_name, operation_type,
			request_count, error_count, total_duration_ms, total_complexity,
			user_hashes, token_hashes
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare api_request_batch batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return err
	}

	appendErrors := 0
	rowCount := 0
	for _, rawAgg := range aggregatesSlice {
		aggMap, ok := rawAgg.(map[string]interface{})
		if !ok {
			continue
		}
		aggTimestamp := timestamp
		if ts, ok := getInt64FromMap(aggMap, "timestamp"); ok {
			aggTimestamp = time.Unix(ts, 0)
		}

		tenantID := parseUUID(getStringFromMap(aggMap, "tenant_id"))
		if tenantID == uuid.Nil {
			continue
		}

		operationName := getStringFromMap(aggMap, "operation_name")
		var operationNameValue interface{}
		if operationName != "" {
			operationNameValue = operationName
		}

		userHashes := getUint64SliceFromMap(aggMap, "user_hashes")
		if userHashes == nil {
			userHashes = []uint64{}
		}
		tokenHashes := getUint64SliceFromMap(aggMap, "token_hashes")
		if tokenHashes == nil {
			tokenHashes = []uint64{}
		}

		if err := chBatch.Append(
			aggTimestamp,
			tenantID,
			sourceNode,
			getStringFromMap(aggMap, "auth_type"),
			operationNameValue,
			getStringFromMap(aggMap, "operation_type"),
			uint32(getUint64FromMap(aggMap, "request_count")),
			uint32(getUint64FromMap(aggMap, "error_count")),
			getUint64FromMap(aggMap, "total_duration_ms"),
			uint32(getUint64FromMap(aggMap, "total_complexity")),
			userHashes,
			tokenHashes,
		); err != nil {
			h.logger.WithFields(logging.Fields{
				"tenant_id": getStringFromMap(aggMap, "tenant_id"),
				"error":     err,
			}).Warn("Failed to append aggregate to api_request_batch")
			appendErrors++
			continue
		}
		rowCount++
	}

	if rowCount == 0 {
		// If everything was filtered out (empty/invalid payload), treat as a no-op.
		// Returning an error would cause the Kafka consumer to retry forever and stall the partition.
		if appendErrors > 0 {
			if h.metrics != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
			}
			return fmt.Errorf("api_request_batch append failures: %d", appendErrors)
		}

		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "skip").Inc()
		}
		h.logger.WithFields(logging.Fields{
			"source_node": sourceNode,
		}).Debug("api_request_batch had no valid aggregates; skipping")
		return nil
	}

	if appendErrors > 0 {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return fmt.Errorf("api_request_batch append failures: %d", appendErrors)
	}

	if err := chBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send api_request_batch batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "success").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"source_node":     sourceNode,
		"aggregate_count": rowCount,
	}).Debug("Successfully processed service API request batch")

	_ = h.processServiceAPIRequestBatchAudit(ctx, event, aggregatesSlice, sourceNode, timestamp)

	return nil
}

func (h *AnalyticsHandler) processServiceAPIRequestBatchAudit(ctx context.Context, event kafka.ServiceEvent, aggregates []interface{}, sourceNode string, batchTimestamp time.Time) error {
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_events", "attempt").Inc()
	}

	chBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO api_events (
			tenant_id, event_type, source, user_id, resource_type, resource_id, details, timestamp
		)`)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}

	rowCount := 0
	for _, rawAgg := range aggregates {
		aggMap, ok := rawAgg.(map[string]interface{})
		if !ok {
			continue
		}

		tenantID := parseUUID(getStringFromMap(aggMap, "tenant_id"))
		if tenantID == uuid.Nil {
			continue
		}

		aggTimestamp := batchTimestamp
		if ts, ok := getInt64FromMap(aggMap, "timestamp"); ok {
			aggTimestamp = time.Unix(ts, 0)
		}

		details := map[string]interface{}{
			"source_node":       sourceNode,
			"auth_type":         getStringFromMap(aggMap, "auth_type"),
			"operation_name":    getStringFromMap(aggMap, "operation_name"),
			"operation_type":    getStringFromMap(aggMap, "operation_type"),
			"request_count":     getUint64FromMap(aggMap, "request_count"),
			"error_count":       getUint64FromMap(aggMap, "error_count"),
			"total_duration_ms": getUint64FromMap(aggMap, "total_duration_ms"),
			"total_complexity":  getUint64FromMap(aggMap, "total_complexity"),
			"user_hashes":       getUint64SliceFromMap(aggMap, "user_hashes"),
			"token_hashes":      getUint64SliceFromMap(aggMap, "token_hashes"),
		}

		detailsJSON, err := json.Marshal(details)
		if err != nil {
			if h.metrics != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
			}
			return fmt.Errorf("failed to marshal api_request_batch audit details: %w", err)
		}

		if err := chBatch.Append(
			tenantID,
			event.EventType,
			event.Source,
			nilIfEmptyString(event.UserID),
			nilIfEmptyString(event.ResourceType),
			nilIfEmptyString(event.ResourceID),
			string(detailsJSON),
			aggTimestamp,
		); err != nil {
			if h.metrics != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
			}
			return err
		}
		rowCount++
	}

	if rowCount == 0 {
		return nil
	}

	if err := chBatch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_events", "success").Inc()
	}

	return nil
}

// processTenantCreated handles tenant_created events for acquisition attribution.
func (h *AnalyticsHandler) processTenantCreated(ctx context.Context, event kafka.ServiceEvent) error {
	if !isValidUUIDString(event.TenantID) {
		return nil
	}
	attr := getMap(event.Data, "attribution")
	if attr == nil {
		return nil
	}
	signupChannel := getString(attr, "signup_channel")
	if signupChannel == "" {
		return nil
	}
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "attempt").Inc()
	}
	eventDataJSON, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal tenant_created event data: %w", err)
	}
	chBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO tenant_acquisition_events (
			timestamp, tenant_id, user_id, signup_channel, signup_method,
			utm_source, utm_medium, utm_campaign, utm_content, utm_term,
			http_referer, landing_page, referral_code, is_agent, event_data
		)`)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "error").Inc()
		}
		return err
	}
	if err := chBatch.Append(
		event.Timestamp,
		parseUUID(event.TenantID),
		parseUUIDOrNil(event.UserID),
		signupChannel,
		getString(attr, "signup_method"),
		nilIfEmptyString(getString(attr, "utm_source")),
		nilIfEmptyString(getString(attr, "utm_medium")),
		nilIfEmptyString(getString(attr, "utm_campaign")),
		nilIfEmptyString(getString(attr, "utm_content")),
		nilIfEmptyString(getString(attr, "utm_term")),
		nilIfEmptyString(getString(attr, "http_referer")),
		nilIfEmptyString(getString(attr, "landing_page")),
		nilIfEmptyString(getString(attr, "referral_code")),
		boolToUInt8(getBool(attr, "is_agent")),
		string(eventDataJSON),
	); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "error").Inc()
		}
		return err
	}
	if err := chBatch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "error").Inc()
		}
		return err
	}
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "success").Inc()
	}
	return nil
}

// processServiceEventAudit inserts service events into the api_events audit table.
func (h *AnalyticsHandler) processServiceEventAudit(ctx context.Context, event kafka.ServiceEvent) error {
	if !isValidUUIDString(event.TenantID) {
		return nil
	}

	data := sanitizeServiceEventData(event)

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_events", "attempt").Inc()
	}

	detailsJSON, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal service event details: %w", err)
	}

	chBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO api_events (
			tenant_id, event_type, source, user_id, resource_type, resource_id, details, timestamp
		)`)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}

	if err := chBatch.Append(
		parseUUID(event.TenantID),
		event.EventType,
		event.Source,
		nilIfEmptyString(event.UserID),
		nilIfEmptyString(event.ResourceType),
		nilIfEmptyString(event.ResourceID),
		string(detailsJSON),
		event.Timestamp,
	); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}

	if err := chBatch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_events", "success").Inc()
	}

	return nil
}

func sanitizeServiceEventData(event kafka.ServiceEvent) map[string]interface{} {
	switch event.EventType {
	case "message_received", "message_updated":
		return allowlistEventData(event.Data, []string{"conversation_id", "message_id", "sender", "timestamp"})
	case "conversation_created", "conversation_updated":
		return allowlistEventData(event.Data, []string{"conversation_id", "status", "subject", "timestamp"})
	default:
		return event.Data
	}
}

func allowlistEventData(data map[string]interface{}, keys []string) map[string]interface{} {
	if data == nil {
		return nil
	}

	out := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		if val, ok := data[key]; ok {
			out[key] = val
		}
	}

	return out
}

func getMap(data map[string]interface{}, key string) map[string]interface{} {
	if data == nil {
		return nil
	}
	if value, ok := data[key]; ok {
		if cast, ok := value.(map[string]interface{}); ok {
			return cast
		}
	}
	return nil
}

func getString(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	if value, ok := data[key]; ok {
		if cast, ok := value.(string); ok {
			return cast
		}
	}
	return ""
}

func getBool(data map[string]interface{}, key string) bool {
	if data == nil {
		return false
	}
	if value, ok := data[key]; ok {
		if cast, ok := value.(bool); ok {
			return cast
		}
	}
	return false
}

func boolToUInt8(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

// Helper functions for optional pointer fields
func nilIfEmptyStringPtr(s *string) interface{} {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nilIfZeroInt32Ptr(v *int32) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

func nilIfZeroInt64Ptr(v *int64) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

func nilIfZeroUint64Ptr(v *uint64) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

// valueOrNilUint64Ptr returns the value if pointer is non-nil (preserves 0), nil otherwise.
// Use this for fields where 0 is a valid value (e.g., packet stats - HLS has 0 packets).
func valueOrNilUint64Ptr(v *uint64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func nilIfZeroFloat64Ptr(v *float64) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

func boolToNullableUInt8(v *bool) interface{} {
	if v == nil {
		return nil
	}
	if *v {
		return uint8(1)
	}
	return uint8(0)
}
