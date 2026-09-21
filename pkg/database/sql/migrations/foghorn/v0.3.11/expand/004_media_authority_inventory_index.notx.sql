CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_media_authorities_inventory
    ON foghorn.media_authorities(authority_kind COLLATE "C" ASC, authority_id COLLATE "C" ASC)
    INCLUDE (authority_version, valid_until);
