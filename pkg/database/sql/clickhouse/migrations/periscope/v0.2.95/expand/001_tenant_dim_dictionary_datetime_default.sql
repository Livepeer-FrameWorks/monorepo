-- Recreate tenant_dim with a DateTime default ClickHouse dictionary DDL accepts.
-- Dictionary attribute DEFAULT values must be parseable literals, not expressions.
DROP DICTIONARY IF EXISTS tenant_dim;

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
