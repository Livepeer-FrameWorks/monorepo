ALTER TABLE foghorn.node_admissions
    ALTER COLUMN public_key_ed25519 SET NOT NULL,
    ALTER COLUMN valid_until SET NOT NULL;
