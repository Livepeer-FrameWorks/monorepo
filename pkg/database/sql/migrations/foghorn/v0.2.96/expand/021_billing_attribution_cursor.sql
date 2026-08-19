-- Durable keyset cursor for the identity-aware billing-attribution reconciliation
-- (control.ReconcileBillingAttribution). That pass reviews DISTINCT (tenant, authoritative-cluster) pairs
-- among still-unmarked synced rows and, for non-local clusters, makes an EXTERNAL per-tenant resolver call to
-- decide ownership. An unbounded ordered scan with the resolver in the row loop, under a shared timeout, could
-- expire mid-scan and restart at the SAME prefix every pass — a large/slow prefix would then permanently
-- STARVE later locally-owned pairs. This single-row cursor lets each pass process a BOUNDED batch of pairs
-- past the cursor and persist progress, so every pair is reached within a bounded number of passes and then
-- the cursor wraps (re-checking pairs whose tenant access may have changed).
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.
CREATE TABLE IF NOT EXISTS foghorn.billing_attribution_cursor (
    id           BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),  -- single-row guard
    last_tenant  TEXT NOT NULL DEFAULT '',
    last_cluster TEXT NOT NULL DEFAULT ''
);
INSERT INTO foghorn.billing_attribution_cursor (id) VALUES (true) ON CONFLICT (id) DO NOTHING;
