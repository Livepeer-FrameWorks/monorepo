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

// HandleAnalyticsEvent processes analytics events and writes to appropriate databases
func (h *AnalyticsHandler) HandleAnalyticsEvent(ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
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
		err = h.processViewerConnection(ctx, ydb, event, true)
	case "viewer_disconnect":
		err = h.processViewerConnection(ctx, ydb, event, false)
	case "stream_buffer":
		err = h.processStreamBuffer(ctx, event)
	case "stream_end":
		err = h.processStreamEnd(ctx, ydb, event)
	case "push_rewrite":
		err = h.processPushRewrite(ctx, event)
	case "viewer_resolve":
		err = h.processStreamIngest(ctx, event)
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
		err = h.processStreamLifecycle(ctx, ydb, event)
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

// processStreamLifecycle handles stream lifecycle events
func (h *AnalyticsHandler) processStreamLifecycle(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
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
	internalName := streamLifecycle.GetInternalName()

	// Write ONLY to ClickHouse - no PostgreSQL writes for events
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "attempt").Inc()
	}

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, node_id, event_type,
			buffer_state, downloaded_bytes, uploaded_bytes, total_viewers, total_inputs, 
			total_outputs, viewer_seconds, health_score, has_issues, issues_description,
			track_count, quality_tier, primary_width, primary_height, primary_fps, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return err
	}

	if err := batch.Append(
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
		streamLifecycle.GetHealthScore(),
		streamLifecycle.GetHasIssues(),
		streamLifecycle.GetIssuesDescription(),
		streamLifecycle.GetTrackCount(),
		streamLifecycle.GetQualityTier(),
		streamLifecycle.GetPrimaryWidth(),
		streamLifecycle.GetPrimaryHeight(),
		streamLifecycle.GetPrimaryFps(),
		marshalTypedEventData(&streamLifecycle),
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "success").Inc()
	}

	if err := h.reduceStreamLifecycle(ctx, ydb, event, streamLifecycle); err != nil {
		h.logger.Errorf("Failed to reduce stream lifecycle: %v", err)
	}

	return nil
}

