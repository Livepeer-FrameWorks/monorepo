-- Access tokens issued at or before this instant (whole seconds) no longer
-- hold realtime connections; set when every session of the user ends (sign
-- out, password reset, refresh-token reuse). NULL means never revoked.
ALTER TABLE commodore.users
    ADD COLUMN IF NOT EXISTS sessions_revoked_at TIMESTAMPTZ;
