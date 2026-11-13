DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('quartermaster.idx_service_instances_manifest_owner') AND indisvalid AND indisready)
    THEN RAISE EXCEPTION 'quartermaster.idx_service_instances_manifest_owner is missing or invalid'; END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('quartermaster.idx_qm_service_instances_service_health') AND indisvalid AND indisready)
    THEN RAISE EXCEPTION 'quartermaster.idx_qm_service_instances_service_health is missing or invalid'; END IF;
END $$;
