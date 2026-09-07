package handlers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"

	"github.com/google/uuid"
)

// Stale-close worker. Lives here (not api_sidecar) because the live-state
// table viewer_sessions_current is populated by this ingest path from
// USER_NEW/USER_END Kafka events — Helmsman does not keep an in-memory
// session map. Co-locating the worker with the ingest writer keeps a
// single ClickHouse client authoritative for the table.
//
// The worker is conservative: it only marks a session/stream anomalous once
// when (a) there is a live-state row, (b) the row has been silent past
// the stale_close_timeout, and (c) neither a final nor anomalous row exists
// for the same natural key. The canonical *_anomalous_v views still collapse
// concurrent first observations from multiple ingest replicas.

const (
	// StaleCloseTimeout is how long after the last observation a live
	// session may linger before the worker records it as anomalous.
	StaleCloseTimeout = 4 * time.Hour

	// StaleCloseScanInterval is how often the worker scans for stale
	// entries. Independent of LedgerRebuildInterval — a 1-minute
	// cadence on a query that only touches recent rows is cheap.
	StaleCloseScanInterval = 1 * time.Minute

	// StaleCloseScanLimit bounds the result set per scan so a backlog
	// from an outage doesn't produce one giant INSERT. Excess rows
	// roll over to the next tick.
	StaleCloseScanLimit = 1000

	// StreamStateOfflineTimeout is how long a stream_state_current row may
	// go without a refresh before the backstop flips it to offline. Live
	// rows are refreshed every ~10s by Helmsman's STREAM_LIFECYCLE_UPDATE
	// poll, so 2 minutes means ~12 consecutive missed refreshes — enough
	// slack for producer-clock vs ClickHouse-clock skew. Much shorter than
	// StaleCloseTimeout: this guards the user-visible status, not billing
	// anomaly accounting.
	StreamStateOfflineTimeout = 2 * time.Minute
)

// startStaleCloseLoops launches the viewer and stream stale-close workers.
// Called from LedgerScheduler.Start.
func (s *LedgerScheduler) startStaleCloseLoops(ctx context.Context) {
	go s.runStaleCloseLoop(ctx, "viewer_sessions_anomalous", s.h.staleCloseViewerSessions)
	go s.runStaleCloseLoop(ctx, "stream_sessions_anomalous", s.h.staleCloseStreamSessions)
	go s.runStaleCloseLoop(ctx, "restream_sessions_anomalous", s.h.staleCloseRestreamSessions)
	go s.runStaleCloseLoop(ctx, "stream_state_offline", s.h.staleMarkStreamStateOffline)
}

func (s *LedgerScheduler) runStaleCloseLoop(ctx context.Context, name string, run func(context.Context) error) {
	ticker := time.NewTicker(StaleCloseScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.WithField("worker", name).Info("Stale-close worker stopping")
			return
		case <-ticker.C:
			release, leader, err := s.lease.TryAcquire(ctx, "stale-close:"+name)
			if err != nil {
				s.logger.WithError(err).WithField("worker", name).Warn("Stale-close lease acquisition failed")
				continue
			}
			if !leader {
				s.logger.WithField("worker", name).Debug("Stale-close pass assigned to another Periscope replica")
				continue
			}
			func() {
				defer func() {
					releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if releaseErr := release(releaseCtx); releaseErr != nil {
						s.logger.WithError(releaseErr).WithField("worker", name).Warn("Stale-close lease release failed")
					}
				}()
				if runErr := run(ctx); runErr != nil {
					s.logger.WithError(runErr).WithField("worker", name).Warn("Stale-close pass failed")
				}
			}()
		}
	}
}

