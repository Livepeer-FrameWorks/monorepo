package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"frameworks/pkg/database"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// PeriscopeMetrics holds all Prometheus metrics for Periscope Ingest
type PeriscopeMetrics struct {
	AnalyticsEvents         *prometheus.CounterVec
	BatchProcessingDuration *prometheus.HistogramVec
	ClickHouseInserts       *prometheus.CounterVec
	KafkaMessages           *prometheus.CounterVec
	KafkaDuration           *prometheus.HistogramVec
	KafkaLag                *prometheus.GaugeVec
}

// AnalyticsHandler handles analytics events
type AnalyticsHandler struct {
	clickhouse database.ClickHouseNativeConn
	logger     logging.Logger
	metrics    *PeriscopeMetrics
}

// NewAnalyticsHandler creates a new analytics handler
func NewAnalyticsHandler(clickhouse database.ClickHouseNativeConn, logger logging.Logger, metrics *PeriscopeMetrics) *AnalyticsHandler {
	return &AnalyticsHandler{
		clickhouse: clickhouse,
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
		err = h.processPlayRewrite(ctx, event)
	case "stream_source":
		err = h.processStreamView(ctx, event)
	case "push_end":
		err = h.processPushLifecycle(ctx, event)
	case "push_out_start":
		err = h.processPushLifecycle(ctx, event)
	case "stream_track_list":
		err = h.processTrackList(ctx, event)
	case "stream_bandwidth":
		err = h.processBandwidthThreshold(ctx, event)
	case "recording_complete":
		err = h.processRecordingLifecycle(ctx, event)
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
	case "storage_snapshot":
		err = h.processStorageSnapshot(ctx, event)
	default:
		h.logger.WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
		}).Debug("Unknown event type, skipping")
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "skipped").Inc()
		}
		return nil
	}

	if err != nil {
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
			timestamp, node_id, tenant_id,
			total_bytes, file_count, dvr_bytes, clip_bytes, recording_bytes
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch for storage_snapshots: %v", err)
		return err
	}

	for _, usage := range storageSnapshot.GetUsage() {
		if err := batch.Append(
			event.Timestamp,
			storageSnapshot.GetNodeId(),
			usage.GetTenantId(),
			usage.GetTotalBytes(),
			usage.GetFileCount(),
			usage.GetDvrBytes(),
			usage.GetClipBytes(),
			usage.GetRecordingBytes(),
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
		INSERT INTO live_streams (
			tenant_id, internal_name, node_id, status, buffer_state,
			current_viewers, total_inputs, uploaded_bytes, downloaded_bytes,
			viewer_seconds, has_issues, issues_description,
			track_count, quality_tier, primary_width, primary_height,
			primary_fps, primary_codec, primary_bitrate,
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

	// Convert started_at unix timestamp to time.Time if present
	var startedAt interface{}
	if streamLifecycle.StartedAt != nil && *streamLifecycle.StartedAt > 0 {
		startedAt = time.Unix(*streamLifecycle.StartedAt, 0)
	}

	if err := stateBatch.Append(
		event.TenantID,
		internalName,
		mt.GetNodeId(),
		status,
		streamLifecycle.GetBufferState(),
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
		nilIfZeroInt32(streamLifecycle.GetPrimaryBitrate()),
		startedAt,
		event.Timestamp,
	); err != nil {
		h.logger.Errorf("Failed to append to live_streams batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "error").Inc()
		}
		return err
	}

	if err := stateBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send live_streams batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "attempt").Inc()
	}

	// 2. Write to stream_events (historical log - MergeTree)
	eventBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, node_id, event_type,
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

	if err := eventBatch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		internalName,
		mt.GetNodeId(),
		"stream_lifecycle",
		streamLifecycle.GetBufferState(),
		streamLifecycle.GetDownloadedBytes(),
		streamLifecycle.GetUploadedBytes(),
		streamLifecycle.GetTotalViewers(),
		streamLifecycle.GetTotalInputs(),
		0, // total_outputs not in StreamLifecycleUpdate
		streamLifecycle.GetViewerSeconds(),
		nilIfZeroBool(streamLifecycle.GetHasIssues()),
		nilIfEmptyString(streamLifecycle.GetIssuesDescription()),
		nilIfZeroInt32(streamLifecycle.GetTrackCount()),
		nilIfEmptyString(streamLifecycle.GetQualityTier()),
		nilIfZeroInt32(streamLifecycle.GetPrimaryWidth()),
		nilIfZeroInt32(streamLifecycle.GetPrimaryHeight()),
		nilIfZeroFloat32(streamLifecycle.GetPrimaryFps()),
		marshalTypedEventData(&streamLifecycle),
	); err != nil {
		h.logger.Errorf("Failed to append to stream_events batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return err
	}

	if err := eventBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send stream_events batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "success").Inc()
	}

	return nil
}

