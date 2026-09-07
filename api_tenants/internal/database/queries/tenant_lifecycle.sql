-- name: LockTenantProvisioningKey :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(provisioning_key), 0));

-- name: FindTenantIDByProvisioningKey :one
SELECT id::text AS id
FROM quartermaster.tenants
WHERE provisioning_key = sqlc.arg(provisioning_key);

-- name: CreateTenantRecord :exec
INSERT INTO quartermaster.tenants
    (id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
     deployment_tier, deployment_model, is_active, billing_entitlements_observed_at, created_at, updated_at)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(name), sqlc.arg(subdomain), sqlc.narg(custom_domain),
        sqlc.narg(logo_url), sqlc.arg(primary_color), sqlc.arg(secondary_color),
        sqlc.arg(deployment_tier), sqlc.arg(deployment_model), true, sqlc.arg(created_at)::timestamptz,
        sqlc.arg(created_at)::timestamptz, sqlc.arg(created_at)::timestamptz);

-- name: CreateTenantRecordWithProvisioningKey :exec
INSERT INTO quartermaster.tenants
    (id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
     deployment_tier, deployment_model, is_active, billing_entitlements_observed_at, created_at, updated_at, provisioning_key)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(name), sqlc.arg(subdomain), sqlc.narg(custom_domain),
        sqlc.narg(logo_url), sqlc.arg(primary_color), sqlc.arg(secondary_color),
        sqlc.arg(deployment_tier), sqlc.arg(deployment_model), true, sqlc.arg(created_at)::timestamptz,
        sqlc.arg(created_at)::timestamptz, sqlc.arg(created_at)::timestamptz, sqlc.arg(provisioning_key));

-- name: CreateTenantAttribution :exec
INSERT INTO quartermaster.tenant_attribution
    (tenant_id, signup_channel, signup_method, utm_source, utm_medium,
     utm_campaign, utm_content, utm_term, http_referer, landing_page,
     referral_code, is_agent, metadata)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(signup_channel), sqlc.arg(signup_method),
        sqlc.arg(utm_source), sqlc.arg(utm_medium), sqlc.arg(utm_campaign),
        sqlc.arg(utm_content), sqlc.arg(utm_term), sqlc.arg(http_referer),
        sqlc.arg(landing_page), sqlc.arg(referral_code), sqlc.arg(is_agent),
        sqlc.arg(metadata)::text::jsonb)
ON CONFLICT (tenant_id) DO NOTHING;

-- name: IncrementReferralCodeUsage :exec
UPDATE quartermaster.referral_codes
SET current_uses = current_uses + 1
WHERE code = sqlc.arg(code) AND is_active = true;

-- name: GetDefaultActiveClusterID :one
SELECT cluster_id
FROM quartermaster.infrastructure_clusters
WHERE is_default_cluster = true AND is_active = true
LIMIT 1;

-- name: GrantDefaultClusterAccess :exec
INSERT INTO quartermaster.tenant_cluster_access
    (tenant_id, cluster_id, access_level, access_source, subscription_status, is_active, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), 'subscriber', 'platform_tier', 'active', true,
        sqlc.arg(created_at)::timestamptz, sqlc.arg(created_at)::timestamptz)
ON CONFLICT (tenant_id, cluster_id) DO NOTHING;

-- name: SetTenantOfficialCluster :exec
UPDATE quartermaster.tenants
SET official_cluster_id = sqlc.arg(cluster_id)
WHERE id = sqlc.arg(tenant_id)::uuid;

-- name: LockTenantPreviousValues :one
SELECT primary_cluster_id, custom_domain, subdomain
FROM quartermaster.tenants
WHERE id = sqlc.arg(tenant_id)::uuid
FOR UPDATE;

-- name: GetTenantCustomDomainEligibility :one
SELECT t.custom_domain, t.custom_subdomain_enabled, t.custom_domain_enabled, t.is_active,
	   t.billing_entitlements_observed_at,
	   EXISTS (SELECT 1 FROM quartermaster.tenant_cluster_access tca
	           JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = tca.cluster_id
               WHERE tca.tenant_id = t.id
                 AND tca.is_active = true
                 AND tca.subscription_status = 'active'
                 AND tca.access_source <> 'unknown'
	             AND (tca.expires_at IS NULL OR tca.expires_at > NOW())
	             AND cluster.is_active = true) AS has_cluster
FROM quartermaster.tenants t
WHERE t.id = sqlc.arg(tenant_id)::uuid;

-- name: LockTenantAliasEligibility :one
SELECT t.name, t.subdomain, t.custom_subdomain_enabled, t.is_active,
	   t.billing_entitlements_observed_at,
	   EXISTS (SELECT 1 FROM quartermaster.tenant_cluster_access tca
	           JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = tca.cluster_id
               WHERE tca.tenant_id = t.id
                 AND tca.is_active = true
                 AND tca.subscription_status = 'active'
                 AND tca.access_source <> 'unknown'
	             AND (tca.expires_at IS NULL OR tca.expires_at > NOW())
	             AND cluster.is_active = true) AS has_cluster
FROM quartermaster.tenants t
WHERE t.id = sqlc.arg(tenant_id)::uuid
FOR UPDATE;

-- name: SetGeneratedTenantSubdomain :exec
UPDATE quartermaster.tenants
SET subdomain = sqlc.arg(subdomain), updated_at = NOW()
WHERE id = sqlc.arg(tenant_id)::uuid;

