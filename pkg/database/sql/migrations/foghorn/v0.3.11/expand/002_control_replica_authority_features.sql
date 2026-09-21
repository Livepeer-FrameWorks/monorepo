-- What a replica does with media authority beyond placement: level 1 accepts
-- media-object authorities valid for up to 30 days, fetches an authority the
-- cell does not hold, and reports which authorities it decides on. A replica
-- that predates the column never writes it and stays at 0, so the cell attests
-- a level only when every live replica has reached it.
ALTER TABLE foghorn.control_replicas
    ADD COLUMN IF NOT EXISTS authority_feature_level INTEGER NOT NULL DEFAULT 0;