// processPlayRewrite handles play rewrite events (formerly stream ingest/viewer resolve)
func (h *AnalyticsHandler) processPlayRewrite(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing play rewrite event: %s", event.EventID)

	// Parse PlayRewriteTrigger from protobuf (viewer-side resolve)
	var vr pb.MistTrigger
	if err := h.parseProtobufData(event, &vr); err != nil {
		return fmt.Errorf("failed to parse MistTrigger envelope: %w", err)
	}
	payload, ok := vr.GetTriggerPayload().(*pb.MistTrigger_PlayRewrite)
	if !ok || payload == nil {
		return fmt.Errorf("event is not PlayRewrite")
	}
	streamIngest := payload.PlayRewrite

	// Use resolved internal name if available (enriched by Foghorn), fallback to requested_stream.
	// Then normalize by stripping any live+/vod+ prefix.
	internalName := streamIngest.GetResolvedInternalName()
	if internalName == "" {
		internalName = streamIngest.GetRequestedStream() // fallback for old events
	}
	internalName = mist.ExtractInternalName(internalName)

	// Write to ClickHouse for time-series analysis
	batch, err := h.clickhouse.PrepareBatch(ctx, `
        INSERT INTO stream_events (
            timestamp, event_id, tenant_id, internal_name, node_id, event_type,
            stream_key, user_id, hostname, push_url, protocol,
            latitude, longitude, location, country_code, city,
            event_data
        )`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	// Viewer resolve is not ingest; set ingest-only fields to NULL
	var nodeID interface{}
	if streamIngest.GetNodeId() != "" {
		nodeID = streamIngest.GetNodeId()
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		internalName,
		nodeID,
		"play_rewrite",
		nil, // stream_key N/A
		nil, // user_id N/A
		streamIngest.GetViewerHost(),
		streamIngest.GetRequestUrl(),
		nil, // protocol N/A
		nilIfZeroFloat64(streamIngest.GetLatitude()),
		nilIfZeroFloat64(streamIngest.GetLongitude()),
		nil, // location N/A for viewer events (node location only)
		nilIfEmptyString(streamIngest.GetCountryCode()),
		nilIfEmptyString(streamIngest.GetCity()),
		marshalTypedEventData(&streamIngest),
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	return nil
}

// processViewerConnection writes connection_events (connect/disconnect) to ClickHouse
func (h *AnalyticsHandler) processViewerConnection(ctx context.Context, event kafka.AnalyticsEvent, isConnect bool) error {
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}

	var streamName, sessionID, connector, nodeID, host, requestURL string
	var duration, upBytes, downBytes int64
	var countryCode, city interface{}
	var latitude, longitude interface{}
	var clientBucketH3 interface{}
	var clientBucketRes interface{}
	var nodeBucketH3 interface{}
	var nodeBucketRes interface{}

	switch p := mt.GetTriggerPayload().(type) {
	case *pb.MistTrigger_ViewerConnect:
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
		vd := p.ViewerDisconnect
		streamName = vd.GetStreamName()
		sessionID = vd.GetSessionId()
		connector = vd.GetConnector()
		host = vd.GetHost()
		nodeID = vd.GetNodeId()
		duration = vd.GetDuration()
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

	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	streamName = mist.ExtractInternalName(streamName)

	batch, err := h.clickhouse.PrepareBatch(ctx, `
        INSERT INTO connection_events (
            event_id, timestamp, tenant_id, internal_name,
            session_id, connection_addr, connector, node_id, request_url,
            country_code, city, latitude, longitude,
            client_bucket_h3, client_bucket_res, node_bucket_h3, node_bucket_res,
            event_type, session_duration, bytes_transferred
        )`)
	if err != nil {
		return err
	}

	eventType := map[bool]string{true: "connect", false: "disconnect"}[isConnect]
	var durationUI interface{}
	var bytesTransferred interface{}
	if !isConnect {
		durationUI = duration
		bytesTransferred = uint64(max64(0, upBytes) + max64(0, downBytes))
	}

	if err := batch.Append(
		event.EventID,
		event.Timestamp,
		event.TenantID,
		streamName,
		sessionID,
		host,
		connector,
		nodeID,
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

func nilIfZeroFloat64(v float64) interface{} {
	if v == 0 {
		return nil
	}
	return v
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
func nilIfZeroInt32(v int32) interface{} {
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
func nilIfFalse(v bool) interface{} {
	if !v {
		return nil
	}
	return v
}

// processPushLifecycle handles push lifecycle events
func (h *AnalyticsHandler) processPushLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing push lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	_, isPushStart := mt.GetTriggerPayload().(*pb.MistTrigger_PushOutStart)

	// Write to ClickHouse for time-series analysis - ONLY fields that exist in push events
	var batchSQL string
	var values []interface{}

	if isPushStart {
		// PUSH_OUT_START: only has push_target
		batchSQL = `INSERT INTO stream_events (
            timestamp, event_id, tenant_id, internal_name, node_id, event_type,
            push_target, event_data
        )`
		p := mt.GetPushOutStart()
		// Normalize internal name by stripping live+/vod+ prefix
		internalName := mist.ExtractInternalName(p.GetStreamName())
		values = []interface{}{
			event.Timestamp,
			event.EventID,
			event.TenantID,
			internalName,
			mt.GetNodeId(),
			"push_out_start",
			p.GetPushTarget(),
			marshalTypedEventData(p),
		}
	} else {
		// PUSH_END: has push_id, target URIs, status, log_messages
		batchSQL = `INSERT INTO stream_events (
            timestamp, event_id, tenant_id, internal_name, node_id, event_type,
            push_id, push_target, target_uri_before, target_uri_after, push_status, log_messages, event_data
        )`
		p := mt.GetPushEnd()
		// Normalize internal name by stripping live+/vod+ prefix
		internalName := mist.ExtractInternalName(p.GetStreamName())
		values = []interface{}{
			event.Timestamp,
			event.EventID,
			event.TenantID,
			internalName,
			mt.GetNodeId(),
			"push_end",
			p.GetPushId(),
			nil, // push_target not present
			p.GetTargetUriBefore(),
			p.GetTargetUriAfter(),
			p.GetPushStatus(),
			p.GetLogMessages(),
			marshalTypedEventData(p),
		}
	}

	batch, err := h.clickhouse.PrepareBatch(ctx, batchSQL)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(values...); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	return nil
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
        INSERT INTO stream_events (
            timestamp, event_id, tenant_id, internal_name, node_id, event_type,
            stream_key, hostname, push_url, protocol,
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
	// Node location (where MistServer is running)
	var lat interface{}
	if pr.Latitude != nil && *pr.Latitude != 0 {
		lat = *pr.Latitude
	}
	var lon interface{}
	if pr.Longitude != nil && *pr.Longitude != 0 {
		lon = *pr.Longitude
	}
	var loc interface{}
	if pr.Location != nil && *pr.Location != "" {
		loc = *pr.Location
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

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		internalName,
		mt.GetNodeId(),
		"push-rewrite",
		pr.GetStreamName(), // Original stream_key for reference
		pr.GetHostname(),
		pr.GetPushUrl(),
		prot,
		lat,
		lon,
		loc,
		pubCountry,
		pubCity,
		marshalTypedEventData(pr),
	); err != nil {
		return err
	}
	return batch.Send()
}

// processRecordingLifecycle handles recording lifecycle events
func (h *AnalyticsHandler) processRecordingLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing recording lifecycle event: %s", event.EventID)

	// Parse RecordingEndTrigger from protobuf
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_RecordingComplete)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for recording_complete")
	}
	recording := tp.RecordingComplete
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(recording.GetStreamName())

	// Write to ClickHouse for time-series analysis - ONLY fields that exist in recording events
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, node_id, event_type,
			file_size, duration, output_file, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		internalName,
		recording.GetNodeId(),
		"recording-complete",
		nilIfZeroUint64(uint64(recording.GetBytesWritten())),   // file_size
		nilIfZeroUint32(uint32(recording.GetSecondsWriting())), // duration
		recording.GetFilePath(),                                // output_file = file_path
		marshalTypedEventData(&recording),
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	return nil
}

// processStreamView handles stream view events
func (h *AnalyticsHandler) processStreamView(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream view event: %s", event.EventID)

	// Parse StreamSourceTrigger from protobuf
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_StreamSource)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for stream_source")
	}
	streamView := tp.StreamSource
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(streamView.GetStreamName())

	// Write to ClickHouse for time-series analysis - basic stream view event
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, node_id, event_type, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		internalName,
		mt.GetNodeId(),
		"stream_view",
		marshalTypedEventData(&streamView),
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	return nil
}

// processLoadBalancing handles load balancing events
func (h *AnalyticsHandler) processLoadBalancing(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing load balancing event: %s", event.EventID)

	// Parse MistTrigger envelope
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
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
        INSERT INTO routing_events (
            timestamp, tenant_id, internal_name, selected_node, status, details, score,
            client_ip, client_country, client_latitude, client_longitude, client_bucket_h3, client_bucket_res,
            node_latitude, node_longitude, node_name, node_bucket_h3, node_bucket_res,
            selected_node_id, routing_distance_km
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

	if err := batch.Append(
		event.Timestamp,
		event.TenantID,
		internalName,
		loadBalancing.GetSelectedNode(),
		loadBalancing.GetStatus(),
		loadBalancing.GetDetails(),
		int64(loadBalancing.GetScore()),
		loadBalancing.GetClientIp(),
		loadBalancing.GetClientCountry(),
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
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
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
	}).Debug("Processed load balancing event")

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
		INSERT INTO client_metrics (
			timestamp, tenant_id, internal_name, session_id, node_id, protocol, host,
			connection_time, bandwidth_in, bandwidth_out, bytes_downloaded, bytes_uploaded,
			packets_sent, packets_lost, packets_retransmitted, connection_quality
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.TenantID,
		internalName,
		clientLifecycle.GetSessionId(),
		mt.GetNodeId(),
		clientLifecycle.GetProtocol(),
		clientLifecycle.GetHost(),
		clientLifecycle.GetConnectionTime(),
		uint64(clientLifecycle.GetBandwidthInBps()),
		uint64(clientLifecycle.GetBandwidthOutBps()),
		uint64(clientLifecycle.GetBytesDownloaded()),
		uint64(clientLifecycle.GetBytesUploaded()),
		uint64(clientLifecycle.GetPacketsSent()),
		uint64(clientLifecycle.GetPacketsLost()),
		uint64(clientLifecycle.GetPacketsRetransmitted()),
		connectionQuality,
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
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
		INSERT INTO live_nodes (
			tenant_id, node_id, cpu_percent, ram_used_bytes, ram_total_bytes,
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

	if err := stateBatch.Append(
		event.TenantID,
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
	); err != nil {
		h.logger.Errorf("Failed to append to live_nodes batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "error").Inc()
		}
		return err
	}

	if err := stateBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send live_nodes batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "attempt").Inc()
	}

	// 2. Write to node_metrics (historical log - MergeTree)
	metricsBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO node_metrics (
			timestamp, tenant_id, node_id, cpu_usage, ram_max, ram_current,
			shm_total_bytes, shm_used_bytes, disk_total_bytes, disk_used_bytes,
			up_speed, down_speed, stream_count, is_healthy, latitude, longitude, metadata
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
		nodeLifecycle.GetNodeId(),
		cpuPercent,
		int64(nodeLifecycle.GetRamMax()),
		int64(nodeLifecycle.GetRamCurrent()),
		uint64(nodeLifecycle.GetShmTotalBytes()),
		uint64(nodeLifecycle.GetShmUsedBytes()),
		uint64(nodeLifecycle.GetDiskTotalBytes()),
		uint64(nodeLifecycle.GetDiskUsedBytes()),
		int64(nodeLifecycle.GetUpSpeed()),
		int64(nodeLifecycle.GetDownSpeed()),
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

// processStreamBuffer handles STREAM_BUFFER webhook events with rich health metrics
func (h *AnalyticsHandler) processStreamBuffer(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream buffer event: %s", event.EventID)

	// Parse MistTrigger envelope and extract StreamBufferTrigger
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	payload, ok := mt.GetTriggerPayload().(*pb.MistTrigger_StreamBuffer)
	if !ok || payload == nil {
		return fmt.Errorf("unexpected payload for stream_buffer")
	}
	streamBuffer := payload.StreamBuffer
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(streamBuffer.GetStreamName())

	// Write to ClickHouse stream_events table
	streamEventsBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, node_id, event_type,
			buffer_state, has_issues, issues_description, track_count,
			quality_tier, primary_width, primary_height, primary_fps, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_events batch: %v", err)
		return err
	}

	if err := streamEventsBatch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		internalName,
		mt.GetNodeId(),
		"stream_buffer",
		streamBuffer.GetBufferState(),
		nilIfZeroBool(streamBuffer.GetHasIssues()),
		nilIfEmptyString(streamBuffer.GetIssuesDescription()),
		nilIfZeroInt32(streamBuffer.GetTrackCount()),
		nilIfEmptyString(streamBuffer.GetQualityTier()),
		nil, // width N/A
		nil, // height N/A
		nil, // fps N/A
		marshalTypedEventData(&streamBuffer),
	); err != nil {
		h.logger.Errorf("Failed to append to stream_events batch: %v", err)
		return err
	}

	if err := streamEventsBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send stream_events batch: %v", err)
		return err
	}

	// Serialize tracks to JSON for track_metadata column
	var trackMetadataJSON string
	if tracks := streamBuffer.GetTracks(); len(tracks) > 0 {
		if jsonBytes, err := json.Marshal(tracks); err == nil {
			trackMetadataJSON = string(jsonBytes)
		}
	}

	// ALSO write to stream_health_metrics table for detailed health tracking and rebuffering_events MV
	healthBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_health_metrics (
			timestamp, tenant_id, internal_name, node_id, buffer_state,
			has_issues, issues_description, track_count, track_metadata
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_health_metrics batch: %v", err)
		return err
	}

	if err := healthBatch.Append(
		event.Timestamp,
		event.TenantID,
		internalName,
		mt.GetNodeId(),
		streamBuffer.GetBufferState(),
		nilIfZeroBool(streamBuffer.GetHasIssues()),
		nilIfEmptyString(streamBuffer.GetIssuesDescription()),
		nilIfZeroUint16(streamBuffer.GetTrackCount()),
		trackMetadataJSON,
	); err != nil {
		h.logger.Errorf("Failed to append to stream_health_metrics batch: %v", err)
		return err
	}

	if err := healthBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send stream_health_metrics batch: %v", err)
		return err
	}

	h.logger.Debugf("Successfully processed stream buffer event for stream: %s (written to both stream_events and stream_health_metrics)", streamBuffer.GetStreamName())
	return nil
}

