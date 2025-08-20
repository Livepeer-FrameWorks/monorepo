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

// HandleAnalyticsEvent processes analytics events and writes to appropriate databases
func (h *AnalyticsHandler) HandleAnalyticsEvent(ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
	start := time.Now()
	ctx := context.Background()

	// Track analytics event received
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "received").Inc()
	}

	// Process based on event type
	var err error
	switch event.EventType {
	case "stream-lifecycle":
		err = h.processStreamLifecycle(ctx, ydb, event)
	case "stream-ingest":
		err = h.processStreamIngest(ctx, event)
	case "user-connection":
		err = h.processUserConnection(ctx, ydb, event)
	case "push-lifecycle":
		err = h.processPushLifecycle(ctx, event)
	case "recording-lifecycle":
		err = h.processRecordingLifecycle(ctx, event)
	case "client-lifecycle":
		err = h.processClientLifecycle(ctx, event)
	case "node-lifecycle":
		err = h.processNodeLifecycle(ctx, event)
	case "stream-buffer":
		err = h.processStreamBuffer(ctx, event)
	case "stream-end":
		err = h.processStreamEnd(ctx, ydb, event)
	case "stream-view":
		err = h.processStreamView(ctx, event)
	case "load-balancing":
		err = h.processLoadBalancing(ctx, event)
	case "track-list":
		err = h.processTrackList(ctx, event)
	case "bandwidth-threshold":
		err = h.processBandwidthThreshold(ctx, event)
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
func (h *AnalyticsHandler) processStreamLifecycle(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream lifecycle event: %s", event.EventID)

	var internalName string
	if event.InternalName != nil {
		internalName = *event.InternalName
	} else {
		h.logger.Warnf("No internal_name provided in stream lifecycle event: %s", event.EventID)
		return nil
	}

	// Write ONLY to ClickHouse - no PostgreSQL writes for events
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "attempt").Inc()
	}

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			event_id, timestamp, tenant_id, internal_name, event_type, status, node_id, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return err
	}

	if err := batch.Append(
		event.EventID,
		event.Timestamp,
		getTenantIDFromEvent(event),
		internalName,
		"stream-lifecycle",
		event.Data["status"],
		event.Data["node_id"],
		marshalEventData(event.Data),
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

	// Extract health metrics if this event contains detailed stream data
	if err := h.extractHealthMetricsFromLifecycle(ctx, event); err != nil {
		h.logger.Errorf("Failed to extract health metrics from lifecycle event: %v", err)
		// Don't fail the whole event processing for health metrics issues
	}

	if err := h.reduceStreamLifecycle(ctx, ydb, event); err != nil {
		h.logger.Errorf("Failed to reduce stream lifecycle: %v", err)
	}

	return nil
}

// processStreamIngest handles stream ingest events
func (h *AnalyticsHandler) processStreamIngest(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream ingest event: %s", event.EventID)

	// Write to ClickHouse for time-series analysis
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, event_type, status, node_id, ingest_type, protocol, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		getTenantIDFromEvent(event),
		*event.InternalName,
		"stream-ingest",
		event.Data["status"],
		event.Data["node_id"],
		event.Data["ingest_type"],
		event.Data["protocol"],
		marshalEventData(event.Data),
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

	// Map Helmsman fields to ClickHouse schema
	action, _ := event.Data["action"].(string) // "connect" or "disconnect"
	connectionAddr := event.Data["connection_addr"]
	userAgent := event.Data["user_agent"]
	connector := event.Data["connector"]
	sessionID := event.Data["session_id"]

	// Derive session_duration and bytes_transferred if provided (USER_END)
	var sessionDuration int
	if v, ok := event.Data["seconds_connected"]; ok {
		sessionDuration = convertToInt(v)
	}
	var bytesTransferred int64
	if down, ok := event.Data["down_bytes"]; ok {
		bytesTransferred += convertToInt64(down)
	}
	if up, ok := event.Data["up_bytes"]; ok {
		bytesTransferred += convertToInt64(up)
	}

	// Write to ClickHouse for time-series analysis
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO connection_events (
			event_id, timestamp, tenant_id, internal_name, user_id, session_id,
			connection_addr, user_agent, connector, node_id,
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
		getTenantIDFromEvent(event),
		*event.InternalName,
		event.UserID, // may be nil → ClickHouse driver handles as NULL/String default
		sessionID,
		connectionAddr,
		userAgent,
		connector,
		event.Data["node_id"],
		// No geo data available from USER_NEW/USER_END webhooks - leave fields empty/null
		nil, // country_code (NULL)
		nil, // city (NULL)
		nil, // latitude (NULL)
		nil, // longitude (NULL)
		action,
		sessionDuration,
		bytesTransferred,
	); err != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	if err := h.reduceUserConnection(ctx, ydb, event); err != nil {
		h.logger.Errorf("Failed to reduce user connection: %v", err)
	}

	return nil
}

