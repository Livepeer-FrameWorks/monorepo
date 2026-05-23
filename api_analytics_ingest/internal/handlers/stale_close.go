package handlers

import (
	"context"
	"fmt"
	"time"
)

// Stale-close worker. Lives here (not api_sidecar) because the live-state
// table viewer_sessions_current is populated by this ingest path from
// USER_NEW/USER_END Kafka events — Helmsman does not keep an in-memory
// session map. Co-locating the worker with the ingest writer keeps a
// single ClickHouse client authoritative for the table.
//
// The worker is conservative: it only marks a session/stream anomalous
// when (a) there is a live-state row, (b) the row has been silent past
// the stale_close_timeout, and (c) no viewer_sessions_final row exists
// for the same natural key. Writes to viewer_sessions_anomalous /
// stream_sessions_anomalous are append-only — re-running the worker on
// the same stale session adds another anomaly row with a fresher
// projection_version_ms, which the operational dashboard can dedupe via
// argMax.

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
)

// startStaleCloseLoops launches the viewer and stream stale-close workers.
// Called from LedgerScheduler.Start.
func (s *LedgerScheduler) startStaleCloseLoops(ctx context.Context) {
	go s.runStaleCloseLoop(ctx, "viewer_sessions_anomalous", s.h.staleCloseViewerSessions)
	go s.runStaleCloseLoop(ctx, "stream_sessions_anomalous", s.h.staleCloseStreamSessions)
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
			if err := run(ctx); err != nil {
				s.logger.WithError(err).WithField("worker", name).Warn("Stale-close pass failed")
			}
		}
	}
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
		SELECT
			tenant_id,
			stream_id,
			session_id,
			node_id,
			toUnixTimestamp(connected_at) * 1000 AS observed_first_at_ms,
			toUnixTimestamp(last_updated) * 1000 AS observed_last_at_ms,
			session_duration
		FROM periscope.viewer_sessions_current FINAL
		WHERE last_updated < ?
		  AND (disconnected_at IS NULL OR disconnected_at = toDateTime(0))
		  AND (tenant_id, node_id, session_id) NOT IN (
		      SELECT tenant_id, node_id, session_id
		      FROM periscope.viewer_sessions_final
		      WHERE projection_version_ms > toUnixTimestamp(now() - INTERVAL 30 DAY) * 1000
		      GROUP BY tenant_id, node_id, session_id
		  )
		LIMIT %d`, StaleCloseScanLimit),
		cutoff)
	if err != nil {
		return fmt.Errorf("viewer stale-close query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	projectionVersionMS := time.Now().UnixMilli()
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.viewer_sessions_anomalous (
			tenant_id, node_id, session_id,
			cluster_id, stream_id, stream_name,
			estimated_duration_seconds,
			observed_first_at_ms, observed_last_at_ms,
			closed_at_ms, closed_reason, projection_version_ms,
			notes
		)`)
	if err != nil {
		return fmt.Errorf("viewer_sessions_anomalous prepare: %w", err)
	}

	rowsEmitted := 0
	for rows.Next() {
		var (
			tenantID, streamID, sessionID, nodeID string
			observedFirstMS, observedLastMS       int64
			sessionDuration                       uint32
		)
		if err := rows.Scan(&tenantID, &streamID, &sessionID, &nodeID, &observedFirstMS, &observedLastMS, &sessionDuration); err != nil {
			h.logger.WithError(err).Warn("viewer stale-close scan failed; skipping row")
			continue
		}
		closedAtMS := projectionVersionMS
		notes := fmt.Sprintf("stale: no USER_END within %s", StaleCloseTimeout)
		if err := batch.Append(
			tenantID, nodeID, sessionID,
			"", streamID, "",
			sessionDuration,
			observedFirstMS, observedLastMS,
			closedAtMS, "stale", projectionVersionMS,
			notes,
		); err != nil {
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
			toUnixTimestamp(ifNull(started_at, updated_at)) * 1000 AS observed_first_at_ms,
			toUnixTimestamp(updated_at) * 1000 AS observed_last_at_ms,
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
		LIMIT %d`, StaleCloseScanLimit),
		cutoff)
	if err != nil {
		return fmt.Errorf("stream stale-close query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	projectionVersionMS := time.Now().UnixMilli()
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.stream_sessions_anomalous (
			tenant_id, node_id, stream_id,
			cluster_id, stream_name,
			estimated_duration_seconds,
			observed_first_at_ms, observed_last_at_ms,
			closed_at_ms, closed_reason, projection_version_ms,
			notes
		)`)
	if err != nil {
		return fmt.Errorf("stream_sessions_anomalous prepare: %w", err)
	}

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
		if err := batch.Append(
			tenantID, nodeID, streamID,
			"", "",
			estDuration,
			observedFirstMS, observedLastMS,
			closedAtMS, "stale", projectionVersionMS,
			notes,
		); err != nil {
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
