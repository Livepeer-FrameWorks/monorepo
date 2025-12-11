CREATE SCHEMA IF NOT EXISTS navigator;

CREATE TABLE IF NOT EXISTS navigator.certificates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    domain TEXT NOT NULL UNIQUE,
    cert_pem TEXT NOT NULL,
    key_pem TEXT NOT NULL, -- Encrypted at rest (future) or restricted access
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS navigator.acme_accounts (
    email TEXT PRIMARY KEY,
    registration_json TEXT NOT NULL, -- Serialized ACME registration
    private_key_pem TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
