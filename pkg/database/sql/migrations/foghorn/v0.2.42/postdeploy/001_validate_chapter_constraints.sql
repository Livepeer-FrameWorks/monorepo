-- Validate the chapter / segment constraints added NOT VALID in expand.
-- Each VALIDATE takes a SHARE lock and scans the table; the new
-- constraints are either strictly broader than the old ones (segments
-- status) or guaranteed-satisfied (chapter state DEFAULT 'open',
-- chapter mode has no 'explicit_range' rows per clean-slate). FK
-- validation walks the existing rows under the same SHARE lock.

ALTER TABLE foghorn.dvr_chapters
    VALIDATE CONSTRAINT chk_foghorn_dvr_chapters_state;

ALTER TABLE foghorn.dvr_chapters
    VALIDATE CONSTRAINT chk_foghorn_dvr_chapters_mode;

ALTER TABLE foghorn.dvr_chapters
    VALIDATE CONSTRAINT fk_foghorn_dvr_chapters_playback_artifact;

ALTER TABLE foghorn.dvr_segments
    VALIDATE CONSTRAINT chk_foghorn_dvr_segments_status;
