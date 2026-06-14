-- Tenant-wide master switch for Skipper AI monitoring/notifications.
-- Default TRUE preserves current behavior for all existing tenants. Adding a
-- NOT NULL column WITH a constant default is expand-safe (existing rows are
-- backfilled to TRUE; no SET NOT NULL on an existing column).
ALTER TABLE quartermaster.tenants
  ADD COLUMN IF NOT EXISTS monitoring_enabled BOOLEAN NOT NULL DEFAULT TRUE;
