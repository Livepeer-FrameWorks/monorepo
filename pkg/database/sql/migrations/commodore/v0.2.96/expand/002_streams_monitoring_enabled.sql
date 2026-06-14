-- Per-stream Skipper monitoring toggle. Tri-state:
--   NULL  = INHERIT (follow the tenant's tier entitlement) -- default
--   TRUE  = ON  (monitor regardless of billing tier)
--   FALSE = OFF (never monitor)
-- Nullable so existing streams default to INHERIT and behavior is unchanged.
ALTER TABLE commodore.streams
  ADD COLUMN IF NOT EXISTS monitoring_enabled BOOLEAN;
