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
	"frameworks/pkg/validation"

	"github.com/prometheus/client_golang/prometheus"
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

// convertKafkaEventToTyped converts a kafka.AnalyticsEvent to a validation.KafkaEvent with typed data
func (h *AnalyticsHandler) convertKafkaEventToTyped(event kafka.AnalyticsEvent) (*validation.KafkaEvent, error) {
	// Create typed event structure
	typedEvent := &validation.KafkaEvent{
		ID:            event.EventID,
		Type:          event.EventType,
		EventID:       event.EventID,
		EventType:     event.EventType,
		Timestamp:     event.Timestamp,
		Source:        event.Source,
		SchemaVersion: "1.0",
	}

	// Event already carries typed payloads; copy through
	switch validation.EventType(event.EventType) {
	case validation.EventStreamIngest:
		typedEvent.Data.StreamIngest = event.Data.StreamIngest
	case validation.EventStreamView:
		typedEvent.Data.StreamView = event.Data.StreamView
	case validation.EventStreamLifecycle, validation.EventStreamBuffer, validation.EventStreamEnd:
		typedEvent.Data.StreamLifecycle = event.Data.StreamLifecycle
	case validation.EventUserConnection:
		typedEvent.Data.UserConnection = event.Data.UserConnection
	case validation.EventClientLifecycle:
		typedEvent.Data.ClientLifecycle = event.Data.ClientLifecycle
	case validation.EventTrackList:
		typedEvent.Data.TrackList = event.Data.TrackList
	case validation.EventRecordingLifecycle:
		typedEvent.Data.Recording = event.Data.Recording
	case validation.EventPushLifecycle:
		typedEvent.Data.PushLifecycle = event.Data.PushLifecycle
	case validation.EventNodeLifecycle:
		typedEvent.Data.NodeLifecycle = event.Data.NodeLifecycle
	case validation.EventBandwidthThreshold:
		typedEvent.Data.BandwidthThreshold = event.Data.BandwidthThreshold
	case validation.EventLoadBalancing:
		typedEvent.Data.LoadBalancing = event.Data.LoadBalancing
	case validation.EventClipLifecycle:
		typedEvent.Data.ClipLifecycle = event.Data.ClipLifecycle
	}

	return typedEvent, nil
}

