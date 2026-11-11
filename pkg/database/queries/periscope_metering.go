// Package queries contains database statements whose exact text is shared by
// runtime code and real-engine contract tests.
package queries

import "strings"

// ActiveViewerReservations returns absolute usage holds for viewer session
// lifecycles that are open (ViewerLifecyclesCTE), held from the lifecycle's
// own connect. The serving cluster comes only from the trusted server-side
// connect event; origin attribution is deliberately not a billing fallback.
var ActiveViewerReservations = `
	WITH ` + ViewerLifecyclesCTE("connected_at IS NOT NULL") + `, active AS (
		SELECT tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms, lifecycle_connected_at AS connected_at, last_updated
		FROM viewer_lifecycles
		WHERE lifecycle_open
	), latest_qoe AS (
		SELECT tenant_id, node_id, session_id,
		       max(timestamp) AS last_sample_at,
		       argMax(connection_time, timestamp) AS connection_seconds,
		       argMax(bytes_downloaded, timestamp) AS bytes_downloaded
		FROM periscope.client_qoe_samples
		WHERE timestamp >= now() - INTERVAL 24 HOUR
		  AND session_id != ''
		GROUP BY tenant_id, node_id, session_id
	), projected_attribution AS (
		SELECT tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms,
		       groupUniqArrayMerge(serving_clusters) AS clusters
		FROM periscope.viewer_lifecycle_serving_attribution
		WHERE (tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms) IN
		      (SELECT tenant_id, stream_id, node_id, session_id, assumeNotNull(lifecycle_connected_at_ms) FROM active)
		GROUP BY tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms
	), retained_attribution AS (
		-- During backfill, merge all retained evidence for active keys so a
		-- partially backfilled lifecycle cannot hide conflicting clusters.
		SELECT tenant_id, stream_id, node_id, session_id, source_timestamp_ms AS lifecycle_connected_at_ms,
		       groupUniqArray(CAST(cluster_id AS String)) AS clusters
		FROM periscope.viewer_connection_events
		WHERE event_type = 'connect' AND cluster_id != ''
		  AND (tenant_id, stream_id, node_id, session_id, source_timestamp_ms) IN
		      (SELECT tenant_id, stream_id, node_id, session_id, assumeNotNull(lifecycle_connected_at_ms) FROM active)
		GROUP BY tenant_id, stream_id, node_id, session_id, source_timestamp_ms
	), attribution AS (
		SELECT tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms,
		       if(length(clusters) = 1, clusters[1], '') AS cluster_id
		FROM (
			SELECT tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms,
			       arrayDistinct(arrayFlatten(groupArray(clusters))) AS clusters
			FROM (SELECT * FROM projected_attribution UNION ALL SELECT * FROM retained_attribution)
			GROUP BY tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms
		)
	)
	SELECT toString(a.tenant_id), at.cluster_id,
	       sum(greatest(
	           toFloat64(dateDiff('second', assumeNotNull(a.connected_at), now())),
	           toFloat64(ifNull(q.connection_seconds, 0))
	       )) / 60.0 AS delivered_minutes,
	       sum(toFloat64(ifNull(q.bytes_downloaded, 0))) / pow(1024, 3) AS egress_gb,
       maxIf(toFloat64(dateDiff('second', assumeNotNull(a.connected_at), now())), at.cluster_id = '') AS unresolved_age_seconds
	FROM active a
	LEFT JOIN latest_qoe q USING (tenant_id, node_id, session_id)
	LEFT JOIN attribution at USING (tenant_id, stream_id, node_id, session_id, lifecycle_connected_at_ms)
	WHERE greatest(a.last_updated, ifNull(q.last_sample_at, a.last_updated)) >= now() - INTERVAL 4 HOUR
	GROUP BY a.tenant_id, at.cluster_id
`

// ActiveViewerReservationsDurable drops raw read-through only after the owner
// backfill is verified complete; the unreferenced retained CTE is not executed.
var ActiveViewerReservationsDurable = strings.Replace(ActiveViewerReservations,
	"SELECT * FROM projected_attribution UNION ALL SELECT * FROM retained_attribution",
	"SELECT * FROM projected_attribution", 1)
