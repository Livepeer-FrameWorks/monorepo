package handlers

import (
	"context"
	"encoding/json"
	"strconv"

	"frameworks/pkg/database"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
)

// AnalyticsHandler handles analytics events
type AnalyticsHandler struct {
	clickhouse database.ClickHouseNativeConn
	logger     logging.Logger
}

// NewAnalyticsHandler creates a new analytics handler
func NewAnalyticsHandler(clickhouse database.ClickHouseNativeConn, logger logging.Logger) *AnalyticsHandler {
	return &AnalyticsHandler{
		clickhouse: clickhouse,
		logger:     logger,
	}
}

// HandleAnalyticsEvent processes analytics events and writes to appropriate databases
func (h *AnalyticsHandler) HandleAnalyticsEvent(ydb database.PostgresConn, event kafka.AnalyticsEvent) error {
	ctx := context.Background()

	// Process based on event type
	switch event.EventType {
	case "stream-lifecycle":
		return h.processStreamLifecycle(ctx, ydb, event)
	case "stream-ingest":
		return h.processStreamIngest(ctx, event)
	case "user-connection":
		return h.processUserConnection(ctx, ydb, event)
	case "push-lifecycle":
		return h.processPushLifecycle(ctx, event)
	case "recording-lifecycle":
		return h.processRecordingLifecycle(ctx, event)
	case "client-lifecycle":
		return h.processClientLifecycle(ctx, event)
	case "node-lifecycle":
		return h.processNodeLifecycle(ctx, event)
	case "stream-buffer":
		return h.processStreamBuffer(ctx, event)
	case "stream-end":
		return h.processStreamEnd(ctx, ydb, event)
	case "stream-view":
		return h.processStreamView(ctx, event)
	case "load-balancing":
		return h.processLoadBalancing(ctx, event)
	case "track-list":
		return h.processTrackList(ctx, event)
	default:
		h.logger.Warnf("Unknown event type: %s", event.EventType)
		return nil
	}
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
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			event_id, timestamp, tenant_id, internal_name, event_type, status, node_id, event_data
		)`)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
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
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
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
		// geo fields not available here
		"",         // country_code
		"",         // city
		float64(0), // latitude
		float64(0), // longitude
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

	// Write time-series metrics to ClickHouse
	if err := h.writeStreamMetrics(ctx, event); err != nil {
		h.logger.WithError(err).Error("Failed to write client lifecycle metrics")
		return err
	}

	// Write viewer_metrics sample for realtime (no geo enrichment)
	if err := h.writeViewerMetric(ctx, event); err != nil {
		h.logger.WithError(err).Warn("Failed to write viewer metric sample")
	}

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

// processStreamBuffer handles STREAM_BUFFER webhook events
func (h *AnalyticsHandler) processStreamBuffer(ctx context.Context, event kafka.AnalyticsEvent) error {
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO stream_events (
			timestamp, event_id, tenant_id, internal_name, event_type, status, node_id, event_data
		)`)
	if err != nil {
		return err
	}
	return appendAndSend(batch, event.Timestamp, event.EventID, getTenantIDFromEvent(event), *event.InternalName, "stream-buffer", event.Data["status"], event.Data["node_id"], marshalEventData(event.Data))
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

// processTrackList handles track list events
func (h *AnalyticsHandler) processTrackList(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing track list event: %s", event.EventID)

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO track_list_events (
			timestamp, event_id, tenant_id, internal_name, node_id, track_list, track_count
		)`)
	if err != nil {
		return err
	}
	trackList := ""
	if v, ok := event.Data["track_list"].(string); ok {
		trackList = v
	}
	var count int
	if v, ok := event.Data["track_count"]; ok {
		count = convertToInt(v)
	}
	if err := batch.Append(event.Timestamp, event.EventID, getTenantIDFromEvent(event), *event.InternalName, event.Data["node_id"], trackList, count); err != nil {
		return err
	}
	return batch.Send()
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