// HandleAnalyticsEvent processes analytics events and writes to appropriate databases
func (h *AnalyticsHandler) HandleAnalyticsEvent(ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
	start := time.Now()
	ctx := context.Background()

	// Track analytics event received
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "received").Inc()
	}

	// Convert to typed event for validation and type safety
	typedEvent, err := h.convertKafkaEventToTyped(event)
	if err != nil {
		h.logger.WithError(err).WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
		}).Error("Failed to convert event to typed structure")

		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "validation_failed").Inc()
		}
		return err
	}

	// Validate typed event against shared schema
	v := validation.NewEventValidator()
	be := validation.BaseEvent{
		EventID:       event.EventID,
		EventType:     validation.EventType(event.EventType),
		Timestamp:     event.Timestamp,
		Source:        event.Source,
		StreamID:      event.StreamID,
		UserID:        event.UserID,
		PlaybackID:    event.PlaybackID,
		InternalName:  event.InternalName,
		NodeURL:       event.NodeURL,
		SchemaVersion: event.SchemaVersion,
	}
	// Populate typed payloads for validator
	switch be.EventType {
	case validation.EventStreamIngest:
		be.StreamIngest = typedEvent.Data.StreamIngest
	case validation.EventStreamView:
		be.StreamView = typedEvent.Data.StreamView
	case validation.EventStreamLifecycle, validation.EventStreamBuffer, validation.EventStreamEnd:
		be.StreamLifecycle = typedEvent.Data.StreamLifecycle
	case validation.EventUserConnection:
		be.UserConnection = typedEvent.Data.UserConnection
	case validation.EventClientLifecycle:
		be.ClientLifecycle = typedEvent.Data.ClientLifecycle
	case validation.EventTrackList:
		be.TrackList = typedEvent.Data.TrackList
	case validation.EventRecordingLifecycle:
		be.Recording = typedEvent.Data.Recording
	case validation.EventPushLifecycle:
		be.PushLifecycle = typedEvent.Data.PushLifecycle
	case validation.EventNodeLifecycle:
		be.NodeLifecycle = typedEvent.Data.NodeLifecycle
	case validation.EventBandwidthThreshold:
		be.BandwidthThreshold = typedEvent.Data.BandwidthThreshold
	case validation.EventLoadBalancing:
		be.LoadBalancing = typedEvent.Data.LoadBalancing
	case validation.EventClipLifecycle:
		be.ClipLifecycle = typedEvent.Data.ClipLifecycle
	}
	batch := validation.BatchedEvents{
		BatchID:   event.EventID,
		Source:    event.Source,
		Timestamp: event.Timestamp,
		Events:    []validation.BaseEvent{be},
	}
	if err := v.ValidateBatch(&batch); err != nil {
		h.logger.WithError(err).WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
		}).Error("Event validation failed")
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "validation_failed").Inc()
		}
		return err
	}

	// Process based on event type using typed data
	switch validation.EventType(event.EventType) {
	case validation.EventStreamLifecycle:
		err = h.processStreamLifecycle(ctx, ydb, typedEvent)
	case validation.EventStreamIngest:
		err = h.processStreamIngest(ctx, typedEvent)
	case validation.EventUserConnection:
		err = h.processUserConnection(ctx, ydb, typedEvent)
	case validation.EventPushLifecycle:
		err = h.processPushLifecycle(ctx, typedEvent)
	case validation.EventRecordingLifecycle:
		err = h.processRecordingLifecycle(ctx, typedEvent)
	case validation.EventClientLifecycle:
		err = h.processClientLifecycle(ctx, typedEvent)
	case validation.EventNodeLifecycle:
		err = h.processNodeLifecycle(ctx, typedEvent)
	case validation.EventStreamBuffer:
		err = h.processStreamBuffer(ctx, typedEvent)
	case validation.EventStreamEnd:
		err = h.processStreamEnd(ctx, ydb, typedEvent)
	case validation.EventStreamView:
		err = h.processStreamView(ctx, typedEvent)
	case validation.EventLoadBalancing:
		err = h.processLoadBalancing(ctx, typedEvent)
	case validation.EventTrackList:
		err = h.processTrackList(ctx, typedEvent)
	case validation.EventBandwidthThreshold:
		err = h.processBandwidthThreshold(ctx, typedEvent)
	case validation.EventClipLifecycle:
		err = h.processClipLifecycle(ctx, typedEvent)
	default:
		h.logger.Warnf("Unknown event type: %s", event.EventType)
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "unknown_type").Inc()
		}
		return nil
	}

	// Track processing metrics
	if h.metrics != nil {
		if err != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "error").Inc()
		} else {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "processed").Inc()
		}
		h.metrics.BatchProcessingDuration.WithLabelValues().Observe(time.Since(start).Seconds())
	}

	return err
}

