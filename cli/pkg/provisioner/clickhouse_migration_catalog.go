package provisioner

// ClickHouse data-migration catalog.
//
// This is the EXPLICIT, hand-curated inventory of every periscope object,
// classified for the cross-host data migration. It is deliberately NOT inferred
// from engine/name at run time — `TestClickHouseMigrationCatalogCoverage` asserts
// it matches `pkg/database/sql/clickhouse/periscope.sql` exactly (fails the build
// when an object is added or removed without updating the catalog), so a new table
// can never be silently mishandled by the migrator.
//
// Division of responsibility:
//   - The catalog owns the *known-object set* and the *MV handling class*. Only
//     RefreshableMVs need runtime handling (SYSTEM STOP/START VIEW); the other
//     classes exist so the coverage assertion accounts for every object.
//   - The per-table COPY METHOD (partition-replace vs full-table replace) is read
//     from authoritative `system.tables.partition_key` at run time — the live
//     schema, never a name guess.

// ClickHouseMigrationCatalog enumerates periscope's migratable objects by class.
type ClickHouseMigrationCatalog struct {
	// Tables: every data-bearing table. Backfilled then partition-synced (or
	// full-replaced when system.tables reports no partition key). Includes raw
	// event tables, insert-trigger MV target tables, refreshable `*_store`
	// targets, `*_current` state tables, and ledger projection tables.
	Tables []string
	// InsertTriggerMVs: classic `… TO <target> AS SELECT` views that fire on every
	// INSERT into their source. The migrator does NOT toggle these: data lands via
	// staging + REPLACE PARTITION / EXCHANGE (which never fire an MV) and each
	// target table is copied directly. Listed only so the coverage assertion
	// accounts for every object in periscope.sql.
	InsertTriggerMVs []string
	// RefreshableMVs: `REFRESH EVERY … APPEND TO <store>` views that rebuild on a
	// schedule. `SYSTEM STOP VIEW` for the duration of the migration (APPEND mode
	// double-appends if it fires mid-copy), `SYSTEM START VIEW` at cutover.
	RefreshableMVs []string
	// Views: plain query-time views — no data to migrate. Listed only so the
	// coverage assertion accounts for every object in periscope.sql.
	Views []string
}

// PeriscopeMigrationCatalog is the catalog for the `periscope` database. Keep it
// in lockstep with periscope.sql — the coverage test enforces it.
var PeriscopeMigrationCatalog = ClickHouseMigrationCatalog{
	Tables: []string{
		"api_events", "api_requests", "api_usage_5m", "api_usage_daily_store",
		"api_usage_hourly_store", "artifact_events", "artifact_state_current",
		"client_qoe_5m", "client_qoe_samples", "client_qoe_session_deltas",
		"federation_events", "federation_hourly", "ingest_errors",
		"ledger_rebuild_cursors", "node_metrics_1h", "node_metrics_samples",
		"node_performance_5m", "node_state_current", "orchestrator_ai_hourly",
		"orchestrator_ai_outcomes", "orchestrator_discovery_1h",
		"orchestrator_discovery_5m", "orchestrator_discovery_samples",
		"orchestrator_instance_state_current", "orchestrator_state_current",
		"orchestrator_transcode_hourly", "orchestrator_transcode_outcomes",
		"orchestrator_vantage_current", "player_boot_samples", "processing_5m",
		"processing_daily_store", "processing_events", "processing_hourly_store",
		"processing_segments_final", "projection_divergences", "quality_tier_daily",
		"raw_mist_triggers", "rebuffering_events", "routing_cluster_hourly",
		"routing_decisions", "storage_events", "storage_gb_seconds_5m",
		"storage_snapshots", "storage_usage_daily_store", "storage_usage_hourly_store",
		"stream_analytics_daily_store", "stream_connection_hourly_store",
		"stream_event_log", "stream_health_5m", "stream_health_samples",
		"stream_runtime_5m", "stream_runtime_daily_store", "stream_runtime_hourly_store",
		"stream_sessions_anomalous", "stream_sessions_final", "stream_state_current",
		"stream_viewer_5m", "tenant_acquisition_events", "tenant_analytics_daily_store",
		"tenant_usage_5m_store", "tenant_usage_daily_store", "tenant_usage_hourly_store",
		"tenant_viewer_daily_store", "track_list_events", "viewer_city_daily_store",
		"viewer_city_hourly_store", "viewer_connection_events", "viewer_geo_daily_store",
		"viewer_geo_hourly_store", "viewer_hours_hourly_store", "viewer_sessions_anomalous",
		"viewer_sessions_current", "viewer_sessions_final", "viewer_usage_5m",
		"vod_retention_buckets",
	},
	InsertTriggerMVs: []string{
		"client_qoe_5m_mv", "federation_hourly_mv", "node_metrics_1h_mv",
		"node_performance_5m_mv", "orchestrator_ai_hourly_mv",
		"orchestrator_discovery_1h_mv", "orchestrator_discovery_5m_mv",
		"orchestrator_transcode_hourly_mv", "quality_tier_daily_mv",
		"rebuffering_events_mv", "routing_cluster_hourly_mv", "stream_health_5m_mv",
		"stream_viewer_5m_mv", "viewer_sessions_connect_mv", "viewer_sessions_disconnect_mv",
	},
	RefreshableMVs: []string{
		"api_usage_daily_mv", "api_usage_hourly_mv", "processing_daily_mv",
		"processing_hourly_mv", "storage_usage_daily_mv", "storage_usage_hourly_mv",
		"stream_analytics_daily_mv", "stream_connection_hourly_mv",
		"stream_runtime_daily_mv", "stream_runtime_hourly_mv", "tenant_analytics_daily_mv",
		"tenant_usage_5m_mv", "tenant_usage_daily_mv", "tenant_usage_hourly_mv",
		"tenant_viewer_daily_mv", "viewer_city_daily_mv", "viewer_city_hourly_mv",
		"viewer_geo_daily_mv", "viewer_geo_hourly_mv", "viewer_hours_hourly_mv",
	},
	Views: []string{
		"api_usage_5m_v", "api_usage_daily", "api_usage_hourly", "processing_5m_v",
		"processing_daily", "processing_hourly", "processing_segments_final_v",
		"storage_gb_seconds_5m_v", "storage_usage_daily", "storage_usage_hourly",
		"stream_analytics_daily", "stream_connection_hourly", "stream_runtime_5m_v",
		"stream_runtime_daily", "stream_runtime_hourly", "stream_sessions_final_v",
		"tenant_analytics_daily", "tenant_usage_5m", "tenant_usage_daily",
		"tenant_usage_hourly", "tenant_viewer_daily", "viewer_city_daily",
		"viewer_city_hourly", "viewer_geo_daily", "viewer_geo_hourly",
		"viewer_hours_hourly", "viewer_sessions_final_v", "viewer_usage_5m_v",
	},
}