// processPushLifecycle handles push lifecycle events
func (h *AnalyticsHandler) processPushLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing push lifecycle event: %s", event.EventID)

	// Write to ClickHouse for time-series analysis
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, event_type, status, node_id, target, protocol, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		getTenantIDFromEvent(event),
		*event.InternalName,
		"push-lifecycle",
		event.Data["status"],
		event.Data["node_id"],
		event.Data["target"],
		event.Data["protocol"],
		marshalEventData(event.Data),
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

// processRecordingLifecycle handles recording lifecycle events
func (h *AnalyticsHandler) processRecordingLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing recording lifecycle event: %s", event.EventID)

	// Write to ClickHouse for time-series analysis
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, event_type, status, node_id, file_size, duration, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		getTenantIDFromEvent(event),
		*event.InternalName,
		"recording-lifecycle",
		event.Data["status"],
		event.Data["node_id"],
		convertToInt64(event.Data["file_size"]),
		convertToInt(event.Data["duration"]),
		marshalEventData(event.Data),
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

	// Write to ClickHouse for time-series analysis
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, event_type, status, node_id, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		getTenantIDFromEvent(event),
		*event.InternalName,
		"stream-view",
		"request",
		event.Data["node_id"],
		marshalEventData(event.Data),
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

	data := event.Data
	if data == nil {
		h.logger.Warnf("No data in load balancing event: %s", event.EventID)
		return nil
	}

	// Write to ClickHouse for time-series analysis
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO routing_events (
			timestamp, tenant_id, stream_name, selected_node, status, details, score, client_ip, client_country, client_region, client_city, client_latitude, client_longitude, node_scores, routing_metadata
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	country := data["client_country"]
	if country == nil {
		country = data["country_code"]
	}
	region := data["client_region"]
	if region == nil {
		region = data["region"]
	}
	city := data["client_city"]
	if city == nil {
		city = data["city"]
	}

	// Resolve stream name/internal_name string
	internalName := ""
	if event.InternalName != nil {
		internalName = *event.InternalName
	} else if v, ok := data["internal_name"].(string); ok {
		internalName = v
	} else if v, ok := data["stream_name"].(string); ok {
		internalName = v
	}

	if err := batch.Append(
		event.Timestamp,
		getTenantIDFromEvent(event),
		internalName,
		data["selected_node"],
		data["status"],
		data["details"],
		convertToInt64(data["score"]),
		data["client_ip"],
		country,
		region,
		city,
		convertToFloat64(data["client_latitude"]),
		convertToFloat64(data["client_longitude"]),
		data["node_scores"],
		data["routing_metadata"],
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
		"stream_name": internalName,
	}).Debug("Processed load balancing event")

	return nil
}

