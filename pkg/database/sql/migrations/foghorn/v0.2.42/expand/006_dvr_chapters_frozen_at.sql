-- Record when a chapter row enters state='frozen' so the reclaim
-- sweep can anchor its abandoned-node grace period on that
-- transition instead of chapter creation. created_at is wrong: for
-- long-running chapters it lands hours past grace the moment freeze
-- completes; for short chapters it under-counts.

ALTER TABLE foghorn.dvr_chapters
    ADD COLUMN IF NOT EXISTS frozen_at TIMESTAMPTZ;