// processStreamIngest handles stream ingest events
func (h *AnalyticsHandler) processStreamIngest(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream ingest event: %s", event.EventID)

	// Parse ViewerResolveTrigger from protobuf (viewer-side resolve)
	var vr pb.MistTrigger
	if err := h.parseProtobufData(event, &vr); err != nil {
		return fmt.Errorf("failed to parse MistTrigger envelope: %w", err)
	}
	payload, ok := vr.GetTriggerPayload().(*pb.MistTrigger_ViewerResolve)
	if !ok || payload == nil {
		return fmt.Errorf("event is not ViewerResolveTrigger")
	}
	streamIngest := payload.ViewerResolve

	// Write to ClickHouse for time-series analysis - ONLY fields that exist in ingest events
	batch, err := h.clickhouse.PrepareBatch(ctx, `
        INSERT INTO stream_events (
            timestamp, event_id, tenant_id, internal_name, node_id, event_type,
            stream_key, user_id, hostname, push_url, protocol, latitude, longitude, location, event_data
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
		streamIngest.GetRequestedStream(),
		nodeID,
		"stream_view",
		nil, // stream_key N/A
		nil, // user_id N/A
		streamIngest.GetViewerHost(),
		streamIngest.GetRequestUrl(),
		nil, // protocol N/A
		nilIfZeroFloat64(streamIngest.GetLatitude()),
		nilIfZeroFloat64(streamIngest.GetLongitude()),
		streamIngest.GetCity(),
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

// processUserConnection handles user connection events
func (h *AnalyticsHandler) processUserConnection(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing user connection event: %s", event.EventID)

	// Parse UserNewTrigger or UserEndTrigger from protobuf
	var tp pb.MistTrigger
	var userNew pb.ViewerConnectTrigger
	var userEnd pb.ViewerDisconnectTrigger
	var isNewUser bool

	// Try parsing as UserNewTrigger first
	if err := h.parseProtobufData(event, &tp); err == nil {
		tp, ok := tp.GetTriggerPayload().(*pb.MistTrigger_ViewerConnect)
		if !ok || tp == nil {
			return fmt.Errorf("unexpected payload for user_new")
		}
		userNew = *tp.ViewerConnect
		isNewUser = true
	} else {
		// Try parsing as UserEndTrigger
		if err := h.parseProtobufData(event, &tp); err != nil {
			tp, ok := tp.GetTriggerPayload().(*pb.MistTrigger_ViewerDisconnect)
			if !ok || tp == nil {
				return fmt.Errorf("unexpected payload for user_end")
			}
			userEnd = *tp.ViewerDisconnect
			return fmt.Errorf("failed to parse UserTrigger: %w", err)
		}
		isNewUser = false
	}

	// Extract data based on trigger type
	var streamName, sessionID, connector, nodeID, host, requestURL string
	var countryCode, city interface{}
	var latitude, longitude interface{}
	var duration, upBytes, downBytes int64

	if isNewUser {
		streamName = userNew.GetStreamName()
		sessionID = userNew.GetSessionId()
		connector = userNew.GetConnector()
		host = userNew.GetHost()
		requestURL = userNew.GetRequestUrl()
	} else {
		streamName = userEnd.GetStreamName()
		sessionID = userEnd.GetSessionId()
		connector = userEnd.GetConnector()
		host = userEnd.GetHost()
		nodeID = userEnd.GetNodeId()
		duration = userEnd.GetDuration()
		upBytes = userEnd.GetUpBytes()
		downBytes = userEnd.GetDownBytes()

		if userEnd.GetCountryCode() != "" {
			countryCode = userEnd.GetCountryCode()
		}
		if userEnd.GetCity() != "" {
			city = userEnd.GetCity()
		}
		if userEnd.GetLatitude() != 0 {
			latitude = userEnd.GetLatitude()
		}
		if userEnd.GetLongitude() != 0 {
			longitude = userEnd.GetLongitude()
		}
	}

	// Write to ClickHouse for time-series analysis
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO connection_events (
			event_id, timestamp, tenant_id, internal_name,
			session_id, connection_addr, connector, node_id, request_url,
			country_code, city, latitude, longitude,
			event_type, session_duration, bytes_transferred
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.EventID,
		event.Timestamp,
		event.TenantID,
		streamName,
		sessionID,
		host, // connection_addr -> host
		connector,
		nodeID,
		requestURL,
		// Geographic data from typed payload
		countryCode,
		city,
		latitude,
		longitude,
		map[bool]string{true: "connect", false: "disconnect"}[isNewUser],
		int(duration),
		upBytes+downBytes,
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	if err := h.reduceUserConnection(ctx, ydb, event, streamName, sessionID, connector, nodeID, host, requestURL, countryCode, city, latitude, longitude, duration, upBytes, downBytes, isNewUser); err != nil {
		h.logger.Errorf("Failed to reduce user connection: %v", err)
	}

	return nil
}

// processViewerConnection writes connection_events (connect/disconnect) and reduces state
func (h *AnalyticsHandler) processViewerConnection(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent, isConnect bool) error {
	var mt pb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}

	var streamName, sessionID, connector, nodeID, host, requestURL string
	var duration, upBytes, downBytes int64
	var countryCode, city interface{}
	var latitude, longitude interface{}

	switch p := mt.GetTriggerPayload().(type) {
	case *pb.MistTrigger_ViewerConnect:
		vc := p.ViewerConnect
		streamName = vc.GetStreamName()
		sessionID = vc.GetSessionId()
		connector = vc.GetConnector()
		host = vc.GetHost()
		requestURL = vc.GetRequestUrl()
		nodeID = mt.GetNodeId()
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
	default:
		return fmt.Errorf("unexpected payload for viewer connection")
	}

	batch, err := h.clickhouse.PrepareBatch(ctx, `
        INSERT INTO connection_events (
            event_id, timestamp, tenant_id, internal_name,
            session_id, connection_addr, connector, node_id, request_url,
            country_code, city, latitude, longitude,
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
		eventType,
		durationUI,
		bytesTransferred,
	); err != nil {
		return err
	}
	if err := batch.Send(); err != nil {
		return err
	}

	// Reduce Postgres state
	return h.reduceUserConnection(ctx, ydb, event, streamName, sessionID, connector, nodeID, host, requestURL, countryCode, city, latitude, longitude, duration, upBytes, downBytes, isConnect)
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
		values = []interface{}{
			event.Timestamp,
			event.EventID,
			event.TenantID,
			p.GetStreamName(),
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
		values = []interface{}{
			event.Timestamp,
			event.EventID,
			event.TenantID,
			p.GetStreamName(),
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

	batch, err := h.clickhouse.PrepareBatch(ctx, `
        INSERT INTO stream_events (
            timestamp, event_id, tenant_id, internal_name, node_id, event_type,
            stream_key, hostname, push_url, protocol, latitude, longitude, location, event_data
        )`)
	if err != nil {
		return err
	}

	var prot interface{}
	if pr.Protocol != nil && *pr.Protocol != "" {
		prot = *pr.Protocol
	}
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

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		event.TenantID,
		pr.GetStreamName(),
		mt.GetNodeId(),
		"push-rewrite",
		pr.GetStreamName(),
		pr.GetHostname(),
		pr.GetPushUrl(),
		prot,
		lat,
		lon,
		loc,
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
		recording.GetStreamName(),
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
		streamView.GetStreamName(),
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

	// Write to ClickHouse routing_events table - using ACTUAL fields from LoadBalancingPayload
	batch, err := h.clickhouse.PrepareBatch(ctx, `
        INSERT INTO routing_events (
            timestamp, tenant_id, stream_name, selected_node, status, details, score, 
            client_ip, client_country, client_latitude, client_longitude,
            node_latitude, node_longitude, node_name,
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

	if err := batch.Append(
		event.Timestamp,
		event.TenantID,
		loadBalancing.GetInternalName(), // stream_name -> internal_name
		loadBalancing.GetSelectedNode(),
		loadBalancing.GetStatus(),
		loadBalancing.GetDetails(),
		int64(loadBalancing.GetScore()),
		loadBalancing.GetClientIp(),
		loadBalancing.GetClientCountry(),
		loadBalancing.GetLatitude(),
		loadBalancing.GetLongitude(),
		loadBalancing.GetNodeLatitude(),
		loadBalancing.GetNodeLongitude(),
		loadBalancing.GetNodeName(),
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
		clientLifecycle.GetInternalName(),
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

	// Write to ClickHouse for time-series analysis using typed data
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO node_metrics (
			timestamp, tenant_id, node_id, cpu_usage, ram_max, ram_current,
			up_speed, down_speed, stream_count, is_healthy
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.TenantID,
		nodeLifecycle.GetNodeId(),
		float32(nodeLifecycle.GetCpuTenths())/10.0, // cpu_tenths (0-1000) -> percentage
		int64(nodeLifecycle.GetRamMax()),
		int64(nodeLifecycle.GetRamCurrent()),
		int64(nodeLifecycle.GetUpSpeed()),
		int64(nodeLifecycle.GetDownSpeed()),
		int(nodeLifecycle.GetActiveStreams()),
		nodeLifecycle.GetIsHealthy(),
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

	// Write to ClickHouse stream_events table
	streamEventsBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, node_id, event_type,
			buffer_state, health_score, has_issues, issues_description, track_count, 
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
		streamBuffer.GetStreamName(),
		mt.GetNodeId(),
		"stream_buffer",
		streamBuffer.GetBufferState(),
		nilIfZeroFloat32(streamBuffer.GetHealthScore()),
		nilIfZeroBool(streamBuffer.GetHasIssues()),
		streamBuffer.GetIssuesDescription(),
		streamBuffer.GetTrackCount(),
		streamBuffer.GetQualityTier(),
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

	// ALSO write to stream_health_metrics table for detailed health tracking and rebuffering_events MV
	healthBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_health_metrics (
			timestamp, tenant_id, internal_name, node_id, buffer_state, health_score, 
			has_issues, issues_description, track_count, frame_jitter_ms, keyframe_stability_ms,
			codec, bitrate, fps, width, height, frame_ms_max, frame_ms_min, 
			frames_max, frames_min, keyframe_ms_max
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_health_metrics batch: %v", err)
		return err
	}

	if err := healthBatch.Append(
		event.Timestamp,
		event.TenantID,
		streamBuffer.GetStreamName(),
		mt.GetNodeId(),
		streamBuffer.GetBufferState(),
		nilIfZeroFloat32(streamBuffer.GetHealthScore()),
		nilIfZeroBool(streamBuffer.GetHasIssues()),
		streamBuffer.GetIssuesDescription(),
		streamBuffer.GetTrackCount(),
		nil, // frame_jitter_ms N/A
		nil, // keyframe_stability_ms N/A
		nil, // codec N/A
		nil, // bitrate N/A
		nil, // fps N/A
		nil, // width N/A
		nil, // height N/A
		nil, // frame_ms_max N/A
		nil, // frame_ms_min N/A
		nil, // frames_max N/A
		nil, // frames_min N/A
		nil, // keyframe_interval_ms N/A
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
func (h *AnalyticsHandler) processStreamEnd(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
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
		streamEnd.GetStreamName(),
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

	// Also update PostgreSQL reduced state
	if err := h.reduceStreamEnd(ctx, ydb, event, streamEnd); err != nil {
		h.logger.Errorf("Failed to reduce stream end: %v", err)
	}

	h.logger.Debugf("Successfully processed stream end event for stream: %s", streamEnd.GetStreamName())
	return nil
}

func (h *AnalyticsHandler) reduceStreamLifecycle(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent, streamLifecycle *pb.StreamLifecycleUpdate) error {
	status := streamLifecycle.GetStatus()
	var startTime interface{}
	switch status {
	case "start", "started", "ingest_start", "live":
		startTime = event.Timestamp
	default:
		startTime = nil
	}

	// Extract metrics from detailed stream lifecycle events using protobuf data
	nodeID := streamLifecycle.GetNodeId()
	viewers := streamLifecycle.GetTotalViewers()
	trackCount := streamLifecycle.GetTrackCount()
	upBytes := streamLifecycle.GetUploadedBytes()
	downBytes := streamLifecycle.GetDownloadedBytes()
	inputs := streamLifecycle.GetTotalInputs()
	var outputs uint32 = 0 // total_outputs not in StreamLifecycleUpdate

	// Calculate bitrate from bandwidth (convert bytes to kbps)
	var avgBitrateKbps int
	if upBytes > 0 {
		avgBitrateKbps = int((upBytes * 8) / 1000) // Convert bytes to kbps, round down
	}

	// Map Mist status strictly to enum when it matches known values
	var mistStatus interface{}
	switch status {
	case "offline", "init", "boot", "wait", "ready", "shutdown", "invalid":
		mistStatus = status
	default:
		mistStatus = nil
	}

	_, err := ydb.ExecContext(ctx, `
			INSERT INTO periscope.stream_analytics (
				tenant_id, internal_name, stream_id, status, mist_status, session_start_time, 
				current_viewers, total_connections, track_count,
				bandwidth_in, bandwidth_out, upbytes, downbytes,
				inputs, outputs, bitrate_kbps, node_id, last_updated
			)
			VALUES ($1, $2, NULLIF($3,'')::uuid, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
			ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
				status = EXCLUDED.status,
				mist_status = COALESCE(EXCLUDED.mist_status, periscope.stream_analytics.mist_status),
				session_start_time = COALESCE(periscope.stream_analytics.session_start_time, EXCLUDED.session_start_time),
				current_viewers = EXCLUDED.current_viewers,
				peak_viewers = GREATEST(periscope.stream_analytics.peak_viewers, EXCLUDED.current_viewers),
				total_connections = GREATEST(periscope.stream_analytics.total_connections, EXCLUDED.total_connections),
				track_count = EXCLUDED.track_count,
				bandwidth_in = EXCLUDED.bandwidth_in,
				bandwidth_out = EXCLUDED.bandwidth_out,
				upbytes = EXCLUDED.upbytes,
				downbytes = EXCLUDED.downbytes,
				inputs = EXCLUDED.inputs,
				outputs = EXCLUDED.outputs,
				bitrate_kbps = EXCLUDED.bitrate_kbps,
				node_id = EXCLUDED.node_id,
				last_updated = EXCLUDED.last_updated
		`, event.TenantID, streamLifecycle.GetInternalName(), streamLifecycle.GetInternalName(), status, mistStatus, startTime,
		viewers, viewers, trackCount, downBytes, upBytes, upBytes, downBytes,
		inputs, outputs, avgBitrateKbps, nodeID, event.Timestamp)
	return err
}

func (h *AnalyticsHandler) reduceUserConnection(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent, streamName, sessionID, connector, nodeID, host, requestURL string, countryCode, city, latitude, longitude interface{}, duration, upBytes, downBytes int64, isConnect bool) error {
	// Data is already passed as parameters from the calling handler
	action := map[bool]string{true: "connect", false: "disconnect"}[isConnect]
	tenantID := event.TenantID
	internal := streamName

	switch action {
	case "connect":
		_, err := ydb.ExecContext(ctx, `
                        INSERT INTO periscope.stream_analytics (tenant_id, internal_name, stream_id, current_viewers, peak_viewers, total_connections, last_updated)
                        VALUES ($1,$2,NULLIF($3,'')::uuid,1,1,1,$4)
                        ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
                                current_viewers = periscope.stream_analytics.current_viewers + 1,
                                peak_viewers = GREATEST(periscope.stream_analytics.peak_viewers, periscope.stream_analytics.current_viewers + 1),
                                total_connections = periscope.stream_analytics.total_connections + 1,
                                last_updated = EXCLUDED.last_updated
                `, tenantID, internal, internal, event.Timestamp)
		return err
	case "disconnect":
		_, err := ydb.ExecContext(ctx, `
                        INSERT INTO periscope.stream_analytics (tenant_id, internal_name, stream_id, last_updated)
                        VALUES ($1,$2,NULLIF($3,'')::uuid,$4)
                        ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
                                current_viewers = GREATEST(periscope.stream_analytics.current_viewers - 1, 0),
                                total_session_duration = periscope.stream_analytics.total_session_duration + $5,
                                upbytes = periscope.stream_analytics.upbytes + $6,
                                downbytes = periscope.stream_analytics.downbytes + $7,
                                bandwidth_in = periscope.stream_analytics.bandwidth_in + $6,
                                bandwidth_out = periscope.stream_analytics.bandwidth_out + $7,
                                last_updated = EXCLUDED.last_updated
                `, tenantID, internal, internal, event.Timestamp, duration, upBytes, downBytes)
		return err
	default:
		_, err := ydb.ExecContext(ctx, `
                        INSERT INTO periscope.stream_analytics (tenant_id, internal_name, stream_id, last_updated)
                        VALUES ($1,$2,NULLIF($3,'')::uuid,$4)
                        ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
                                last_updated = EXCLUDED.last_updated
                `, tenantID, internal, internal, event.Timestamp)
		return err
	}
}

func (h *AnalyticsHandler) reduceStreamEnd(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent, streamEnd *pb.StreamEndTrigger) error {
	status := "ended" // Stream end events indicate the stream has ended
	_, err := ydb.ExecContext(ctx, `
                INSERT INTO periscope.stream_analytics (tenant_id, internal_name, stream_id, status, session_end_time, last_updated)
                VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5,$5)
                ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
                        status = EXCLUDED.status,
                        session_end_time = EXCLUDED.session_end_time,
                        last_updated = EXCLUDED.last_updated
        `, event.TenantID, streamEnd.GetStreamName(), streamEnd.GetStreamName(), status, event.Timestamp)
	return err
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
		trackList.GetStreamName(),
		mt.GetNodeId(),
		marshalTypedEventData(trackList.GetTracks()), // serialize tracks as JSON
		trackList.GetTotalTracks(),
		trackList.GetVideoTrackCount(),
		trackList.GetAudioTrackCount(),
		trackList.GetPrimaryWidth(),
		trackList.GetPrimaryHeight(),
		float32(trackList.GetPrimaryFps()),
		trackList.GetPrimaryVideoCodec(),
		trackList.GetPrimaryVideoBitrate(),
		trackList.GetQualityTier(),
		trackList.GetPrimaryAudioChannels(),
		trackList.GetPrimaryAudioSampleRate(),
		trackList.GetPrimaryAudioCodec(),
		trackList.GetPrimaryAudioBitrate(),
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
		bandwidthThreshold.GetStreamName(),
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

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO clip_events (
			timestamp, tenant_id, internal_name, request_id, stage, content_type, title, format,
			start_unix, stop_unix, start_ms, stop_ms, duration_sec,
			ingest_node_id, storage_node_id, routing_distance_km,
			percent, message, file_path, s3_url, size_bytes
		)`)
	if err != nil {
		return err
	}

	// Required
	internalName := ""
	if cl.GetInternalName() != "" {
		internalName = cl.GetInternalName()
	}
	tenantID := event.TenantID

	// Optional
	var (
		title, format                                     interface{}
		startUnix, stopUnix, startMs, stopMs, durationSec interface{}
		ingestNode, storageNode, routeKm, percent         interface{}
		message, filePath, s3url                          interface{}
		sizeBytes                                         interface{}
	)
	// No title or format in ClipLifecycleData
	// Use optional fields
	if cl.GetStartedAt() != 0 {
		startUnix = cl.GetStartedAt()
	}
	if cl.GetCompletedAt() != 0 {
		stopUnix = cl.GetCompletedAt()
	}
	// No start_ms/stop_ms in ClipLifecycleData
	// No duration_sec in ClipLifecycleData
	if cl.GetNodeId() != "" {
		ingestNode = cl.GetNodeId()
	}
	// No storage node in ClipLifecycleData
	// No routing distance in ClipLifecycleData
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

	if err := batch.Append(
		event.Timestamp,
		tenantID,
		internalName,
		cl.GetRequestId(),
		cl.GetStage().String(),
		"", // content_type not in ClipLifecycleData
		title,
		format,
		startUnix,
		stopUnix,
		startMs,
		stopMs,
		durationSec,
		ingestNode,
		storageNode,
		routeKm,
		percent,
		message,
		filePath,
		s3url,
		sizeBytes,
	); err != nil {
		return err
	}
	return batch.Send()
}

// processDVRLifecycle handles DVR lifecycle events
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

	// Write to ClickHouse dvr_events table
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO dvr_events (
			timestamp, tenant_id, internal_name, event_type, source,
			dvr_hash, status, node_id, storage_path
		)`)
	if err != nil {
		return err
	}

	var tenantID, internalName string
	if dvrData.TenantId != nil {
		tenantID = *dvrData.TenantId
	}
	if dvrData.InternalName != nil {
		internalName = *dvrData.InternalName
	}

	if err := batch.Append(
		event.Timestamp,
		tenantID,
		internalName,
		event.EventType,
		event.Source,
		dvrData.GetDvrHash(),
		dvrData.GetStatus().String(),
		mt.GetNodeId(),
		dvrData.GetManifestPath(), // use manifest_path instead of storage_path
	); err != nil {
		return err
	}

	return batch.Send()
}