// processStreamEnd handles STREAM_END webhook events
func (h *AnalyticsHandler) processStreamEnd(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream end event: %s", event.EventID)

	// Parse MistTrigger envelope and extract StreamEndTrigger
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
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
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, node_id, event_type,
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

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		internalName,
		mt.GetNodeId(),
		"stream_end",
		downloaded,
		uploaded,
		totalViewers,
		totalInputs,
		totalOutputs,
		viewerSeconds,
		marshalTypedEventData(&streamEnd),
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	h.logger.Debugf("Successfully processed stream end event for stream: %s", streamEnd.GetStreamName())
	return nil
}

func appendAndSend(batch interface {
	Append(args ...interface{}) error
	Send() error
}, args ...interface{}) error {
	if err := batch.Append(args...); err != nil {
		return err
	}
	return batch.Send()
}

// processTrackList handles track list events with quality metrics
func (h *AnalyticsHandler) processTrackList(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing track list event: %s", event.EventID)

	// Parse LiveTrackListTrigger from protobuf
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
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
			timestamp, event_id, tenant_id, internal_name, node_id, 
			track_list, track_count, video_track_count, audio_track_count,
			primary_width, primary_height, primary_fps, primary_video_codec, primary_video_bitrate,
			quality_tier, primary_audio_channels, primary_audio_sample_rate, 
			primary_audio_codec, primary_audio_bitrate
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare track list batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		internalName,
		mt.GetNodeId(),
		marshalTypedEventData(trackList.GetTracks()), // serialize tracks as JSON
		trackList.GetTotalTracks(),                   // track_count - required
		trackList.GetVideoTrackCount(),               // video_track_count - required
		trackList.GetAudioTrackCount(),               // audio_track_count - required
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
	); err != nil {
		h.logger.Errorf("Failed to append track list data: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send track list batch: %v", err)
		return err
	}

	// Detect quality changes if we have previous track data
	if err := h.detectQualityChanges(ctx, event); err != nil {
		h.logger.Errorf("Failed to detect quality changes: %v", err)
		// Don't fail the main operation for this
	}

	h.logger.Debugf("Successfully processed track list for stream: %s", trackList.GetStreamName())
	return nil
}

