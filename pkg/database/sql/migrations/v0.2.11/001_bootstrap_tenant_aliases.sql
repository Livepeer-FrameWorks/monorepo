-- Bootstrap tenant alias mapping (Quartermaster).
--
-- The schema file (pkg/database/sql/schema/quartermaster.sql) carries the
-- canonical CREATE for fresh schemas. This migration adds the table to
-- existing Quartermaster schemas and records the change in the _migrations
-- table.
--
-- Safe on every DB in postgres_databases: the body only fires when the
-- `quartermaster` schema is present, so applying this migration to purser /
-- commodore / other databases is a no-op.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = 'quartermaster') THEN
        CREATE TABLE IF NOT EXISTS quartermaster.bootstrap_tenant_aliases (
            alias       TEXT PRIMARY KEY,
            tenant_id   UUID NOT NULL REFERENCES quartermaster.tenants(id) ON DELETE CASCADE,
            created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT chk_bootstrap_tenant_alias_format CHECK (alias ~ '^[a-z][a-z0-9-]{0,63}$')
        );
    END IF;
END
$$;
