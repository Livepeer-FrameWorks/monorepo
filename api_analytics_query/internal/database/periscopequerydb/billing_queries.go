package periscopequerydb

import dbqueries "github.com/Livepeer-FrameWorks/monorepo/pkg/database/queries"

var ActiveViewerReservations = statement("billing.active_viewer_reservations", dbqueries.ActiveViewerReservations)

var ActiveTenants = statement("billing.active_tenants", `
	SELECT DISTINCT tenant_id FROM (
		SELECT toString(tenant_id) AS tenant_id FROM periscope.viewer_sessions_final
		WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
		UNION ALL
		SELECT toString(tenant_id) AS tenant_id FROM periscope.processing_segments_final
		WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
		UNION ALL
		SELECT toString(tenant_id) AS tenant_id FROM periscope.stream_sessions_final
		WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
		UNION ALL
		SELECT toString(tenant_id) AS tenant_id
		FROM (
			SELECT tenant_id, cluster_id, storage_scope,
				storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
				argMax(total_bytes, tuple(timestamp, ingested_at_ms)) AS total_bytes
			FROM periscope.storage_snapshots
			GROUP BY tenant_id, cluster_id, storage_scope,
				storage_provider_tenant_id, storage_provider_cluster_id, storage_backend
		)
		WHERE total_bytes > 0
		UNION ALL
		SELECT toString(tenant_id) AS tenant_id FROM periscope.stream_runtime_5m
		WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
		UNION ALL
		SELECT toString(tenant_id) AS tenant_id FROM periscope.api_usage_5m
		WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
		UNION ALL
		SELECT JSONExtractString(natural_key_json, 'tenant_id') AS tenant_id
		FROM periscope.projection_divergences
		WHERE observed_at_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
	)
	WHERE tenant_id IS NOT NULL
	  AND tenant_id != ''
	  AND tenant_id NOT IN (?, ?, ?)
	ORDER BY tenant_id
`)

var PeakBandwidth = statement("billing.peak_bandwidth", `
	SELECT COALESCE(max(avg_bw_out) / (1024*1024), 0) AS peak_bandwidth_mbps
	FROM periscope.client_qoe_5m
	WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m < ?
`)

var MonthlyUniqueUsers = statement("billing.monthly_unique_users", `
	WITH sessions AS (
		SELECT tenant_id, node_id, session_id,
			argMax(host, projection_version_ms) AS host,
			argMax(source_ended_at_ms, projection_version_ms) AS source_ended_at_ms,
			argMax(closed_reason, projection_version_ms) AS closed_reason
		FROM periscope.viewer_sessions_final
		WHERE tenant_id = ? AND projection_version_ms < ?
		GROUP BY tenant_id, node_id, session_id
	)
	SELECT COALESCE(uniqCombined(if(host != '', host, concat(toString(node_id), '|', session_id))), 0) AS unique_users
	FROM sessions
	WHERE tenant_id = ? AND closed_reason = 'final'
	  AND source_ended_at_ms >= ? AND source_ended_at_ms < ?
`)

var APIUsageByDimension = statement("billing.api_usage_by_dimension", `
	SELECT auth_type, operation_type, operation_name, service, llm_model, llm_provider,
		COALESCE(sum(requests), 0) AS total_requests,
		COALESCE(sum(errors), 0) AS total_errors,
		COALESCE(sum(duration_ms), 0) AS total_duration_ms,
		COALESCE(sum(complexity), 0) AS total_complexity,
		COALESCE(sum(llm_input_tokens), 0) AS total_llm_input_tokens,
		COALESCE(sum(llm_output_tokens), 0) AS total_llm_output_tokens,
		COALESCE(uniqCombinedMerge(unique_users_state), 0) AS unique_users,
		COALESCE(uniqCombinedMerge(unique_tokens_state), 0) AS unique_tokens
	FROM periscope.api_usage_5m_v
	WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
	GROUP BY auth_type, operation_type, operation_name, service, llm_model, llm_provider
`)

