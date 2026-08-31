ALTER TABLE navigator.tls_bundles
    ADD COLUMN IF NOT EXISTS version TEXT NOT NULL DEFAULT '';

ALTER TABLE navigator.tenant_edge_apply_state
    ADD COLUMN IF NOT EXISTS bundle_version TEXT NOT NULL DEFAULT '';