-- name: TenantHasPaidClusterAccess :one
SELECT t.billing_entitlements_observed_at <> 'epoch'::timestamptz AS entitlements_observed,
	EXISTS (
    SELECT 1
    FROM quartermaster.tenant_cluster_access tca
	JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = tca.cluster_id
    WHERE tca.tenant_id = sqlc.arg(tenant_id)::uuid
      AND tca.is_active = true
      AND tca.subscription_status = 'active'
      AND tca.access_source <> 'unknown'
      AND (tca.expires_at IS NULL OR tca.expires_at > NOW())
	      AND t.is_active = true
      AND t.custom_subdomain_enabled = true
	      AND cluster.is_active = true
	) AS has_paid_cluster_access
FROM quartermaster.tenants t
WHERE t.id = sqlc.arg(tenant_id)::uuid;

-- name: LockTenantBillingEntitlements :one
SELECT deployment_tier, custom_subdomain_enabled, custom_domain_enabled,
       billing_entitlements_observed_at
FROM quartermaster.tenants
WHERE id = sqlc.arg(tenant_id)::uuid
FOR UPDATE;

-- name: ApplyTenantBillingEntitlements :execrows
UPDATE quartermaster.tenants
SET deployment_tier = sqlc.arg(deployment_tier),
    custom_subdomain_enabled = sqlc.arg(custom_subdomain_enabled),
    custom_domain_enabled = sqlc.arg(custom_domain_enabled),
    billing_entitlements_observed_at = sqlc.arg(observed_at),
    updated_at = NOW()
WHERE id = sqlc.arg(tenant_id)::uuid
  AND billing_entitlements_observed_at <= sqlc.arg(observed_at);

-- name: CompleteBillingEntitlementHandoff :execrows
INSERT INTO quartermaster.billing_entitlement_handoffs (handoff_key, subscription_count)
VALUES (sqlc.arg(handoff_key), sqlc.arg(subscription_count))
ON CONFLICT (handoff_key) DO NOTHING;

-- name: HasBillingEntitlementHandoff :one
SELECT EXISTS (
    SELECT 1
    FROM quartermaster.billing_entitlement_handoffs
    WHERE handoff_key = sqlc.arg(handoff_key)
);

-- name: GetTenantBillingEntitlementCensus :one
SELECT COUNT(*)::bigint AS tenant_count,
       COUNT(*) FILTER (WHERE billing_entitlements_observed_at = 'epoch'::timestamptz)::bigint AS unobserved_count
FROM quartermaster.tenants;

-- name: LockActiveTenantDomains :one
SELECT custom_domain, subdomain
FROM quartermaster.tenants
WHERE id = sqlc.arg(tenant_id)::uuid AND is_active = true
FOR UPDATE;

-- name: DeactivateTenant :execrows
UPDATE quartermaster.tenants
SET is_active = false, updated_at = NOW()
WHERE id = sqlc.arg(tenant_id)::uuid AND is_active = true;

-- name: GetTenantPrimaryClusterID :one
SELECT primary_cluster_id
FROM quartermaster.tenants
WHERE id = sqlc.arg(tenant_id)::uuid;

-- name: TenantHasActiveClusterAccess :one
SELECT EXISTS (
	SELECT 1 FROM quartermaster.tenant_cluster_access access
	JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = access.cluster_id
	WHERE access.tenant_id = sqlc.arg(tenant_id)::uuid
	  AND access.cluster_id = sqlc.arg(cluster_id)
	  AND access.is_active = true
	  AND access.subscription_status = 'active'
	  AND access.access_source <> 'unknown'
	  AND (access.expires_at IS NULL OR access.expires_at > NOW())
	  AND cluster.is_active = true
);

-- name: GetInfrastructureClusterType :one
SELECT cluster_type
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: UpdateTenantPrimaryCluster :execrows
UPDATE quartermaster.tenants
SET primary_cluster_id = sqlc.arg(primary_cluster_id), updated_at = NOW()
WHERE id = sqlc.arg(tenant_id)::uuid AND is_active = true;

-- name: UpdateTenantDeploymentModel :execrows
UPDATE quartermaster.tenants
SET deployment_model = sqlc.arg(deployment_model), updated_at = NOW()
WHERE id = sqlc.arg(tenant_id)::uuid AND is_active = true;

-- name: UpdateTenantClusterAndDeploymentModel :execrows
UPDATE quartermaster.tenants
SET primary_cluster_id = sqlc.arg(primary_cluster_id),
    deployment_model = sqlc.arg(deployment_model),
    updated_at = NOW()
WHERE id = sqlc.arg(tenant_id)::uuid AND is_active = true;

-- name: RepointTenantPrimaryToOfficialIfCluster :execrows
UPDATE quartermaster.tenants AS tenant
SET primary_cluster_id = tenant.official_cluster_id,
    updated_at = NOW()
WHERE tenant.id = sqlc.arg(tenant_id)::uuid
  AND tenant.is_active = true
  AND tenant.primary_cluster_id = sqlc.arg(lost_cluster_id)::text
  AND tenant.official_cluster_id IS NOT NULL
  AND tenant.official_cluster_id <> sqlc.arg(lost_cluster_id)::text
  AND EXISTS (
      SELECT 1
      FROM quartermaster.tenant_cluster_access access
      JOIN quartermaster.infrastructure_clusters cluster
        ON cluster.cluster_id = access.cluster_id
       AND cluster.is_active = true
      WHERE access.tenant_id = tenant.id
        AND access.cluster_id = tenant.official_cluster_id
        AND access.is_active = true
        AND access.subscription_status = 'active'
        AND access.access_source <> 'unknown'
        AND (access.expires_at IS NULL OR access.expires_at > NOW())
  );