// processStreamLifecycle handles stream lifecycle events
func (h *AnalyticsHandler) processStreamLifecycle(ctx context.Context, ydb database.PostgresConn, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing stream lifecycle event: %s", event.EventID)

	// Get typed stream lifecycle payload
	if event.Data.StreamLifecycle == nil {
		h.logger.Warnf("No stream lifecycle data in event: %s", event.EventID)
		return nil
	}

	streamLifecycle := event.Data.StreamLifecycle
	internalName := streamLifecycle.InternalName

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
		getTenantIDFromKafkaEvent(*event),
		internalName,
		streamLifecycle.NodeID,
		"stream-lifecycle",
		streamLifecycle.BufferState,
		streamLifecycle.DownloadedBytes,
		streamLifecycle.UploadedBytes,
		streamLifecycle.TotalViewers,
		streamLifecycle.TotalInputs,
		streamLifecycle.TotalOutputs,
		streamLifecycle.ViewerSeconds,
		streamLifecycle.HealthScore,
		streamLifecycle.HasIssues,
		streamLifecycle.IssuesDesc,
		streamLifecycle.TrackCount,
		streamLifecycle.QualityTier,
		streamLifecycle.PrimaryWidth,
		streamLifecycle.PrimaryHeight,
		streamLifecycle.PrimaryFPS,
		marshalTypedEventData(streamLifecycle),
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

	if err := h.reduceStreamLifecycle(ctx, ydb, *event); err != nil {
		h.logger.Errorf("Failed to reduce stream lifecycle: %v", err)
	}

	return nil
}

