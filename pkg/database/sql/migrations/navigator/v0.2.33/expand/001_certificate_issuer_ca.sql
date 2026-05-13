-- Tracks which ACME CA issued each certificate so renewals route to the
-- same CA. Required for the Google Trust Services fallback path: certs
-- initially issued by Let's Encrypt renew via LE; certs issued by GTS
-- renew via GTS. Without this column, multi-CA Navigator would round-
-- robin renewals through the wrong issuer and break ARI semantics.
-- Schema source of truth: pkg/database/sql/schema/navigator.sql

ALTER TABLE navigator.certificates
    ADD COLUMN IF NOT EXISTS issuer_ca TEXT NOT NULL DEFAULT 'letsencrypt';
