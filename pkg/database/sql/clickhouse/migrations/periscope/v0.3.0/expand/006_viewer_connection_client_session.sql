ALTER TABLE periscope.viewer_connection_events
    ADD COLUMN IF NOT EXISTS client_session_id String DEFAULT '' AFTER session_id;

ALTER TABLE periscope.viewer_connection_events
    ADD INDEX IF NOT EXISTS idx_viewer_client_session client_session_id TYPE bloom_filter(0.01) GRANULARITY 1;

ALTER TABLE periscope.viewer_connection_events
    MATERIALIZE INDEX idx_viewer_client_session;
