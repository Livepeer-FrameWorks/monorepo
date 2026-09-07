ALTER TABLE commodore.push_targets
    DROP CONSTRAINT IF EXISTS ck_commodore_push_targets_platform;
ALTER TABLE commodore.push_targets
    ADD CONSTRAINT ck_commodore_push_targets_platform
    CHECK (platform IN ('twitch', 'youtube', 'facebook', 'kick', 'x', 'custom')) NOT VALID;

ALTER TABLE commodore.push_targets
    DROP CONSTRAINT IF EXISTS ck_commodore_push_targets_status;
ALTER TABLE commodore.push_targets
    ADD CONSTRAINT ck_commodore_push_targets_status
    CHECK (status IN ('pending', 'pushing', 'retrying', 'stopping', 'idle', 'failed')) NOT VALID;

ALTER TABLE commodore.push_targets
    VALIDATE CONSTRAINT ck_commodore_push_targets_platform;
ALTER TABLE commodore.push_targets
    VALIDATE CONSTRAINT ck_commodore_push_targets_status;
ALTER TABLE commodore.push_targets
    VALIDATE CONSTRAINT ck_commodore_push_targets_reason_code;
