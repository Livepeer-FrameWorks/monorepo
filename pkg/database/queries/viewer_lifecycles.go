package queries

// ViewerLifecyclesCTE returns two common table expressions for a WITH clause:
// viewer_current, the viewer_sessions_current rows matching where (a
// predicate over its columns, which may hold placeholders bound once), and
// viewer_lifecycles, one row per session describing its current lifecycle.
//
// Mist reuses a viewer session id when a viewer resumes a playback after Mist
// ended its session, so a session's current lifecycle starts at its latest
// connect (lifecycle_connected_at). The lifecycle is open while the session's
// last disconnect and its latest final (USER_END) both precede that connect.
// lifecycle_started_at and lifecycle_ended_at bound the activity the overlay
// attributes to the row: the open lifecycle from its connect until now, else
// the session from its first connect to its last disconnect or final.
//
// Stale-close, the prepaid reservation overlay and the live usage queries all
// read this definition, so they agree on which lifecycle is open.
func ViewerLifecyclesCTE(where string) string {
	return `viewer_current AS (
		SELECT * FROM periscope.viewer_sessions_current FINAL
		WHERE ` + where + `
	), viewer_current_finals AS (
		SELECT tenant_id, node_id, session_id, max(source_ended_at_ms) AS last_ended_at_ms
		FROM periscope.viewer_sessions_final
		WHERE projection_version_ms > toUnixTimestamp(now() - INTERVAL 30 DAY) * 1000
		  AND (tenant_id, node_id, session_id) IN (SELECT tenant_id, node_id, session_id FROM viewer_current)
		GROUP BY tenant_id, node_id, session_id
	), viewer_lifecycles AS (
		SELECT tenant_id, stream_id, node_id, session_id, cluster_id, country_code, city,
		       connected_at, disconnected_at, session_duration, last_updated,
		       ifNull(c.last_connected_at, c.connected_at) AS lifecycle_connected_at,
		       toInt64(toUnixTimestamp(ifNull(c.last_connected_at, c.connected_at))) * 1000 AS lifecycle_connected_at_ms,
		       (lifecycle_connected_at IS NOT NULL
		           AND ifNull(c.disconnected_at, toDateTime(0)) < lifecycle_connected_at
		           AND ifNull(f.last_ended_at_ms, 0) < lifecycle_connected_at_ms) AS lifecycle_open,
		       if(lifecycle_open, lifecycle_connected_at, ifNull(c.connected_at, c.disconnected_at)) AS lifecycle_started_at,
		       if(lifecycle_open, now(),
		          greatest(ifNull(c.disconnected_at, toDateTime(0)), toDateTime(intDiv(ifNull(f.last_ended_at_ms, 0), 1000)))) AS lifecycle_ended_at
		FROM viewer_current AS c
		LEFT JOIN viewer_current_finals AS f USING (tenant_id, node_id, session_id)
	)`
}
