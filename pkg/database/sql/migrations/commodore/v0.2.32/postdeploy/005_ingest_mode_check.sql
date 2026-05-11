-- Validate the streams_ingest_mode_chk constraint added NOT VALID in the
-- expand phase. Splitting expand → postdeploy lets the constraint be added
-- without a full table scan blocking writes during deploy, then validated
-- after binaries that respect the constraint are rolled forward.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.streams
    VALIDATE CONSTRAINT streams_ingest_mode_chk;