// staleCloseRestreamSessions turns a silent non-terminal destination into an
// anomalous, explicitly non-billable fact. A real final arriving later still
// remains the sole source of delivered-minute and egress usage.
func (h *AnalyticsHandler) staleCloseRestreamSessions(ctx context.Context) error {
	cutoffMS := time.Now().Add(-StaleCloseTimeout).UnixMilli()
	rows, err := h.clickhouse.Query(ctx, fmt.Sprintf(`
		SELECT tenant_id, node_id, cluster_id, stream_id, stream_name,
		       source_generation, target_id, target_revision, platform,
		       source_started_at_ms, last_observed_at_ms, payload_raw
		FROM periscope.restream_sessions_current FINAL
		WHERE state IN ('pending', 'pushing', 'retrying', 'stopping')
		  AND last_observed_at_ms < ?
		  AND (tenant_id, node_id, source_generation, target_revision, target_id) NOT IN (
		      SELECT tenant_id, node_id, source_generation, target_revision, target_id
		      FROM periscope.restream_sessions_final
		      WHERE projection_version_ms >= toUnixTimestamp(now() - INTERVAL 90 DAY) * 1000
		      GROUP BY tenant_id, node_id, source_generation, target_revision, target_id
		  )
		  AND (tenant_id, node_id, source_generation, target_revision, target_id) NOT IN (
		      SELECT tenant_id, node_id, source_generation, target_revision, target_id
		      FROM periscope.restream_sessions_anomalous
		      WHERE projection_version_ms >= toUnixTimestamp(now() - INTERVAL 90 DAY) * 1000
		      GROUP BY tenant_id, node_id, source_generation, target_revision, target_id
		  )
		LIMIT %d`, StaleCloseScanLimit), cutoffMS)
	if err != nil {
		return fmt.Errorf("restream stale-close query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	projectionVersionMS := time.Now().UnixMilli()
	batch, err := periscopeingestdb.PrepareRestreamSessionAnomalous(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("restream_sessions_anomalous prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()
	rowsEmitted := 0
	for rows.Next() {
		var tenantValue, streamValue, generationValue, targetValue string
		var nodeID, clusterID, streamName, platform, payloadRaw string
		var targetRevision, startedAtMS, observedAtMS int64
		if err := rows.Scan(&tenantValue, &nodeID, &clusterID, &streamValue, &streamName,
			&generationValue, &targetValue, &targetRevision, &platform,
			&startedAtMS, &observedAtMS, &payloadRaw); err != nil {
			return fmt.Errorf("restream stale-close scan: %w", err)
		}
		tenantID, tenantErr := uuid.Parse(tenantValue)
		streamID, streamErr := uuid.Parse(streamValue)
		generationID, generationErr := uuid.Parse(generationValue)
		targetID, targetErr := uuid.Parse(targetValue)
		if tenantErr != nil || streamErr != nil || generationErr != nil || targetErr != nil {
			return fmt.Errorf("restream stale-close returned invalid fenced identity")
		}
		sourceEventID := fmt.Sprintf("stale:%s:%d:%s", generationID.String(), targetRevision, targetID.String())
		notes := fmt.Sprintf("stale: no RESTREAM_STATUS_FINAL within %s; started_at_ms=%d; stream_name=%s", StaleCloseTimeout, startedAtMS, streamName)
		if err := batch.Append(periscopeingestdb.RestreamSessionAnomalousRow{
			TenantID: tenantID, NodeID: nodeID, SourceEventID: sourceEventID, ClusterID: clusterID,
			StreamID: streamID, SourceGeneration: generationID, TargetID: targetID,
			TargetRevision: targetRevision, Platform: platform, ObservedAtMS: observedAtMS,
			Reason: "stale", Notes: notes, ProjectionVersionMS: projectionVersionMS,
			PayloadRaw: []byte(payloadRaw),
		}); err != nil {
			return fmt.Errorf("restream_sessions_anomalous append: %w", err)
		}
		rowsEmitted++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("restream stale-close rows: %w", err)
	}
	if rowsEmitted == 0 {
		return nil
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("restream_sessions_anomalous send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("restream_sessions_anomalous", "stale").Add(float64(rowsEmitted))
	}
	h.logger.WithField("count", rowsEmitted).Info("Stale-closed restream sessions")
	return nil
}

// staleCloseViewerSessions scans viewer_sessions_current for sessions
// whose last activity is older than StaleCloseTimeout and that have no
// matching row in viewer_sessions_final. Each matching row is appended
// to viewer_sessions_anomalous.
func (h *AnalyticsHandler) staleCloseViewerSessions(ctx context.Context) error {
	cutoff := time.Now().Add(-StaleCloseTimeout).UTC()

	// SimpleAggregateFunction columns in viewer_sessions_current still
	// require the aggregating-engine semantic on read; ClickHouse
	// materializes them inline via the engine, so we read them as
	// regular columns with FINAL to force a merge view.
	rows, err := h.clickhouse.Query(ctx, fmt.Sprintf(`
		WITH latest_qoe AS (
			SELECT tenant_id, node_id, session_id, max(timestamp) AS last_sample_at
			FROM periscope.client_qoe_samples
			WHERE timestamp >= now() - INTERVAL 24 HOUR
			  AND session_id != ''
			GROUP BY tenant_id, node_id, session_id
		), attribution AS (
			SELECT tenant_id, node_id, session_id,
			       argMaxIf(cluster_id, timestamp, event_type = 'connect' AND cluster_id != '') AS cluster_id
			FROM periscope.viewer_connection_events
			WHERE timestamp >= now() - INTERVAL 24 HOUR
			GROUP BY tenant_id, node_id, session_id
		)
		SELECT
			s.tenant_id,
			s.stream_id,
			s.session_id,
			s.node_id,
			at.cluster_id,
			toInt64(toUnixTimestamp(s.connected_at)) * 1000 AS observed_first_at_ms,
			toInt64(toUnixTimestamp(greatest(s.last_updated, ifNull(q.last_sample_at, s.last_updated)))) * 1000 AS observed_last_at_ms,
			s.session_duration
		FROM periscope.viewer_sessions_current AS s FINAL
		LEFT JOIN latest_qoe q USING (tenant_id, node_id, session_id)
		LEFT JOIN attribution at USING (tenant_id, node_id, session_id)
		WHERE greatest(s.last_updated, ifNull(q.last_sample_at, s.last_updated)) < ?
		  AND (s.disconnected_at IS NULL OR s.disconnected_at = toDateTime(0))
		  AND (s.tenant_id, s.node_id, s.session_id) NOT IN (
		      SELECT tenant_id, node_id, session_id
		      FROM periscope.viewer_sessions_final
		      WHERE projection_version_ms > toUnixTimestamp(now() - INTERVAL 30 DAY) * 1000
		      GROUP BY tenant_id, node_id, session_id
		  )
		  AND (s.tenant_id, s.node_id, s.session_id) NOT IN (
		      SELECT tenant_id, node_id, session_id
		      FROM periscope.viewer_sessions_anomalous
		      GROUP BY tenant_id, node_id, session_id
		  )
		LIMIT %d`, StaleCloseScanLimit),
		cutoff)
	if err != nil {
		return fmt.Errorf("viewer stale-close query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	projectionVersionMS := time.Now().UnixMilli()
	batch, err := periscopeingestdb.PrepareViewerSessionAnomalous(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("viewer_sessions_anomalous prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()

	rowsEmitted := 0
	for rows.Next() {
		var (
			tenantID, streamID, sessionID, nodeID string
			clusterID                             string
			observedFirstMS, observedLastMS       int64
			sessionDuration                       uint32
		)
		if err := rows.Scan(&tenantID, &streamID, &sessionID, &nodeID, &clusterID, &observedFirstMS, &observedLastMS, &sessionDuration); err != nil {
			h.logger.WithError(err).Warn("viewer stale-close scan failed; skipping row")
			continue
		}
		closedAtMS := projectionVersionMS
		notes := fmt.Sprintf("stale: no USER_END within %s", StaleCloseTimeout)
		tenantUUID, parseErr := uuid.Parse(tenantID)
		if parseErr != nil {
			return fmt.Errorf("viewer_sessions_anomalous tenant_id %q: %w", tenantID, parseErr)
		}
		streamUUID, parseErr := uuid.Parse(streamID)
		if parseErr != nil {
			return fmt.Errorf("viewer_sessions_anomalous stream_id %q: %w", streamID, parseErr)
		}
		if err := batch.Append(periscopeingestdb.ViewerSessionAnomalousRow{
			TenantID: tenantUUID, NodeID: nodeID, SessionID: sessionID,
			ClusterID: clusterID, StreamID: streamUUID,
			EstimatedDurationSeconds: sessionDuration,
			ObservedFirstAtMS:        observedFirstMS, ObservedLastAtMS: observedLastMS,
			ClosedAtMS: closedAtMS, ClosedReason: "stale", ProjectionVersionMS: projectionVersionMS,
			Notes: notes,
		}); err != nil {
			return fmt.Errorf("viewer_sessions_anomalous append: %w", err)
		}
		rowsEmitted++
	}
	if rowsEmitted == 0 {
		return nil
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("viewer_sessions_anomalous send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("viewer_sessions_anomalous", "inserted").Add(float64(rowsEmitted))
	}
	h.logger.WithField("count", rowsEmitted).Info("Stale-closed viewer sessions")
	return nil
}

// staleCloseStreamSessions does the analogous scan for streams from
// stream_state_current. Streams without a Foghorn-assigned UUID
// (stream_id_uuid empty) cannot be matched against stream_sessions_final
// and are skipped rather than mis-attributed.
func (h *AnalyticsHandler) staleCloseStreamSessions(ctx context.Context) error {
	cutoff := time.Now().Add(-StaleCloseTimeout).UTC()

	rows, err := h.clickhouse.Query(ctx, fmt.Sprintf(`
		SELECT
			tenant_id,
			stream_id,
			node_id,
			toInt64(toUnixTimestamp(ifNull(started_at, updated_at))) * 1000 AS observed_first_at_ms,
			toInt64(toUnixTimestamp(updated_at)) * 1000 AS observed_last_at_ms,
			toInt64(viewer_seconds) AS viewer_seconds_max
		FROM periscope.stream_state_current FINAL
		WHERE updated_at < ?
		  AND stream_id != toUUIDOrZero('')
		  AND status NOT IN ('offline', 'stopped', 'gone')
		   AND (tenant_id, node_id, stream_id) NOT IN (
		       SELECT tenant_id, node_id, stream_id
		       FROM periscope.stream_sessions_final
		       WHERE projection_version_ms > toUnixTimestamp(now() - INTERVAL 30 DAY) * 1000
		       GROUP BY tenant_id, node_id, stream_id
		   )
		   AND (tenant_id, node_id, stream_id) NOT IN (
		       SELECT tenant_id, node_id, stream_id
		       FROM periscope.stream_sessions_anomalous
		       GROUP BY tenant_id, node_id, stream_id
		   )
		LIMIT %d`, StaleCloseScanLimit),
		cutoff)
	if err != nil {
		return fmt.Errorf("stream stale-close query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	projectionVersionMS := time.Now().UnixMilli()
	batch, err := periscopeingestdb.PrepareStreamSessionAnomalous(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("stream_sessions_anomalous prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()

	rowsEmitted := 0
	for rows.Next() {
		var (
			tenantID, streamID, nodeID                       string
			observedFirstMS, observedLastMS, viewerSecondsMx int64
		)
		if err := rows.Scan(&tenantID, &streamID, &nodeID, &observedFirstMS, &observedLastMS, &viewerSecondsMx); err != nil {
			h.logger.WithError(err).Warn("stream stale-close scan failed; skipping row")
			continue
		}
		closedAtMS := projectionVersionMS
		notes := fmt.Sprintf("stale: no STREAM_END within %s", StaleCloseTimeout)
		estDuration := uint32(0)
		if viewerSecondsMx > 0 {
			estDuration = uint32(viewerSecondsMx)
		}
		tenantUUID, parseErr := uuid.Parse(tenantID)
		if parseErr != nil {
			return fmt.Errorf("stream_sessions_anomalous tenant_id %q: %w", tenantID, parseErr)
		}
		streamUUID, parseErr := uuid.Parse(streamID)
		if parseErr != nil {
			return fmt.Errorf("stream_sessions_anomalous stream_id %q: %w", streamID, parseErr)
		}
		if err := batch.Append(periscopeingestdb.StreamSessionAnomalousRow{
			TenantID: tenantUUID, NodeID: nodeID, StreamID: streamUUID,
			EstimatedDurationSeconds: estDuration,
			ObservedFirstAtMS:        observedFirstMS, ObservedLastAtMS: observedLastMS,
			ClosedAtMS: closedAtMS, ClosedReason: "stale", ProjectionVersionMS: projectionVersionMS,
			Notes: notes,
		}); err != nil {
			return fmt.Errorf("stream_sessions_anomalous append: %w", err)
		}
		rowsEmitted++
	}
	if rowsEmitted == 0 {
		return nil
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("stream_sessions_anomalous send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_sessions_anomalous", "inserted").Add(float64(rowsEmitted))
	}
	h.logger.WithField("count", rowsEmitted).Info("Stale-closed stream sessions")
	return nil
}

// staleMarkStreamStateOffline is the status backstop: it flips
// stream_state_current rows to offline when their event-driven refresh
// stopped. The primary offline edges (STREAM_END, the poller's vanish
// diff) can be delayed or dropped — Mist buffer drain, an expired
// Foghorn stream-context cache, a dead node — and without this loop a
// stuck "live" row stays live forever.
//
// One atomic server-side INSERT ... SELECT: the FINAL row supplies every
// non-nullable column, the new row gets updated_at = now() so it wins
// the ReplacingMergeTree merge and drops out of the next scan's WHERE.
// Known trade-off: an ingest/Kafka backlog longer than the timeout
// briefly flips genuinely-live streams offline; once the backlog drains,
// fresh lifecycle refreshes win again and status self-heals.
func (h *AnalyticsHandler) staleMarkStreamStateOffline(ctx context.Context) error {
	err := h.clickhouse.Exec(ctx, fmt.Sprintf(`
		INSERT INTO periscope.stream_state_current (
			tenant_id, stream_id, internal_name, node_id,
			status, buffer_state,
			current_viewers, total_inputs,
			uploaded_bytes, downloaded_bytes, viewer_seconds,
			has_issues, issues_description,
			track_count, quality_tier, primary_width, primary_height,
			primary_fps, primary_codec, primary_bitrate,
			packets_sent, packets_lost, packets_retransmitted,
			started_at, updated_at, cluster_id
		)
		SELECT
			tenant_id, stream_id, internal_name, node_id,
			'offline', 'EMPTY',
			0, 0,
			uploaded_bytes, downloaded_bytes, viewer_seconds,
			has_issues, issues_description,
			track_count, quality_tier, primary_width, primary_height,
			primary_fps, primary_codec, primary_bitrate,
			packets_sent, packets_lost, packets_retransmitted,
			started_at, now(), cluster_id
		FROM periscope.stream_state_current FINAL
		WHERE status NOT IN ('offline', 'stopped', 'gone')
		  AND stream_id != toUUIDOrZero('')
		  AND updated_at < now() - INTERVAL %d SECOND
		LIMIT %d`,
		int(StreamStateOfflineTimeout.Seconds()), StaleCloseScanLimit))
	if err != nil {
		return fmt.Errorf("stream_state_current offline backstop: %w", err)
	}
	return nil
}
