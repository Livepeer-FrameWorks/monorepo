-- ============================================================================
-- OPERATOR-ANALYTICS READ-ONLY ROLE - quartermaster grants
-- ============================================================================
-- frameworks_analytics_ro is the sanctioned exception to the
-- no-cross-service-DB-reads rule: ClickHouse reaches these tables through
-- the *_pg named collections (dictionaries + postgresql() table functions)
-- to power the operator tenant-activity views in Metabase.
--
-- Grants are a fail-closed allowlist: tables and columns holding secrets
-- (connection strings, tokens, TLS bundles) are never granted. Extend the
-- allowlist here and re-run `frameworks cluster seed`. The role's password
-- is set from manifest env (ANALYTICS_RO_PASSWORD) by the seed command,
-- never from this file.
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'frameworks_analytics_ro') THEN
        CREATE ROLE frameworks_analytics_ro LOGIN;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA quartermaster TO frameworks_analytics_ro;

GRANT SELECT ON
    quartermaster.tenant_attribution,
    quartermaster.bootstrap_tenant_aliases,
    quartermaster.referral_codes,
    quartermaster.infrastructure_clusters,
    quartermaster.infrastructure_nodes,
    quartermaster.services,
    quartermaster.cluster_services,
    quartermaster.service_instances,
    quartermaster.tenant_cluster_assignments,
    quartermaster.edge_releases,
    quartermaster.cluster_release_targets
TO frameworks_analytics_ro;

-- database_url / kafka_* are connectivity config (database_url can embed
-- credentials); everything else on tenants is fair game.
GRANT SELECT (
    id, name, subdomain, custom_domain,
    logo_url, primary_color, secondary_color,
    deployment_tier, deployment_model, primary_cluster_id, official_cluster_id,
    rate_limit_per_minute, rate_limit_burst, max_owned_clusters,
    is_provider, is_active, trial_ends_at, created_at, updated_at
) ON quartermaster.tenants TO frameworks_analytics_ro;

-- invite_token is a live credential; expose the lifecycle columns only.
GRANT SELECT (
    id, tenant_id, cluster_id, access_level, resource_limits,
    subscription_status, requested_at, approved_at, approved_by, rejection_reason,
    is_active, granted_at, expires_at, created_at, updated_at
) ON quartermaster.tenant_cluster_access TO frameworks_analytics_ro;
