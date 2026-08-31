ALTER TABLE navigator.tls_bundles
    ALTER COLUMN version SET DEFAULT '',
    ALTER COLUMN version SET NOT NULL;

ALTER TABLE navigator.tenant_edge_apply_state
    ALTER COLUMN bundle_version SET DEFAULT '',
    ALTER COLUMN bundle_version SET NOT NULL;