// processStreamIngest handles stream ingest events
func (h *AnalyticsHandler) processStreamIngest(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing stream ingest event: %s", event.EventID)

	// Get typed stream ingest payload
	if event.Data.StreamIngest == nil {
		h.logger.Warnf("No stream ingest data in event: %s", event.EventID)
		return nil
	}

	streamIngest := event.Data.StreamIngest

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

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		getTenantIDFromKafkaEvent(*event),
		streamIngest.InternalName,
		streamIngest.NodeID,
		"stream-ingest",
		streamIngest.StreamKey,
		streamIngest.UserID,
		streamIngest.Hostname,
		streamIngest.PushURL,
		streamIngest.Protocol, // Protocol EXISTS in ingest events!
		streamIngest.Latitude,
		streamIngest.Longitude,
		streamIngest.Location,
		marshalTypedEventData(streamIngest),
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
func (h *AnalyticsHandler) processUserConnection(ctx context.Context, ydb database.PostgresConn, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing user connection event: %s", event.EventID)

	// Get typed user connection payload
	if event.Data.UserConnection == nil {
		h.logger.Warnf("No user connection data in event: %s", event.EventID)
		return nil
	}

	userConn := event.Data.UserConnection

	// Extract geographic data from typed payload (embedded periscope.ConnectionEvent)
	var countryCode, city interface{}
	var latitude, longitude interface{}

	if userConn.CountryCode != "" {
		countryCode = userConn.CountryCode
	}
	if userConn.City != "" {
		city = userConn.City
	}
	if userConn.Latitude != 0 {
		latitude = userConn.Latitude
	}
	if userConn.Longitude != 0 {
		longitude = userConn.Longitude
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
		getTenantIDFromKafkaEvent(*event),
		userConn.InternalName,
		userConn.SessionID,
		userConn.ConnectionAddr,
		userConn.Connector,
		userConn.NodeID,
		userConn.RequestURL,
		// Geographic data from typed payload
		countryCode,
		city,
		latitude,
		longitude,
		userConn.Action,
		int(userConn.SecondsConnected),
		int64(userConn.DownloadedBytes+userConn.UploadedBytes),
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	if err := h.reduceUserConnection(ctx, ydb, *event); err != nil {
		h.logger.Errorf("Failed to reduce user connection: %v", err)
	}

	return nil
}

// processPushLifecycle handles push lifecycle events
func (h *AnalyticsHandler) processPushLifecycle(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing push lifecycle event: %s", event.EventID)

	// Get typed push lifecycle payload
	if event.Data.PushLifecycle == nil {
		h.logger.Warnf("No push lifecycle data in event: %s", event.EventID)
		return nil
	}

	pushLifecycle := event.Data.PushLifecycle

	// Write to ClickHouse for time-series analysis - ONLY fields that exist in push events
	var batchSQL string
	var values []interface{}

	if pushLifecycle.Action == "start" {
		// PUSH_OUT_START: only has push_target
		batchSQL = `INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, node_id, event_type, 
			push_target, event_data
		)`
		values = []interface{}{
			event.Timestamp,
			event.EventID,
			getTenantIDFromKafkaEvent(*event),
			pushLifecycle.InternalName,
			pushLifecycle.NodeID,
			"push-start",
			pushLifecycle.PushTarget,
			marshalTypedEventData(pushLifecycle),
		}
	} else {
		// PUSH_END: has push_id, target URIs, status, log_messages
		batchSQL = `INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, node_id, event_type,
			push_id, push_target, target_uri_before, target_uri_after, push_status, log_messages, event_data
		)`
		values = []interface{}{
			event.Timestamp,
			event.EventID,
			getTenantIDFromKafkaEvent(*event),
			pushLifecycle.InternalName,
			pushLifecycle.NodeID,
			"push-end",
			pushLifecycle.PushID,
			pushLifecycle.PushTarget,
			pushLifecycle.TargetURIBefore,
			pushLifecycle.TargetURIAfter,
			pushLifecycle.Status,
			pushLifecycle.LogMessages,
			marshalTypedEventData(pushLifecycle),
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

// processRecordingLifecycle handles recording lifecycle events
func (h *AnalyticsHandler) processRecordingLifecycle(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing recording lifecycle event: %s", event.EventID)

	// Get typed recording payload
	if event.Data.Recording == nil {
		h.logger.Warnf("No recording data in event: %s", event.EventID)
		return nil
	}

	recording := event.Data.Recording

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
		getTenantIDFromKafkaEvent(*event),
		recording.InternalName,
		recording.NodeID,
		"recording-lifecycle",
		uint64(recording.BytesWritten),   // file_size = bytes_written
		uint32(recording.SecondsWriting), // duration = seconds_writing
		recording.FilePath,               // output_file = file_path
		marshalTypedEventData(recording),
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
func (h *AnalyticsHandler) processStreamView(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing stream view event: %s", event.EventID)

	// Get typed stream view payload
	if event.Data.StreamView == nil {
		h.logger.Warnf("No stream view data in event: %s", event.EventID)
		return nil
	}

	streamView := event.Data.StreamView

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
		getTenantIDFromKafkaEvent(*event),
		streamView.InternalName,
		streamView.NodeID,
		"stream-view",
		marshalTypedEventData(streamView),
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
func (h *AnalyticsHandler) processLoadBalancing(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing load balancing event: %s", event.EventID)

	// Get typed load balancing payload
	if event.Data.LoadBalancing == nil {
		h.logger.Warnf("No load balancing data in event: %s", event.EventID)
		return nil
	}

	loadBalancing := event.Data.LoadBalancing

	// Write to ClickHouse routing_events table - using ACTUAL fields from LoadBalancingPayload
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO routing_events (
			timestamp, tenant_id, stream_name, selected_node, status, details, score, 
			client_ip, client_country, client_latitude, client_longitude,
			node_latitude, node_longitude, node_name
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		getTenantIDFromKafkaEvent(*event),
		loadBalancing.StreamID,
		loadBalancing.SelectedNode,
		loadBalancing.Status,
		loadBalancing.Details,
		int64(loadBalancing.Score),
		loadBalancing.ClientIP,
		loadBalancing.ClientCountry,
		loadBalancing.Latitude,
		loadBalancing.Longitude,
		loadBalancing.NodeLatitude,
		loadBalancing.NodeLongitude,
		loadBalancing.NodeName,
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
		"stream_name": loadBalancing.StreamID,
	}).Debug("Processed load balancing event")

	return nil
}

// processClientLifecycle handles per-client bandwidth and connection metrics
func (h *AnalyticsHandler) processClientLifecycle(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing client lifecycle event: %s", event.EventID)

	// Get typed client lifecycle payload
	if event.Data.ClientLifecycle == nil {
		h.logger.Warnf("No client lifecycle data in event: %s", event.EventID)
		return nil
	}

	clientLifecycle := event.Data.ClientLifecycle

	// Calculate connection quality if packets were sent
	var connectionQuality *float32
	if clientLifecycle.PacketsSent > 0 {
		quality := float32(1.0 - (float64(clientLifecycle.PacketsLost) / float64(clientLifecycle.PacketsSent)))
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
		getTenantIDFromKafkaEvent(*event),
		clientLifecycle.InternalName,
		clientLifecycle.SessionID,
		clientLifecycle.NodeID,
		clientLifecycle.Protocol,
		clientLifecycle.Host,
		clientLifecycle.ConnectionTime,
		uint64(clientLifecycle.BandwidthIn),
		uint64(clientLifecycle.BandwidthOut),
		uint64(clientLifecycle.BytesDown),
		uint64(clientLifecycle.BytesUp),
		uint64(clientLifecycle.PacketsSent),
		uint64(clientLifecycle.PacketsLost),
		uint64(clientLifecycle.PacketsRetransmitted),
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
func (h *AnalyticsHandler) processNodeLifecycle(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing node lifecycle event: %s", event.EventID)

	// Get typed node lifecycle payload
	if event.Data.NodeLifecycle == nil {
		h.logger.Warnf("No node lifecycle data in event: %s", event.EventID)
		return nil
	}

	nodeLifecycle := event.Data.NodeLifecycle

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
		getTenantIDFromKafkaEvent(*event),
		nodeLifecycle.NodeID,
		float32(nodeLifecycle.CPUUsage),
		int64(nodeLifecycle.RAMMax),
		int64(nodeLifecycle.RAMCurrent),
		int64(nodeLifecycle.BandwidthUp),   // UpSpeed -> BandwidthUp
		int64(nodeLifecycle.BandwidthDown), // DownSpeed -> BandwidthDown
		int(nodeLifecycle.ActiveStreams),   // StreamCount -> ActiveStreams
		nodeLifecycle.IsHealthy,
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
func (h *AnalyticsHandler) processStreamBuffer(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing stream buffer event: %s", event.EventID)

	// Get typed stream lifecycle payload (StreamBuffer events use this payload type)
	if event.Data.StreamLifecycle == nil {
		h.logger.Warnf("No stream lifecycle data in stream buffer event: %s", event.EventID)
		return nil
	}

	streamLifecycle := event.Data.StreamLifecycle

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
		getTenantIDFromKafkaEvent(*event),
		streamLifecycle.InternalName,
		streamLifecycle.NodeID,
		"stream-buffer",
		streamLifecycle.BufferState,
		streamLifecycle.HealthScore,
		streamLifecycle.HasIssues,
		streamLifecycle.IssuesDesc,
		streamLifecycle.TrackCount,
		streamLifecycle.QualityTier,
		streamLifecycle.PrimaryWidth,
		streamLifecycle.PrimaryHeight,
		streamLifecycle.PrimaryFPS,
		marshalTypedEventData(streamLifecycle),
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
		getTenantIDFromKafkaEvent(*event),
		streamLifecycle.InternalName,
		streamLifecycle.NodeID,
		streamLifecycle.BufferState,
		streamLifecycle.HealthScore,
		streamLifecycle.HasIssues,
		streamLifecycle.IssuesDesc,
		streamLifecycle.TrackCount,
		streamLifecycle.FrameJitterMS,
		streamLifecycle.KeyFrameStabilityMS,
		streamLifecycle.PrimaryCodec,
		streamLifecycle.PrimaryBitrate,
		streamLifecycle.PrimaryFPS,
		streamLifecycle.PrimaryWidth,
		streamLifecycle.PrimaryHeight,
		streamLifecycle.FrameMSMax,
		streamLifecycle.FrameMSMin,
		streamLifecycle.FramesMax,
		streamLifecycle.FramesMin,
		streamLifecycle.KeyFrameIntervalMS,
	); err != nil {
		h.logger.Errorf("Failed to append to stream_health_metrics batch: %v", err)
		return err
	}

	if err := healthBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send stream_health_metrics batch: %v", err)
		return err
	}

	h.logger.Debugf("Successfully processed stream buffer event for stream: %s (written to both stream_events and stream_health_metrics)", streamLifecycle.InternalName)
	return nil
}

// processStreamEnd handles STREAM_END webhook events
func (h *AnalyticsHandler) processStreamEnd(ctx context.Context, ydb database.PostgresConn, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing stream end event: %s", event.EventID)

	// Get typed stream lifecycle payload (StreamEnd events use this payload type)
	if event.Data.StreamLifecycle == nil {
		h.logger.Warnf("No stream lifecycle data in stream end event: %s", event.EventID)
		return nil
	}

	streamLifecycle := event.Data.StreamLifecycle

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

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		getTenantIDFromKafkaEvent(*event),
		streamLifecycle.InternalName,
		streamLifecycle.NodeID,
		"stream-end",
		streamLifecycle.DownloadedBytes,
		streamLifecycle.UploadedBytes,
		streamLifecycle.TotalViewers,
		streamLifecycle.TotalInputs,
		streamLifecycle.TotalOutputs,
		streamLifecycle.ViewerSeconds,
		marshalTypedEventData(streamLifecycle),
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	// Also update PostgreSQL reduced state
	if err := h.reduceStreamEnd(ctx, ydb, *event); err != nil {
		h.logger.Errorf("Failed to reduce stream end: %v", err)
	}

	h.logger.Debugf("Successfully processed stream end event for stream: %s", streamLifecycle.InternalName)
	return nil
}

func (h *AnalyticsHandler) reduceStreamLifecycle(ctx context.Context, ydb database.PostgresConn, event validation.KafkaEvent) error {
	if event.Data.StreamLifecycle == nil {
		return nil
	}

	streamLifecycle := event.Data.StreamLifecycle
	status := streamLifecycle.Status
	var startTime interface{}
	switch status {
	case "start", "started", "ingest_start", "live":
		startTime = event.Timestamp
	default:
		startTime = nil
	}

	// Extract metrics from detailed stream lifecycle events using typed data
	if event.Data.StreamLifecycle != nil {
		// Use available typed data from StreamLifecyclePayload
		streamLifecycle := event.Data.StreamLifecycle
		nodeID := streamLifecycle.NodeID
		viewers := streamLifecycle.TotalViewers
		trackCount := streamLifecycle.TrackCount
		upBytes := streamLifecycle.UploadedBytes
		downBytes := streamLifecycle.DownloadedBytes
		inputs := streamLifecycle.TotalInputs
		outputs := streamLifecycle.TotalOutputs

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
		`, getTenantIDFromKafkaEvent(event), streamLifecycle.InternalName, streamLifecycle.InternalName, status, mistStatus, startTime,
			viewers, viewers, trackCount, downBytes, upBytes, upBytes, downBytes,
			inputs, outputs, avgBitrateKbps, nodeID, event.Timestamp)
		return err
	} else {
		// No StreamLifecycle data available - this shouldn't happen for stream lifecycle events
		h.logger.Warnf("StreamLifecycle event with no StreamLifecycle data: %s", event.EventID)
		return nil
	}
}

func (h *AnalyticsHandler) reduceUserConnection(ctx context.Context, ydb database.PostgresConn, event validation.KafkaEvent) error {
	if event.Data.UserConnection == nil {
		return nil
	}

	userConn := event.Data.UserConnection
	action := userConn.Action
	tenantID := getTenantIDFromKafkaEvent(event)
	internal := userConn.InternalName // From embedded periscope.ConnectionEvent
	upBytes := int64(userConn.UploadedBytes)
	downBytes := int64(userConn.DownloadedBytes)
	duration := int(userConn.SecondsConnected)

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

func (h *AnalyticsHandler) reduceStreamEnd(ctx context.Context, ydb database.PostgresConn, event validation.KafkaEvent) error {
	if event.Data.StreamLifecycle == nil {
		return nil
	}

	streamLifecycle := event.Data.StreamLifecycle
	status := streamLifecycle.Status
	_, err := ydb.ExecContext(ctx, `
                INSERT INTO periscope.stream_analytics (tenant_id, internal_name, stream_id, status, session_end_time, last_updated)
                VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5,$5)
                ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
                        status = EXCLUDED.status,
                        session_end_time = EXCLUDED.session_end_time,
                        last_updated = EXCLUDED.last_updated
        `, getTenantIDFromKafkaEvent(event), streamLifecycle.InternalName, streamLifecycle.InternalName, status, event.Timestamp)
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
func (h *AnalyticsHandler) processTrackList(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing track list event: %s", event.EventID)

	// Get typed track list payload
	if event.Data.TrackList == nil {
		h.logger.Warnf("No track list data in event: %s", event.EventID)
		return nil
	}

	trackList := event.Data.TrackList

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
		getTenantIDFromKafkaEvent(*event),
		trackList.InternalName,
		trackList.NodeID,
		trackList.TrackListJSON,
		trackList.TrackCount,
		trackList.VideoTrackCount,
		trackList.AudioTrackCount,
		trackList.PrimaryWidth,
		trackList.PrimaryHeight,
		float32(trackList.PrimaryFPS),
		trackList.PrimaryVideoCodec,
		trackList.PrimaryVideoBitrate,
		trackList.QualityTier,
		trackList.PrimaryAudioChannels,
		trackList.PrimaryAudioSampleRate,
		trackList.PrimaryAudioCodec,
		trackList.PrimaryAudioBitrate,
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

	h.logger.Debugf("Successfully processed track list for stream: %s", trackList.InternalName)
	return nil
}

// processBandwidthThreshold handles bandwidth threshold events
func (h *AnalyticsHandler) processBandwidthThreshold(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing bandwidth threshold event: %s", event.EventID)

	// Get typed bandwidth threshold payload
	if event.Data.BandwidthThreshold == nil {
		h.logger.Warnf("No bandwidth threshold data in event: %s", event.EventID)
		return nil
	}

	bandwidthThreshold := event.Data.BandwidthThreshold

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
		getTenantIDFromKafkaEvent(*event),
		bandwidthThreshold.InternalName,
		"bandwidth-threshold",
		bandwidthThreshold.NodeID,
		bandwidthThreshold.CurrentBytesPerSec,
		bandwidthThreshold.ThresholdExceeded,
		bandwidthThreshold.ThresholdValue,
		marshalTypedEventData(bandwidthThreshold),
	); err != nil {
		h.logger.Errorf("Failed to append bandwidth threshold event: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send bandwidth threshold batch: %v", err)
		return err
	}

	h.logger.Debugf("Successfully processed bandwidth threshold for stream: %s", bandwidthThreshold.InternalName)
	return nil
}

// detectQualityChanges detects and records quality tier changes
func (h *AnalyticsHandler) detectQualityChanges(ctx context.Context, event *validation.KafkaEvent) error {
	// For now, we'll record every track list event as a potential change
	// In a full implementation, we'd query the previous state and compare

	// Get typed track list payload
	if event.Data.TrackList == nil {
		return nil // No track list data to process
	}

	trackList := event.Data.TrackList
	currentQuality := trackList.QualityTier
	currentCodec := trackList.PrimaryVideoCodec
	currentResolution := ""
	if trackList.PrimaryWidth > 0 && trackList.PrimaryHeight > 0 {
		currentResolution = fmt.Sprintf("%dx%d", trackList.PrimaryWidth, trackList.PrimaryHeight)
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
			getTenantIDFromKafkaEvent(*event),
			trackList.InternalName,
			trackList.NodeID,
			"track_update", // Generic change type
			trackList.TrackListJSON,
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

// getTenantIDFromKafkaEvent extracts tenant_id from typed KafkaEvent
func getTenantIDFromKafkaEvent(event validation.KafkaEvent) string {
	// Extract tenant ID based on event type from typed payload
	switch validation.EventType(event.EventType) {
	case validation.EventStreamIngest:
		if event.Data.StreamIngest != nil {
			return event.Data.StreamIngest.TenantID
		}
	case validation.EventStreamView:
		if event.Data.StreamView != nil {
			return event.Data.StreamView.TenantID
		}
	case validation.EventStreamLifecycle:
		if event.Data.StreamLifecycle != nil {
			return event.Data.StreamLifecycle.TenantID
		}
	case validation.EventUserConnection:
		if event.Data.UserConnection != nil {
			return event.Data.UserConnection.TenantID
		}
	case validation.EventClientLifecycle:
		if event.Data.ClientLifecycle != nil {
			return event.Data.ClientLifecycle.TenantID
		}
	case validation.EventTrackList:
		if event.Data.TrackList != nil {
			return event.Data.TrackList.TenantID
		}
	case validation.EventRecordingLifecycle:
		if event.Data.Recording != nil {
			return event.Data.Recording.TenantID
		}
	case validation.EventPushLifecycle:
		if event.Data.PushLifecycle != nil {
			return event.Data.PushLifecycle.TenantID
		}
	case validation.EventBandwidthThreshold:
		if event.Data.BandwidthThreshold != nil {
			return event.Data.BandwidthThreshold.TenantID
		}
	case validation.EventLoadBalancing:
		if event.Data.LoadBalancing != nil {
			return event.Data.LoadBalancing.TenantID
		}
	case validation.EventClipLifecycle:
		if event.Data.ClipLifecycle != nil {
			return event.Data.ClipLifecycle.TenantID
		}
	}
	return "00000000-0000-0000-0000-000000000001"
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

func (h *AnalyticsHandler) processClipLifecycle(ctx context.Context, event *validation.KafkaEvent) error {
	h.logger.Infof("Processing clip lifecycle event: %s", event.EventID)
	if event.Data.ClipLifecycle == nil {
		return nil
	}
	cl := event.Data.ClipLifecycle

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
	if event.Data.ClipLifecycle.InternalName != "" {
		internalName = event.Data.ClipLifecycle.InternalName
	}
	tenantID := getTenantIDFromKafkaEvent(*event)

	// Optional
	var (
		title, format                                     interface{}
		startUnix, stopUnix, startMs, stopMs, durationSec interface{}
		ingestNode, storageNode, routeKm, percent         interface{}
		message, filePath, s3url                          interface{}
		sizeBytes                                         interface{}
	)
	if cl.Title != "" {
		title = cl.Title
	}
	if cl.Format != "" {
		format = cl.Format
	}
	if cl.StartUnix != 0 {
		startUnix = cl.StartUnix
	}
	if cl.StopUnix != 0 {
		stopUnix = cl.StopUnix
	}
	if cl.StartMs != 0 {
		startMs = cl.StartMs
	}
	if cl.StopMs != 0 {
		stopMs = cl.StopMs
	}
	if cl.DurationSec != 0 {
		durationSec = cl.DurationSec
	}
	if cl.IngestNodeID != "" {
		ingestNode = cl.IngestNodeID
	}
	if cl.StorageNodeID != "" {
		storageNode = cl.StorageNodeID
	}
	if cl.RoutingDistanceKm != 0 {
		routeKm = cl.RoutingDistanceKm
	}
	if cl.Percent != 0 {
		percent = cl.Percent
	}
	if cl.Message != "" {
		message = cl.Message
	}
	if cl.FilePath != "" {
		filePath = cl.FilePath
	}
	if cl.S3URL != "" {
		s3url = cl.S3URL
	}
	if cl.SizeBytes != 0 {
		sizeBytes = cl.SizeBytes
	}

	if err := batch.Append(
		event.Timestamp,
		tenantID,
		internalName,
		cl.RequestID,
		cl.Stage,
		cl.ContentType,
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
