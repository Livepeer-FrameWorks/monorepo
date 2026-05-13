-- Enterprise stream pinning side table. A row here locks a stream to a
-- constrained set of clusters regardless of tenant-wide tenant_cluster_access.
-- Absence (no row for stream_id) means policy-derived placement applies
-- normally. Side-table shape avoids a perpetually-NULL TEXT[] on
-- commodore.streams, since ~all rows in production never need pinning.
-- pinned_by + pin_reason give an audit trail for support/ops review.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

CREATE TABLE IF NOT EXISTS commodore.stream_cluster_pins (
    stream_id UUID PRIMARY KEY REFERENCES commodore.streams(id) ON DELETE CASCADE,
    allowed_cluster_ids TEXT[] NOT NULL,
    pinned_by UUID,
    pin_reason TEXT,
    pinned_at TIMESTAMP NOT NULL DEFAULT NOW()
);
