// Package queries contains database statements whose exact text is shared by
// runtime code and real-engine contract tests.
package queries

// ActiveViewerReservations returns absolute usage holds for sessions that do
// not yet have a disconnect fact. The serving cluster comes only from the
// trusted server-side connect event; origin attribution is deliberately not a
// billing fallback.
const ActiveViewerReservations = `
	WITH active AS (
		SELECT tenant_id, node_id, session_id, connected_at, last_updated
		FROM periscope.viewer_sessions_current FINAL
		WHERE connected_at IS NOT NULL
		  AND (disconnected_at IS NULL OR disconnected_at = toDateTime(0))
	), latest_qoe AS (
		SELECT tenant_id, node_id, session_id,
		       max(timestamp) AS last_sample_at,
		       argMax(connection_time, timestamp) AS connection_seconds,
		       argMax(bytes_downloaded, timestamp) AS bytes_downloaded
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
	SELECT toString(a.tenant_id), at.cluster_id,
	       sum(greatest(
	           toFloat64(dateDiff('second', assumeNotNull(a.connected_at), now())),
	           toFloat64(ifNull(q.connection_seconds, 0))
	       )) / 60.0 AS delivered_minutes,
	       sum(toFloat64(ifNull(q.bytes_downloaded, 0))) / pow(1024, 3) AS egress_gb
	FROM active a
	LEFT JOIN latest_qoe q USING (tenant_id, node_id, session_id)
	LEFT JOIN attribution at USING (tenant_id, node_id, session_id)
	WHERE greatest(a.last_updated, ifNull(q.last_sample_at, a.last_updated)) >= now() - INTERVAL 4 HOUR
	GROUP BY a.tenant_id, at.cluster_id
`
