-- Validate the expanded streams_ingest_mode_chk constraint (added NOT VALID in
-- the expand phase). Splitting expand → postdeploy lets the new value land
-- without a full table scan blocking writes during deploy, then validates
-- after binaries that emit 'mist_native' have rolled forward.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.streams
    VALIDATE CONSTRAINT streams_ingest_mode_chk;