var ClusterStreamRuntime = statement("billing.cluster_stream_runtime", `
	SELECT cluster_id,
		COALESCE(toInt32(max(peak_viewers)), 0) AS max_viewers,
		COALESCE(toInt32(uniqCombined(stream_id)), 0) AS total_streams,
		COALESCE(sum(active_seconds) / 3600.0, 0) AS stream_hours
	FROM periscope.stream_runtime_5m_v
	WHERE tenant_id = ? AND window_start >= ? AND window_start < ? AND cluster_id != ''
	GROUP BY cluster_id
`)

var ClusterProcessingSeconds = statement("billing.cluster_processing_seconds", `
	WITH window_candidates AS (
		SELECT tenant_id, node_id, stream_id, source_event_id,
			min(projection_version_ms) AS proj_first_in_window,
			argMax(process_type, projection_version_ms) AS process_type,
			argMax(output_codec, projection_version_ms) AS output_codec,
			argMax(track_type, projection_version_ms) AS track_type,
			argMax(rendition_count, projection_version_ms) AS rendition_count,
			argMax(renditions_json, projection_version_ms) AS renditions_json,
			argMax(cluster_id, projection_version_ms) AS cluster_id,
			argMax(media_seconds, projection_version_ms) AS media_seconds
		FROM periscope.processing_segments_final
		WHERE tenant_id = ? AND projection_version_ms >= ? AND projection_version_ms < ?
		GROUP BY tenant_id, node_id, stream_id, source_event_id
	)
	SELECT c.cluster_id, c.process_type, c.output_codec, c.track_type,
		c.rendition_count, c.renditions_json, sum(c.media_seconds) AS media_seconds
	FROM window_candidates c
	LEFT ANTI JOIN (
		SELECT DISTINCT tenant_id, node_id, stream_id, source_event_id
		FROM periscope.processing_segments_final
		WHERE tenant_id = ? AND projection_version_ms < ?
		  AND (tenant_id, node_id, stream_id, source_event_id) IN (
			SELECT tenant_id, node_id, stream_id, source_event_id FROM window_candidates
		  )
	) prior USING (tenant_id, node_id, stream_id, source_event_id)
	GROUP BY c.cluster_id, c.process_type, c.output_codec, c.track_type, c.rendition_count, c.renditions_json
`)

var ClusterStorageProviderUsage = statement("billing.cluster_storage_provider_usage", `
	WITH first_projections AS (
		SELECT cluster_id, storage_provider_tenant_id, storage_provider_cluster_id,
			storage_backend, storage_scope, window_start,
			min(projection_version_ms) AS billable_at_ms,
			argMax(gb_seconds, projection_version_ms) AS gb_seconds
		FROM periscope.storage_gb_seconds_5m
		WHERE tenant_id = ? AND projection_version_ms < ?
		GROUP BY cluster_id, storage_provider_tenant_id, storage_provider_cluster_id,
			storage_backend, storage_scope, window_start
		HAVING billable_at_ms >= ? AND billable_at_ms < ?
	)
	SELECT cluster_id, storage_provider_tenant_id, storage_provider_cluster_id,
		storage_backend, storage_scope, sum(gb_seconds) AS total_gb_seconds
	FROM first_projections
	GROUP BY cluster_id, storage_provider_tenant_id, storage_provider_cluster_id,
		storage_backend, storage_scope
	HAVING total_gb_seconds != 0
`)

var UsageAdjustments = statement("billing.usage_adjustments", `
	SELECT observed_at_ms, table_name, meter, field,
		natural_key_json, prior_value_json, new_value_json, source_event_id
	FROM periscope.projection_divergences
	WHERE observed_at_ms >= ? AND observed_at_ms < ?
	  AND table_name IN ('storage_gb_seconds_5m', 'viewer_sessions_final', 'stream_sessions_final', 'processing_segments_final')
	  AND JSONExtractString(natural_key_json, 'tenant_id') = ?
`)

