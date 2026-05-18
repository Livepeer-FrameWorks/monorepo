-- Retire commodore.dvr_chapter_aliases. The chapter-id → origin-cluster
-- lookup it provided is unnecessary now that chapter playback resolves
-- to a regular VOD artifact whose origin cluster is already tracked on
-- foghorn.artifacts.origin_cluster_id. Cross-cluster chapter playback
-- becomes cross-cluster VOD playback against the chapter artifact.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

DROP TABLE IF EXISTS commodore.dvr_chapter_aliases;