// processBandwidthThreshold handles bandwidth threshold events
func (h *AnalyticsHandler) processBandwidthThreshold(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing bandwidth threshold event: %s", event.EventID)

	// Parse StreamBandwidthTrigger from MistTrigger envelope
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_StreamBandwidth)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for stream_bandwidth")
	}
	bandwidthThreshold := tp.StreamBandwidth
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(bandwidthThreshold.GetStreamName())

	// Write to stream_events for threshold alerts using typed data
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, event_type, node_id,
			current_bytes_per_sec, threshold_exceeded, threshold_value, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare bandwidth threshold batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		internalName,
		"stream_bandwidth",
		mt.GetNodeId(),
		bandwidthThreshold.GetCurrentBytesPerSecond(),
		nilIfFalse(bandwidthThreshold.GetThresholdExceeded()),
		nilIfZeroUint64(bandwidthThreshold.GetThresholdValue()),
		marshalTypedEventData(&bandwidthThreshold),
	); err != nil {
		h.logger.Errorf("Failed to append bandwidth threshold event: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send bandwidth threshold batch: %v", err)
		return err
	}

	h.logger.Debugf("Successfully processed bandwidth threshold for stream: %s", bandwidthThreshold.GetStreamName())
	return nil
}

// detectQualityChanges detects and records quality tier changes
func (h *AnalyticsHandler) detectQualityChanges(ctx context.Context, event kafka.AnalyticsEvent) error {
	// For now, we'll record every track list event as a potential change
	// In a full implementation, we'd query the previous state and compare

	// Parse LiveTrackListTrigger from MistTrigger envelope
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_TrackList)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for track_list")
	}
	trackList := tp.TrackList

	currentQuality := trackList.GetQualityTier()
	currentCodec := trackList.GetPrimaryVideoCodec()
	currentResolution := ""
	if trackList.GetPrimaryWidth() > 0 && trackList.GetPrimaryHeight() > 0 {
		currentResolution = fmt.Sprintf("%dx%d", trackList.GetPrimaryWidth(), trackList.GetPrimaryHeight())
	}

	// Simple change detection - record when we have quality info
	if currentQuality != "" || currentCodec != "" {
		batch, err := h.clickhouse.PrepareBatch(ctx, `
			INSERT INTO track_change_events (
				timestamp, event_id, tenant_id, internal_name, node_id,
				change_type, new_tracks, new_quality_tier, new_resolution, new_codec
			)`)
		if err != nil {
			return err
		}

		if err := batch.Append(
			event.Timestamp,
			event.EventID,
			event.TenantID,
			trackList.GetStreamName(),
			"",             // node_id not in LiveTrackListTrigger
			"track_update", // Generic change type
			marshalTypedEventData(trackList.GetTracks()),
			currentQuality,
			currentResolution,
			currentCodec,
		); err != nil {
			return err
		}

		if err := batch.Send(); err != nil {
			return err
		}
	}

	return nil
}