var FirstViewerSessionProjection = statement("billing.first_viewer_session_projection", `
	SELECT if(count() = 0, 0, min(projection_version_ms))
	FROM periscope.viewer_sessions_final
	WHERE tenant_id = toUUID(?) AND node_id = ? AND session_id = ?
`)

var FirstProcessingSegmentProjection = statement("billing.first_processing_segment_projection", `
	SELECT if(count() = 0, 0, min(projection_version_ms))
	FROM periscope.processing_segments_final
	WHERE tenant_id = toUUID(?) AND node_id = ? AND stream_id = toUUID(?) AND source_event_id = ?
`)

var FirstStreamSessionProjection = statement("billing.first_stream_session_projection", `
	SELECT if(count() = 0, 0, min(projection_version_ms))
	FROM periscope.stream_sessions_final
	WHERE tenant_id = toUUID(?) AND node_id = ? AND stream_id = toUUID(?) AND source_event_id = ?
`)

var FirstStorageProjection = statement("billing.first_storage_projection", `
	SELECT if(count() = 0, 0, min(projection_version_ms))
	FROM periscope.storage_gb_seconds_5m
	WHERE tenant_id = toUUID(?) AND cluster_id = ? AND storage_scope = ?
	  AND storage_provider_tenant_id = ? AND storage_provider_cluster_id = ?
	  AND storage_backend = ? AND window_start = parseDateTimeBestEffort(?)
`)

var TenantViewerMetrics = statement("billing.tenant_viewer_metrics", `
	WITH window_candidates AS (
		SELECT tenant_id, node_id, session_id,
			min(projection_version_ms) AS proj_first_in_window,
			argMax(cluster_id, projection_version_ms) AS cluster_id,
			argMax(duration_seconds, projection_version_ms) AS duration_seconds,
			argMax(uploaded_bytes, projection_version_ms) AS uploaded_bytes,
			argMax(downloaded_bytes, projection_version_ms) AS downloaded_bytes,
			argMax(closed_reason, projection_version_ms) AS closed_reason
		FROM periscope.viewer_sessions_final
		WHERE tenant_id = ? AND projection_version_ms >= ? AND projection_version_ms < ?
		GROUP BY tenant_id, node_id, session_id
	)
	SELECT c.cluster_id, '' AS origin_cluster_id,
		sum(c.uploaded_bytes) / pow(1024, 3) AS ingress_gb,
		sum(c.downloaded_bytes) / pow(1024, 3) AS egress_gb,
		sum(c.duration_seconds) / 3600.0 AS viewer_hours,
		toInt64(uniqCombined(c.session_id)) AS unique_viewers
	FROM window_candidates c
	LEFT ANTI JOIN (
		SELECT DISTINCT tenant_id, node_id, session_id
		FROM periscope.viewer_sessions_final
		WHERE tenant_id = ? AND projection_version_ms < ?
		  AND (tenant_id, node_id, session_id) IN (
			SELECT tenant_id, node_id, session_id FROM window_candidates
		  )
	) prior USING (tenant_id, node_id, session_id)
	WHERE c.closed_reason = 'final'
	GROUP BY c.cluster_id
`)

var EarliestCanonicalBillingFact = statement("billing.earliest_canonical_fact", `
	SELECT min(first_ms)
	FROM (
		SELECT toInt64(min(projection_version_ms)) AS first_ms FROM periscope.viewer_sessions_final WHERE tenant_id = ?
		UNION ALL
		SELECT toInt64(min(projection_version_ms)) AS first_ms FROM periscope.stream_sessions_final WHERE tenant_id = ?
		UNION ALL
		SELECT toInt64(min(projection_version_ms)) AS first_ms FROM periscope.processing_segments_final WHERE tenant_id = ?
		UNION ALL
		SELECT toInt64(min(projection_version_ms)) AS first_ms FROM periscope.storage_gb_seconds_5m WHERE tenant_id = ?
		UNION ALL
		SELECT toInt64(toUnixTimestamp(min(window_start)) * 1000) AS first_ms FROM periscope.api_usage_5m_v WHERE tenant_id = ?
	)
`)
