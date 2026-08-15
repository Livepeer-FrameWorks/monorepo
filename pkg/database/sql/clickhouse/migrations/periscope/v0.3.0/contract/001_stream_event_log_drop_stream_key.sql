-- A publishing credential is not part of the stream-event projection; streams
-- are identified by stream_id and internal_name. The contract phase runs after
-- ingest writers use that credential-free row shape. IF EXISTS keeps the delta
-- idempotent when applied on top of the current baseline.
ALTER TABLE stream_event_log DROP COLUMN IF EXISTS stream_key;