// processClientLifecycle handles per-client bandwidth and connection metrics
func (h *AnalyticsHandler) processClientLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	data := event.Data
	if data == nil {
		h.logger.Warnf("No data in client lifecycle event: %s", event.EventID)
		return nil
	}

	// Write time-series performance metrics to ClickHouse (stream_health_metrics)
	if err := h.writeStreamMetrics(ctx, event); err != nil {
		h.logger.WithError(err).Error("Failed to write client lifecycle metrics")
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

	// Write to ClickHouse for time-series analysis
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
		getTenantIDFromEvent(event),
		event.Data["node_id"],
		convertToFloat32(event.Data["cpu_usage"]),
		convertToInt64(event.Data["ram_max"]),
		convertToInt64(event.Data["ram_current"]),
		convertToInt64(event.Data["up_speed"]),
		convertToInt64(event.Data["down_speed"]),
		convertToInt(event.Data["stream_count"]),
		event.Data["is_healthy"],
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

	// Write to stream_events for historical record
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, event_type, status, node_id, event_data
		)`)
	if err != nil {
		return err
	}
	if err := appendAndSend(batch, event.Timestamp, event.EventID, getTenantIDFromEvent(event), *event.InternalName, "stream-buffer", event.Data["status"], event.Data["node_id"], marshalEventData(event.Data)); err != nil {
		return err
	}

	// Write rich health metrics to stream_health_metrics table
	healthBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_health_metrics (
			timestamp, tenant_id, internal_name, node_id,
			bitrate, fps, width, height, codec, profile,
			buffer_state, frame_jitter_ms, keyframe_stability_ms,
			issues_description, has_issues, health_score, track_count,
			frame_ms_max, frame_ms_min, frames_max, frames_min,
			keyframe_ms_max, keyframe_ms_min, packets_sent, packets_lost,
			audio_channels, audio_sample_rate, audio_codec, audio_bitrate
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare health metrics batch: %v", err)
		return err
	}

	if err := healthBatch.Append(
		event.Timestamp,
		getTenantIDFromEvent(event),
		*event.InternalName,
		event.Data["node_id"],
		convertToInt(event.Data["bitrate"]),
		convertToFloat32(event.Data["fps"]),
		convertToInt(event.Data["width"]),
		convertToInt(event.Data["height"]),
		event.Data["codec"],
		event.Data["profile"],
		event.Data["buffer_state"],
		convertToFloat32(event.Data["frame_jitter_ms"]),
		convertToFloat32(event.Data["keyframe_stability_ms"]),
		event.Data["issues_description"],
		convertToInt(event.Data["has_issues"]),
		convertToFloat32(event.Data["health_score"]),
		convertToInt(event.Data["track_count"]),
		convertToFloat32(event.Data["frame_ms_max"]),
		convertToFloat32(event.Data["frame_ms_min"]),
		convertToInt(event.Data["frames_max"]),
		convertToInt(event.Data["frames_min"]),
		convertToFloat32(event.Data["keyframe_ms_max"]),
		convertToFloat32(event.Data["keyframe_ms_min"]),
		convertToInt64(event.Data["packets_sent"]),
		convertToInt64(event.Data["packets_lost"]),
		convertToInt(event.Data["audio_channels"]),
		convertToInt(event.Data["audio_sample_rate"]),
		event.Data["audio_codec"],
		convertToInt(event.Data["audio_bitrate"]),
	); err != nil {
		h.logger.Errorf("Failed to append health metrics: %v", err)
		return err
	}

	if err := healthBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send health metrics batch: %v", err)
		return err
	}

	h.logger.Debugf("Successfully processed stream buffer health metrics for stream: %s", *event.InternalName)
	return nil
}

// processStreamEnd handles STREAM_END webhook events
func (h *AnalyticsHandler) processStreamEnd(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
	batch, err := h.clickhouse.PrepareBatch(ctx, `
                INSERT INTO stream_events (
                        timestamp, event_id, tenant_id, internal_name, event_type, status, node_id, event_data
                )`)
	if err != nil {
		return err
	}
	if err := appendAndSend(batch, event.Timestamp, event.EventID, getTenantIDFromEvent(event), *event.InternalName, "stream-end", event.Data["status"], event.Data["node_id"], marshalEventData(event.Data)); err != nil {
		return err
	}
	if err := h.reduceStreamEnd(ctx, ydb, event); err != nil {
		h.logger.Errorf("Failed to reduce stream end: %v", err)
	}
	return nil
}

func (h *AnalyticsHandler) reduceStreamLifecycle(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
	if event.InternalName == nil {
		return nil
	}

	status, _ := event.Data["status"].(string)
	var startTime interface{}
	switch status {
	case "start", "started", "ingest_start", "live":
		startTime = event.Timestamp
	default:
		startTime = nil
	}

	// Extract metrics from detailed stream lifecycle events (from Prometheus monitor)
	source, _ := event.Data["source"].(string)
	if source == "mistserver_api" {
		// This is a detailed event with actual metrics data
		nodeID, _ := event.Data["node_id"].(string)
		viewers := convertToInt(event.Data["viewers"])
		clients := convertToInt(event.Data["clients"])
		trackCount := convertToInt(event.Data["track_count"])
		upBytes := convertToInt64(event.Data["bandwidth_in"])
		downBytes := convertToInt64(event.Data["bandwidth_out"])
		packetsSent := convertToInt64(event.Data["packets_sent"])
		packetsLost := convertToInt64(event.Data["packets_lost"])
		inputs := convertToInt(event.Data["inputs"])
		outputs := convertToInt(event.Data["outputs"])

		// Calculate bitrate from bandwidth (convert bytes to kbps)
		var avgBitrate float64
		if upBytes > 0 {
			avgBitrate = float64(upBytes*8) / 1000 // Convert bytes to kbps
		}

		_, err := ydb.ExecContext(ctx, `
			INSERT INTO stream_analytics (
				tenant_id, internal_name, status, session_start_time, 
				current_viewers, total_connections, track_count,
				bandwidth_in, bandwidth_out, upbytes, downbytes,
				packets_sent, packets_lost, inputs, outputs,
				avg_bitrate, primary_node_id, last_updated
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
			ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
				status = EXCLUDED.status,
				session_start_time = COALESCE(stream_analytics.session_start_time, EXCLUDED.session_start_time),
				current_viewers = EXCLUDED.current_viewers,
				peak_viewers = GREATEST(stream_analytics.peak_viewers, EXCLUDED.current_viewers),
				total_connections = GREATEST(stream_analytics.total_connections, EXCLUDED.total_connections),
				track_count = EXCLUDED.track_count,
				bandwidth_in = EXCLUDED.bandwidth_in,
				bandwidth_out = EXCLUDED.bandwidth_out,
				upbytes = EXCLUDED.upbytes,
				downbytes = EXCLUDED.downbytes,
				packets_sent = EXCLUDED.packets_sent,
				packets_lost = EXCLUDED.packets_lost,
				inputs = EXCLUDED.inputs,
				outputs = EXCLUDED.outputs,
				avg_bitrate = EXCLUDED.avg_bitrate,
				primary_node_id = EXCLUDED.primary_node_id,
				last_updated = EXCLUDED.last_updated
		`, getTenantIDFromEvent(event), *event.InternalName, status, startTime,
			viewers, clients, trackCount, upBytes, downBytes, upBytes, downBytes,
			packetsSent, packetsLost, inputs, outputs, avgBitrate, nodeID, event.Timestamp)
		return err
	} else {
		// Basic lifecycle event without detailed metrics
		_, err := ydb.ExecContext(ctx, `
			INSERT INTO stream_analytics (tenant_id, internal_name, status, session_start_time, last_updated)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
				status = EXCLUDED.status,
				session_start_time = COALESCE(stream_analytics.session_start_time, EXCLUDED.session_start_time),
				last_updated = EXCLUDED.last_updated
		`, getTenantIDFromEvent(event), *event.InternalName, status, startTime, event.Timestamp)
		return err
	}
}

func (h *AnalyticsHandler) reduceUserConnection(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
	if event.InternalName == nil {
		return nil
	}
	action, _ := event.Data["action"].(string)
	tenantID := getTenantIDFromEvent(event)
	internal := *event.InternalName
	upBytes := convertToInt64(event.Data["up_bytes"])
	downBytes := convertToInt64(event.Data["down_bytes"])
	duration := convertToInt(event.Data["seconds_connected"])

	switch action {
	case "connect":
		_, err := ydb.ExecContext(ctx, `
                        INSERT INTO stream_analytics (tenant_id, internal_name, current_viewers, peak_viewers, total_connections, last_updated)
                        VALUES ($1,$2,1,1,1,$3)
                        ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
                                current_viewers = stream_analytics.current_viewers + 1,
                                peak_viewers = GREATEST(stream_analytics.peak_viewers, stream_analytics.current_viewers + 1),
                                total_connections = stream_analytics.total_connections + 1,
                                last_updated = EXCLUDED.last_updated
                `, tenantID, internal, event.Timestamp)
		return err
	case "disconnect":
		_, err := ydb.ExecContext(ctx, `
                        INSERT INTO stream_analytics (tenant_id, internal_name, last_updated)
                        VALUES ($1,$2,$3)
                        ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
                                current_viewers = GREATEST(stream_analytics.current_viewers - 1, 0),
                                total_session_duration = stream_analytics.total_session_duration + $4,
                                upbytes = stream_analytics.upbytes + $5,
                                downbytes = stream_analytics.downbytes + $6,
                                bandwidth_in = stream_analytics.bandwidth_in + $5,
                                bandwidth_out = stream_analytics.bandwidth_out + $6,
                                last_updated = EXCLUDED.last_updated
                `, tenantID, internal, event.Timestamp, duration, upBytes, downBytes)
		return err
	default:
		_, err := ydb.ExecContext(ctx, `
                        INSERT INTO stream_analytics (tenant_id, internal_name, last_updated)
                        VALUES ($1,$2,$3)
                        ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
                                last_updated = EXCLUDED.last_updated
                `, tenantID, internal, event.Timestamp)
		return err
	}
}

func (h *AnalyticsHandler) reduceStreamEnd(ctx context.Context, ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
	if event.InternalName == nil {
		return nil
	}
	status, _ := event.Data["status"].(string)
	_, err := ydb.ExecContext(ctx, `
                INSERT INTO stream_analytics (tenant_id, internal_name, status, session_end_time, last_updated)
                VALUES ($1,$2,$3,$4,$4)
                ON CONFLICT (tenant_id, internal_name) DO UPDATE SET
                        status = EXCLUDED.status,
                        session_end_time = EXCLUDED.session_end_time,
                        last_updated = EXCLUDED.last_updated
        `, getTenantIDFromEvent(event), *event.InternalName, status, event.Timestamp)
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

// writeStreamMetrics writes time-series metrics to ClickHouse
func (h *AnalyticsHandler) writeStreamMetrics(ctx context.Context, event kafka.AnalyticsEvent) error {
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_health_metrics (
			timestamp, tenant_id, internal_name, node_id,
			bitrate, fps, buffer_health, packets_sent, packets_lost, packets_retransmitted,
			bandwidth_in, bandwidth_out
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}

	if err := batch.Append(
		event.Timestamp,
		getTenantIDFromEvent(event),
		*event.InternalName,
		event.Data["node_id"],
		convertToInt(event.Data["bitrate"]),
		convertToFloat64(event.Data["fps"]),
		convertToFloat32(event.Data["buffer_health"]),
		convertToInt64(event.Data["packets_sent"]),
		convertToInt64(event.Data["packets_lost"]),
		convertToInt64(event.Data["packets_retransmitted"]),
		convertToInt64(event.Data["bandwidth_in"]),
		convertToInt64(event.Data["bandwidth_out"]),
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

// writeViewerMetric writes a single viewer_metrics sample derived from client-lifecycle
func (h *AnalyticsHandler) writeViewerMetric(ctx context.Context, event kafka.AnalyticsEvent) error {
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO viewer_metrics (
			timestamp, tenant_id, internal_name,
			viewer_count, connection_type, node_id,
			country_code, city, latitude, longitude,
			connection_quality, buffer_health
		)`)
	if err != nil {
		return err
	}

	// Compute connection_quality = 1 - (packets_lost/packets_sent) when possible
	var quality float64
	ps := convertToInt64(event.Data["packets_sent"])
	pl := convertToInt64(event.Data["packets_lost"])
	if ps > 0 {
		loss := float64(pl) / float64(ps)
		if loss < 0 {
			loss = 0
		}
		if loss > 1 {
			loss = 1
		}
		quality = 1 - loss
	} else {
		quality = 0
	}

	// Buffer health
	bh := convertToFloat32(event.Data["buffer_health"]) // may be nil, driver handles NULL

	if err := batch.Append(
		event.Timestamp,
		getTenantIDFromEvent(event),
		*event.InternalName,
		uint32(1),
		event.Data["protocol"],
		event.Data["node_id"],
		"",
		"",
		float64(0),
		float64(0),
		float32(quality),
		bh,
	); err != nil {
		return err
	}
	return batch.Send()
}

