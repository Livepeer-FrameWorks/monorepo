package database

import (
	"context"
	"fmt"
	"sort"
)

// Engine identifies the database protocol used by a capability probe.
type Engine string

const (
	EnginePostgres   Engine = "postgres"
	EngineClickHouse Engine = "clickhouse"
)

// Capability is a deliberately small executable requirement of a service
// binary. Probe must be read-only and return without consuming application
// data. These probes complement the migration ledger; they are not another
// desired-schema manifest.
type Capability struct {
	Name   string
	Engine Engine
	Probe  string
}

// CapabilityError identifies the binary requirement that the live engine did
// not satisfy, so startup and operator diagnostics can name the exact repair.
type CapabilityError struct {
	Service    string
	Capability string
	Engine     Engine
	Err        error
}

func (e *CapabilityError) Error() string {
	return fmt.Sprintf("%s requires %s capability %q: %v; apply the release migrations before starting this binary", e.Service, e.Engine, e.Capability, e.Err)
}

func (e *CapabilityError) Unwrap() error { return e.Err }

var capabilityCatalog = map[string][]Capability{
	"commodore": {
		{Name: "media placement revisions", Engine: EnginePostgres, Probe: "SELECT tenant_id, scope_kind, scope_id, revision, policy_payload, active_revision, active_policy_payload FROM commodore.media_placement_policies LIMIT 0"},
		{Name: "media placement apply receipts", Engine: EnginePostgres, Probe: "SELECT tenant_id, idempotency_key, request_sha256, revision, parent_revision, rollout_status FROM commodore.media_placement_changes LIMIT 0"},
		{Name: "wallet authentication", Engine: EnginePostgres, Probe: "SELECT wallet_address, chain_id, message_hash, expires_at, consumed_at FROM commodore.wallet_auth_challenges LIMIT 0"},
		{Name: "stream cleanup outbox", Engine: EnginePostgres, Probe: "SELECT tenant_id, stream_id, status, next_attempt_at, lease_token FROM commodore.stream_cleanup_outbox LIMIT 0"},
		{Name: "artifact creation acknowledgement leasing", Engine: EnginePostgres, Probe: "SELECT tenant_id, kind, artifact_hash, command_ack_pending, command_ack_next_at, command_ack_leased_until, command_ack_lease_token FROM commodore.artifact_creation_intents LIMIT 0"},
	},
	"foghorn": {
		{Name: "ingest admission fencing", Engine: EnginePostgres, Probe: "SELECT id, tenant_id, node_id, stream_internal_name, connector_pid, projection_state, source_revision FROM foghorn.ingest_sessions LIMIT 0"},
		{Name: "restream activation-attempt fencing", Engine: EnginePostgres, Probe: "SELECT source_generation, target_revision, activation_attempt, mist_push_ids FROM foghorn.admission_push_target_revisions LIMIT 0"},
		{Name: "cell storage identity", Engine: EnginePostgres, Probe: "SELECT backend_id, bucket, endpoint, region, prefix FROM foghorn.cell_storage_identity LIMIT 0"},
		{Name: "artifact placement stable identity", Engine: EnginePostgres, Probe: "SELECT a.artifact_hash, a.tenant_id, n.node_id, n.last_emitted_version FROM foghorn.artifacts a LEFT JOIN foghorn.artifact_nodes n ON n.artifact_hash = a.artifact_hash LIMIT 0"},
		{Name: "artifact reconciliation cursors", Engine: EnginePostgres, Probe: "SELECT id, last_hash FROM foghorn.active_object_key_backfill_cursor LIMIT 0"},
	},
	"lookout": {
		{Name: "verified cluster owner scopes", Engine: EnginePostgres, Probe: "SELECT cluster_id, scope, tenant_id, verified, source_updated_at FROM lookout.cluster_scopes LIMIT 0"},
		{Name: "incident scope and lifecycle", Engine: EnginePostgres, Probe: "SELECT id, scope, tenant_id, group_key, status, resolution FROM lookout.incidents LIMIT 0"},
		{Name: "notification outbox leasing and retention", Engine: EnginePostgres, Probe: "SELECT channel, attempts, next_attempt_at, lease_token, last_error, delivered_at, failed_at FROM lookout.notification_outbox LIMIT 0"},
		{Name: "operator activity outbox", Engine: EnginePostgres, Probe: "SELECT source_event_id, event_type, tenant_id, channel, attempts, next_attempt_at, lease_token, delivered_at, failed_at FROM lookout.operator_activity_outbox LIMIT 0"},
	},
	"navigator": {
		{Name: "tenant edge apply state", Engine: EnginePostgres, Probe: "SELECT tenant_id, cluster_id, node_id, bundle_id, state, last_seed_version, last_delivery_sequence FROM navigator.tenant_edge_apply_state LIMIT 0"},
		{Name: "tenant alias retirement queue", Engine: EnginePostgres, Probe: "SELECT tenant_id, subdomain, requested_at, attempts, last_error FROM navigator.tenant_alias_retirements LIMIT 0"},
	},
	"periscope-ingest": {
		{Name: "distributed ledger-worker locking", Engine: EnginePostgres, Probe: "SELECT pg_try_advisory_xact_lock(hashtext('periscope-ingest:capability'))"},
		{Name: "API delivery identity", Engine: EngineClickHouse, Probe: "SELECT tenant_id, source_event_id, ingested_at_ms FROM periscope.api_requests LIMIT 0"},
		{Name: "viewer attribution", Engine: EngineClickHouse, Probe: "SELECT tenant_id, cluster_id, node_id, session_id FROM periscope.viewer_connection_events LIMIT 0"},
	},
	"periscope-query": {
		{Name: "delegated-token replay fencing", Engine: EnginePostgres, Probe: "SELECT jti, expires_at FROM periscope.delegated_jwt_replays LIMIT 0"},
		{Name: "final viewer facts", Engine: EngineClickHouse, Probe: "SELECT tenant_id, cluster_id, session_id, source_started_at_ms, source_ended_at_ms FROM periscope.viewer_sessions_final_v LIMIT 0"},
		{Name: "dimensioned API usage", Engine: EngineClickHouse, Probe: "SELECT tenant_id, auth_type, operation_type, service, llm_model, llm_provider, requests FROM periscope.api_usage_5m_v LIMIT 0"},
	},
	"periscope-metering": {
		{Name: "metering source cursor", Engine: EnginePostgres, Probe: "SELECT source_id, tenant_id, last_processed_at FROM periscope.billing_cursors LIMIT 0"},
		{Name: "reservation idempotency", Engine: EnginePostgres, Probe: "SELECT source_id, tenant_id, cluster_id, last_sequence FROM periscope.metering_reservation_keys LIMIT 0"},
		{Name: "final viewer billing facts", Engine: EngineClickHouse, Probe: "SELECT tenant_id, cluster_id, session_id, source_started_at_ms, source_ended_at_ms FROM periscope.viewer_sessions_final_v LIMIT 0"},
		{Name: "dimensioned API billing facts", Engine: EngineClickHouse, Probe: "SELECT tenant_id, auth_type, operation_type, service, llm_model, llm_provider, requests FROM periscope.api_usage_5m_v LIMIT 0"},
	},
	"purser": {
		{Name: "dimensioned usage", Engine: EnginePostgres, Probe: "SELECT tenant_id, source_id, usage_type, usage_value, dimensions, dimension_key FROM purser.usage_records LIMIT 0"},
		{Name: "provider webhook leasing", Engine: EnginePostgres, Probe: "SELECT provider, event_key, status, claimed_at, lease_token FROM purser.provider_webhook_inbox LIMIT 0"},
	},
	"quartermaster": {
		{Name: "media capacity consent", Engine: EnginePostgres, Probe: "SELECT media_consent_revision, media_allow_ingest, media_allow_serve, media_allow_external_source FROM quartermaster.infrastructure_clusters LIMIT 0"},
		{Name: "media capacity consent receipts", Engine: EnginePostgres, Probe: "SELECT tenant_id, cluster_record_id, cluster_id, revision, request_sha256, review_digest FROM quartermaster.media_capacity_consent_changes LIMIT 0"},
		{Name: "service cluster assignments", Engine: EnginePostgres, Probe: "SELECT service_instance_id, cluster_id, source, is_active FROM quartermaster.service_cluster_assignments LIMIT 0"},
		{Name: "tenant provisioning idempotency", Engine: EnginePostgres, Probe: "SELECT alias, tenant_id, updated_at FROM quartermaster.bootstrap_tenant_aliases LIMIT 0"},
	},
	"skipper": {
		{Name: "knowledge vectors", Engine: EnginePostgres, Probe: "SELECT tenant_id, source_url, chunk_text, embedding FROM skipper.skipper_knowledge LIMIT 0"},
		{Name: "usage publication", Engine: EnginePostgres, Probe: "SELECT tenant_id, event_type, created_at, claimed_at, published_at FROM skipper.skipper_usage LIMIT 0"},
	},
}

// CapabilitiesFor returns a copy of the executable requirements declared by a
// deploy binary for one engine.
func CapabilitiesFor(service string, engine Engine) []Capability {
	var out []Capability
	for _, capability := range capabilityCatalog[service] {
		if capability.Engine == engine {
			out = append(out, capability)
		}
	}
	return out
}

// CapabilityServices returns the stable set of binaries with executable
// database requirements. It is used by completeness tests and operator tools.
func CapabilityServices() []string {
	services := make([]string, 0, len(capabilityCatalog))
	for service := range capabilityCatalog {
		services = append(services, service)
	}
	sort.Strings(services)
	return services
}

// VerifyCapabilities executes every read-only probe through the same runtime
// database handle the binary will use.
func VerifyCapabilities(ctx context.Context, service string, engine Engine, query func(context.Context, string) error) error {
	for _, capability := range CapabilitiesFor(service, engine) {
		if err := query(ctx, capability.Probe); err != nil {
			capabilityErr := &CapabilityError{Service: service, Capability: capability.Name, Engine: engine, Err: err}
			ObserveDatabaseError(service, engine, capabilityErr, false)
			return capabilityErr
		}
	}
	return nil
}
