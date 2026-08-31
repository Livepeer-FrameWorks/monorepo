ALTER TABLE quartermaster.node_fingerprints
    ADD COLUMN IF NOT EXISTS node_identity_public_key_ed25519 BYTEA;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'quartermaster.node_fingerprints'::regclass
          AND conname = 'chk_qm_node_identity_public_key'
    ) THEN
        ALTER TABLE quartermaster.node_fingerprints
            ADD CONSTRAINT chk_qm_node_identity_public_key
            CHECK (node_identity_public_key_ed25519 IS NULL OR octet_length(node_identity_public_key_ed25519) = 32) NOT VALID;
    END IF;
END
$$;
