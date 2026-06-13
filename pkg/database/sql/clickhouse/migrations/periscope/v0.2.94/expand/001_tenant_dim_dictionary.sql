-- Tenant dimension dictionary for operator tenant-activity analytics: lets
-- analytics queries label tenant_id columns with the tenant's name/tier
-- instead of a bare UUID.
--
-- Source is quartermaster.tenants through the quartermaster_pg named
-- collection (config.d/named-collections.xml, provisioned from the manifest;
-- authenticates as the frameworks_analytics_ro role; this is the sanctioned
-- operator-analytics exception to the no-cross-service-DB-reads rule).
-- Attribute names match the Postgres columns so no custom query is needed;
-- the key column is `id`, so lookups are dictGet('periscope.tenant_dim',
-- 'name', tuple(tenant_id)). Lazy load means CREATE succeeds even when
-- Postgres is unreachable; the first dictGet surfaces connection errors.
-- The same DDL is appended to the baseline periscope.sql so a fresh init
-- and an upgrade converge on the same schema.

-- Attribute DEFAULTs must be plain parseable literals: expressions like
-- toDateTime(0) are a hard SYNTAX_ERROR in dictionary DDL.
CREATE DICTIONARY IF NOT EXISTS tenant_dim
(
    id UUID,
    name String DEFAULT '',
    subdomain String DEFAULT '',
    deployment_tier String DEFAULT '',
    is_active UInt8 DEFAULT 0,
    created_at DateTime DEFAULT '1970-01-01 00:00:00'
)
PRIMARY KEY id
SOURCE(POSTGRESQL(NAME quartermaster_pg TABLE 'tenants'))
LAYOUT(COMPLEX_KEY_HASHED())
LIFETIME(MIN 300 MAX 600);
