CREATE TABLE IF NOT EXISTS navigator.certificate_issuance_leases (
    lease_key TEXT PRIMARY KEY,
    lease_owner TEXT NOT NULL,
    lease_until TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
