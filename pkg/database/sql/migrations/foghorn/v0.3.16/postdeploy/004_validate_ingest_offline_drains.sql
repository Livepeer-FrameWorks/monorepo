ALTER TABLE foghorn.ingest_offline_effects
    VALIDATE CONSTRAINT ck_foghorn_ingest_offline_effects_drain_binding;
ALTER TABLE foghorn.ingest_offline_effects
    VALIDATE CONSTRAINT ck_foghorn_ingest_offline_effects_drain_settled;