// processTrackList handles track list events with quality metrics
func (h *AnalyticsHandler) processTrackList(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing track list event: %s", event.EventID)

	// Write to track_list_events with enhanced quality metrics
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

	trackList := ""
	if v, ok := event.Data["track_list"].(string); ok {
		trackList = v
	}

	if err := batch.Append(
		event.Timestamp,
		event.EventID,
		getTenantIDFromEvent(event),
		*event.InternalName,
		event.Data["node_id"],
		trackList,
		convertToInt(event.Data["track_count"]),
		convertToInt(event.Data["video_track_count"]),
		convertToInt(event.Data["audio_track_count"]),
		convertToInt(event.Data["primary_width"]),
		convertToInt(event.Data["primary_height"]),
		convertToFloat32(event.Data["primary_fps"]),
		event.Data["primary_video_codec"],
		convertToInt(event.Data["primary_video_bitrate"]),
		event.Data["quality_tier"],
		convertToInt(event.Data["primary_audio_channels"]),
		convertToInt(event.Data["primary_audio_sample_rate"]),
		event.Data["primary_audio_codec"],
		convertToInt(event.Data["primary_audio_bitrate"]),
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

	h.logger.Debugf("Successfully processed track list for stream: %s", *event.InternalName)
	return nil
}

// processBandwidthThreshold handles bandwidth threshold events
func (h *AnalyticsHandler) processBandwidthThreshold(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing bandwidth threshold event: %s", event.EventID)

	// Write to stream_events for threshold alerts
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, event_type, status, node_id, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare bandwidth threshold batch: %v", err)
		return err
	}

	if err := appendAndSend(batch,
		event.Timestamp,
		event.EventID,
		getTenantIDFromEvent(event),
		*event.InternalName,
		"bandwidth-threshold",
		event.Data["threshold_type"],
		event.Data["node_id"],
		marshalEventData(event.Data)); err != nil {
		h.logger.Errorf("Failed to write bandwidth threshold event: %v", err)
		return err
	}

	h.logger.Debugf("Successfully processed bandwidth threshold for stream: %s", *event.InternalName)
	return nil
}