// nilIfZero returns nil if the value is zero/empty, otherwise returns the value
func nilIfZero[T comparable](value T) interface{} {
	var zero T
	if value == zero {
		return nil
	}
	return value
}

// Utility functions for data conversion
func convertToInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return 0
}

func convertToInt64(v interface{}) int64 {
	switch val := v.(type) {
	case int:
		return int64(val)
	case int64:
		return val
	case float64:
		return int64(val)
	case string:
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i
		}
	}
	return 0
}

func convertToFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return 0
}

func convertToFloat32(v interface{}) float32 {
	switch val := v.(type) {
	case float32:
		return val
	case float64:
		return float32(val)
	case int:
		return float32(val)
	case int64:
		return float32(val)
	case string:
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return float32(f)
		}
	}
	return 0
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
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_ClipLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for clip_lifecycle")
	}
	cl := tp.ClipLifecycleData

	// Required - normalize internal name by stripping any prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(cl.GetInternalName())
	tenantID := event.TenantID

	// Optional - extract from enriched ClipLifecycleData
	// Note: Only extract fields that exist in clip_events ClickHouse schema
	var (
		startUnix, stopUnix         interface{}
		ingestNode, percent         interface{}
		message, filePath, s3url    interface{}
		sizeBytes                   interface{}
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

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "attempt").Inc()
	}

	// 1. Write to live_artifacts (current state - ReplacingMergeTree)
	stateBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO live_artifacts (
			tenant_id, request_id, internal_name, content_type, stage,
			progress_percent, error_message, requested_at, started_at, completed_at,
			clip_start_unix, clip_stop_unix, file_path, s3_url, size_bytes,
			processing_node_id, updated_at
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}

	// Map stage string for consistency
	stageStr := cl.GetStage().String()

	if err := stateBatch.Append(
		tenantID,
		cl.GetRequestId(),
		internalName,
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
	); err != nil {
		h.logger.Errorf("Failed to append to live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}

	if err := stateBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "attempt").Inc()
	}

	// 2. Write to clip_events (historical log - MergeTree)
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO clip_events (
			timestamp, tenant_id, internal_name, request_id, stage, content_type,
			start_unix, stop_unix, ingest_node_id,
			percent, message, file_path, s3_url, size_bytes
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
		internalName,
		cl.GetRequestId(),
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
	tp, ok := mt.GetTriggerPayload().(*pb.MistTrigger_DvrLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for dvr_lifecycle")
	}
	dvrData := tp.DvrLifecycleData

	var tenantID, internalName string
	if dvrData.TenantId != nil {
		tenantID = *dvrData.TenantId
	}
	if dvrData.InternalName != nil {
		// Normalize internal name by stripping any prefix for consistent analytics keys
		internalName = mist.ExtractInternalName(*dvrData.InternalName)
	}

	// Map status to stage
	stageStr := dvrData.GetStatus().String()

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "attempt").Inc()
	}

	// 1. Write to live_artifacts (current state - ReplacingMergeTree)
	stateBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO live_artifacts (
			tenant_id, request_id, internal_name, content_type, stage,
			progress_percent, error_message, requested_at, started_at, completed_at,
			segment_count, manifest_path, file_path, processing_node_id, updated_at
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}

	if err := stateBatch.Append(
		tenantID,
		dvrData.GetDvrHash(),
		internalName,
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
		nilIfEmptyString(mt.GetNodeId()),
		event.Timestamp,
	); err != nil {
		h.logger.Errorf("Failed to append to live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}

	if err := stateBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "attempt").Inc()
	}

	// 2. Write to clip_events (historical log - MergeTree)
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO clip_events (
			timestamp, tenant_id, internal_name, request_id, stage, content_type,
			start_unix, stop_unix, ingest_node_id, file_path, message
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
		internalName,
		dvrData.GetDvrHash(),                   // request_id
		stageStr,                               // stage
		"dvr",                                  // content_type
		nilIfZeroInt64(dvrData.GetStartedAt()), // start_unix = DVR started_at
		nilIfZeroInt64(dvrData.GetEndedAt()),   // stop_unix = DVR ended_at
		mt.GetNodeId(),                         // ingest_node_id
		dvrData.GetManifestPath(),              // file_path
		message,                                // message
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
