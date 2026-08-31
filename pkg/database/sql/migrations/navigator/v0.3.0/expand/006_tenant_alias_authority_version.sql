ALTER TABLE navigator.tenant_aliases
    ADD COLUMN IF NOT EXISTS authority_version BIGINT NOT NULL DEFAULT 1;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'ck_navigator_tenant_alias_authority_version'
          AND conrelid = 'navigator.tenant_aliases'::regclass
    ) THEN
        ALTER TABLE navigator.tenant_aliases
            ADD CONSTRAINT ck_navigator_tenant_alias_authority_version
            CHECK (authority_version > 0) NOT VALID;
    END IF;
END $$;