// detectQualityChanges detects and records quality tier changes
func (h *AnalyticsHandler) detectQualityChanges(ctx context.Context, event kafka.AnalyticsEvent) error {
	// For now, we'll record every track list event as a potential change
	// In a full implementation, we'd query the previous state and compare
	currentQuality := event.Data["quality_tier"]
	currentCodec := event.Data["primary_video_codec"]
	currentResolution := ""
	if w, ok := event.Data["primary_width"]; ok {
		if h, ok := event.Data["primary_height"]; ok {
			currentResolution = fmt.Sprintf("%dx%d", convertToInt(w), convertToInt(h))
		}
	}

	// Simple change detection - record when we have quality info
	if currentQuality != nil || currentCodec != nil {
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
			getTenantIDFromEvent(event),
			*event.InternalName,
			event.Data["node_id"],
			"track_update", // Generic change type
			event.Data["track_list"],
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

// getTenantIDFromEvent extracts tenant_id from event if present
func getTenantIDFromEvent(event kafka.AnalyticsEvent) string {
	if event.TenantID != "" {
		return event.TenantID
	}
	if event.Data != nil {
		if v, ok := event.Data["tenant_id"].(string); ok && v != "" {
			return v
		}
	}
	return "00000000-0000-0000-0000-000000000001"
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

func marshalEventData(m map[string]interface{}) string {
	if m == nil {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// extractHealthMetricsFromLifecycle extracts health metrics from stream-lifecycle events
// that contain detailed health_data and track_details from the Prometheus monitor
func (h *AnalyticsHandler) extractHealthMetricsFromLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	// Only process events from the mistserver_api source (Prometheus monitor)
	source, hasSource := event.Data["source"].(string)
	if !hasSource || source != "mistserver_api" {
		return nil // Skip events that aren't from the detailed monitor
	}

	// Check if this event has health_data and track_details
	healthData, hasHealthData := event.Data["health_data"]
	trackDetails, hasTrackDetails := event.Data["track_details"]

	if !hasHealthData && !hasTrackDetails {
		return nil // No health data to extract
	}

	h.logger.Debugf("Extracting health metrics from lifecycle event: %s", event.EventID)

	// Prepare health metrics batch
	healthBatch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_health_metrics (
			timestamp, tenant_id, internal_name, node_id,
			bitrate, fps, width, height, buffer_size, buffer_used,
			buffer_health, packets_sent, packets_lost, packets_retransmitted,
			bandwidth_in, bandwidth_out, codec, profile, track_count,
			gop_size, buffer_state, frame_jitter_ms, keyframe_stability_ms,
			issues_description, has_issues, health_score,
			frame_ms_max, frame_ms_min, frames_max, frames_min,
			keyframe_ms_max, keyframe_ms_min,
			audio_channels, audio_sample_rate, audio_codec, audio_bitrate
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare health metrics batch: %v", err)
		return err
	}

	// Extract basic metrics from the event data
	bitrate := convertToInt(event.Data["bandwidth_in"])
	nodeID := ""
	if nid, ok := event.Data["node_id"].(string); ok {
		nodeID = nid
	}

	// Extract metrics from track_details if available
	var primaryTrack map[string]interface{}
	var trackCount int
	if trackDetails != nil {
		if tracks, ok := trackDetails.([]interface{}); ok {
			trackCount = len(tracks)
			// Get primary track (first video track or first track)
			for _, track := range tracks {
				if trackMap, ok := track.(map[string]interface{}); ok {
					if trackType, hasType := trackMap["type"].(string); hasType && trackType == "video" {
						primaryTrack = trackMap
						break
					}
					if primaryTrack == nil {
						primaryTrack = trackMap // Use first track if no video track found
					}
				}
			}
		}
	}

	// Extract video metrics from primary track
	var width, height, gopSize int
	var fps float32
	var codec, profile string
	var audioChannels, audioSampleRate, audioBitrate int
	var audioCodec string

	if primaryTrack != nil {
		width = convertToInt(primaryTrack["width"])
		height = convertToInt(primaryTrack["height"])
		fps = convertToFloat32(primaryTrack["fps"])
		if c, ok := primaryTrack["codec"].(string); ok {
			codec = c
		}
		if trackType, hasType := primaryTrack["type"].(string); hasType && trackType == "video" {
			bitrate = convertToInt(primaryTrack["bitrate_kbps"]) // Use track bitrate if available
		}

		// Audio metrics (if this is an audio track or has audio info)
		if trackType, hasType := primaryTrack["type"].(string); hasType && trackType == "audio" {
			audioChannels = convertToInt(primaryTrack["channels"])
			audioSampleRate = convertToInt(primaryTrack["sample_rate"])
			audioBitrate = convertToInt(primaryTrack["bitrate_kbps"])
			if ac, ok := primaryTrack["codec"].(string); ok {
				audioCodec = ac
			}
		}
	}

	// Extract health score and buffer state
	var healthScore float32
	bufferState := "unknown"
	var hasIssues int

	if healthData != nil {
		if hd, ok := healthData.(map[string]interface{}); ok {
			healthScore = convertToFloat32(hd["health_score"])
			if state, ok := hd["buffer_state"].(string); ok {
				bufferState = state
			}
			hasIssues = convertToInt(hd["has_issues"])
		}
	}

	if err := healthBatch.Append(
		event.Timestamp,
		getTenantIDFromEvent(event),
		*event.InternalName,
		nodeID,
		bitrate,
		fps,
		width,
		height,
		convertToInt(event.Data["buffer_size"]),
		convertToInt(event.Data["buffer_used"]),
		convertToFloat32(event.Data["buffer_health"]),
		convertToInt64(event.Data["packets_sent"]),
		convertToInt64(event.Data["packets_lost"]),
		convertToInt64(event.Data["packets_retransmitted"]),
		convertToInt64(event.Data["bandwidth_in"]),
		convertToInt64(event.Data["bandwidth_out"]),
		codec,
		profile,
		trackCount,
		gopSize,
		bufferState,
		convertToFloat32(event.Data["frame_jitter_ms"]),
		convertToFloat32(event.Data["keyframe_stability_ms"]),
		event.Data["issues_description"],
		hasIssues,
		healthScore,
		convertToFloat32(event.Data["frame_ms_max"]),
		convertToFloat32(event.Data["frame_ms_min"]),
		convertToInt(event.Data["frames_max"]),
		convertToInt(event.Data["frames_min"]),
		convertToFloat32(event.Data["keyframe_ms_max"]),
		convertToFloat32(event.Data["keyframe_ms_min"]),
		audioChannels,
		audioSampleRate,
		audioCodec,
		audioBitrate,
	); err != nil {
		h.logger.Errorf("Failed to append health metrics from lifecycle: %v", err)
		return err
	}

	if err := healthBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send health metrics from lifecycle: %v", err)
		return err
	}

	h.logger.Debugf("Successfully extracted health metrics from lifecycle event for stream: %s", *event.InternalName)
	return nil
}
