-- Validate the source CHECK added NOT VALID in expand. Every row written so
-- far carries the column default 'runtime', so validation is a trivial scan
-- under SHARE lock. After this completes the constraint is enforced for all
-- existing rows; new inserts have been guarded by the NOT VALID constraint
-- since expand ran.

ALTER TABLE quartermaster.service_cluster_assignments
    VALIDATE CONSTRAINT service_cluster_assignments_source_check;
