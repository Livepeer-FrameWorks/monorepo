CREATE TABLE IF NOT EXISTS foghorn.vod_original_retention_activation (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    activated_at TIMESTAMPTZ
);

INSERT INTO foghorn.vod_original_retention_activation (singleton, activated_at)
VALUES (true, NULL)
ON CONFLICT (singleton) DO NOTHING;
