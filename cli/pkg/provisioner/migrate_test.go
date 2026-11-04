package provisioner

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestDiscoverMigrationsPhaseLayout(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/purser/v0.3.0/expand/002_add_column.sql":        {Data: []byte("ALTER TABLE purser.example ADD COLUMN IF NOT EXISTS name TEXT;")},
		"migrations/purser/v0.3.0/expand/001_add_table.sql":         {Data: []byte("CREATE TABLE IF NOT EXISTS purser.example(id UUID PRIMARY KEY);")},
		"migrations/purser/v0.3.0/postdeploy/001_verify.sql":        {Data: []byte("SELECT 1;")},
		"migrations/purser/v0.3.1/contract/001_drop_old.sql":        {Data: []byte("ALTER TABLE purser.example DROP COLUMN old_name;")},
		"migrations/quartermaster/v0.3.0/expand/001_index.notx.sql": {Data: []byte("CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_example ON quartermaster.example(id);")},
	}

	got, err := discoverMigrationsInFS(fsys, "migrations", map[string]bool{
		"purser":        true,
		"quartermaster": true,
	})
	if err != nil {
		t.Fatalf("discoverMigrationsInFS returned error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("len(got) = %d, want 5", len(got))
	}

	wantOrder := []struct {
		db            string
		version       string
		phase         string
		sequence      int
		transactional bool
	}{
		{"purser", "v0.3.0", "expand", 1, true},
		{"purser", "v0.3.0", "expand", 2, true},
		{"purser", "v0.3.0", "postdeploy", 1, true},
		{"purser", "v0.3.1", "contract", 1, true},
		{"quartermaster", "v0.3.0", "expand", 1, false},
	}

	for i, want := range wantOrder {
		if got[i].Database != want.db ||
			got[i].Version != want.version ||
			got[i].Phase != want.phase ||
			got[i].Sequence != want.sequence ||
			got[i].Transactional != want.transactional {
			t.Fatalf("got[%d] = %#v, want db=%s version=%s phase=%s seq=%d transactional=%v",
				i, got[i], want.db, want.version, want.phase, want.sequence, want.transactional)
		}
	}
}

func TestDiscoverMigrationsRejectsFlatLayout(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/v0.3.0/001_old_shape.sql": {Data: []byte("SELECT 1;")},
	}

	_, err := discoverMigrationsInFS(fsys, "migrations", map[string]bool{"purser": true})
	if err == nil {
		t.Fatal("discoverMigrationsInFS returned nil error for flat migration layout")
	}
}

func TestDiscoverMigrationsRejectsUnknownDatabase(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/billing/v0.3.0/expand/001_add_table.sql": {Data: []byte("SELECT 1;")},
	}

	_, err := discoverMigrationsInFS(fsys, "migrations", map[string]bool{"purser": true})
	if err == nil {
		t.Fatal("discoverMigrationsInFS returned nil error for unknown database")
	}
}

func TestValidateMigrationSetRejectsUnsafeExpandSQL(t *testing.T) {
	migrations := []Migration{
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/expand/001_bad.sql",
			Transactional: true,
			content:       "ALTER TABLE purser.billing_invoices DROP COLUMN legacy_total;",
		},
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      2,
			Path:          "migrations/purser/v0.3.0/expand/002_index.sql",
			Transactional: true,
			content:       "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_invoice_tenant ON purser.billing_invoices(tenant_id);",
		},
	}

	err := validatePostgresMigrationSet(migrations)
	if err == nil {
		t.Fatal("validatePostgresMigrationSet returned nil error")
	}
	if !IsMigrationValidationError(err) {
		t.Fatalf("validatePostgresMigrationSet error type = %T, want MigrationValidationError", err)
	}
}

func TestValidateMigrationSetAcceptsSafeExpandSQL(t *testing.T) {
	migrations := []Migration{
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/expand/001_add.sql",
			Transactional: true,
			content:       "ALTER TABLE purser.billing_invoices ADD COLUMN IF NOT EXISTS rating_version TEXT;",
		},
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      2,
			Path:          "migrations/purser/v0.3.0/expand/002_index.notx.sql",
			Transactional: false,
			content:       "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_invoice_tenant ON purser.billing_invoices(tenant_id);",
		},
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "postdeploy",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/postdeploy/001_require.sql",
			Transactional: true,
			content:       "ALTER TABLE purser.billing_invoices ALTER COLUMN rating_version SET NOT NULL; SELECT pg_index.indisvalid, pg_index.indisready FROM pg_index WHERE indexrelid=to_regclass('idx_invoice_tenant');",
		},
	}

	if err := validatePostgresMigrationSet(migrations); err != nil {
		t.Fatalf("validatePostgresMigrationSet returned error: %v", err)
	}
}

func TestValidateMigrationSetAcceptsPushTargetURITextWidening(t *testing.T) {
	migration := Migration{
		Database: "commodore", Version: "v0.3.0", Phase: "expand", Sequence: 1,
		Path: "migrations/commodore/v0.3.0/expand/001_widen.sql", Transactional: true,
		content: "ALTER TABLE commodore.push_targets ALTER COLUMN target_uri TYPE TEXT;",
	}
	if err := validatePostgresMigrationSet([]Migration{migration}); err != nil {
		t.Fatalf("metadata-only push target URI widening rejected: %v", err)
	}
}

func TestValidateMigrationSetRejectsOtherExpandTypeChanges(t *testing.T) {
	migration := Migration{
		Database: "commodore", Version: "v0.3.0", Phase: "expand", Sequence: 1,
		Path: "migrations/commodore/v0.3.0/expand/001_rewrite.sql", Transactional: true,
		content: "ALTER TABLE commodore.push_targets ALTER COLUMN status TYPE TEXT;",
	}
	if err := validatePostgresMigrationSet([]Migration{migration}); err == nil || !strings.Contains(err.Error(), "column type rewrites") {
		t.Fatalf("unsafe type change error = %v, want expand compatibility rejection", err)
	}
}

func TestValidateEmbeddedPostgresMigrations(t *testing.T) {
	if err := ValidateEmbeddedPostgresMigrations(); err != nil {
		t.Fatalf("ValidateEmbeddedPostgresMigrations returned error: %v", err)
	}
}

func TestValidateEmbeddedClickHouseMigrations(t *testing.T) {
	if err := ValidateEmbeddedClickHouseMigrations(); err != nil {
		t.Fatalf("ValidateEmbeddedClickHouseMigrations returned error: %v", err)
	}
}

func TestValidateMigrationReleaseCeilingRejectsFutureVersion(t *testing.T) {
	migrations := []Migration{
		{Version: "v0.2.96", Path: "migrations/purser/v0.2.96/expand/001_current.sql"},
		{Version: "v0.3.0", Path: "migrations/purser/v0.3.0/expand/001_future.sql"},
	}
	err := validateMigrationReleaseCeiling(migrations, "v0.2.96")
	if err == nil {
		t.Fatal("a migration newer than the latest declared release must fail validation")
	}
	if !IsMigrationValidationError(err) || !strings.Contains(err.Error(), "v0.3.0") {
		t.Fatalf("future migration error = %v, want a version-specific MigrationValidationError", err)
	}
}

func TestValidateMigrationSet_NotxRequiresIfNotExists(t *testing.T) {
	migrations := []Migration{
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/expand/001_idx.notx.sql",
			Transactional: false,
			content:       "CREATE INDEX CONCURRENTLY idx_x ON purser.t (col);",
		},
	}
	err := validatePostgresMigrationSet(migrations)
	if err == nil {
		t.Fatal("expected validation error for notx CREATE INDEX CONCURRENTLY without IF NOT EXISTS")
	}
	if !IsMigrationValidationError(err) {
		t.Fatalf("got %T, want MigrationValidationError", err)
	}
}

func TestValidateMigrationSet_NotxWithIfNotExistsPasses(t *testing.T) {
	migrations := []Migration{
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/expand/001_idx.notx.sql",
			Transactional: false,
			content:       "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x ON purser.t (col);",
		},
		concurrentIndexGuardMigration("purser", "v0.3.0", "idx_x"),
	}
	if err := validatePostgresMigrationSet(migrations); err != nil {
		t.Fatalf("validatePostgresMigrationSet returned error: %v", err)
	}
}

func TestValidateMigrationSet_NotxAcceptsMultipleSafeStatements(t *testing.T) {
	migrations := []Migration{
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/expand/001_indexes.notx.sql",
			Transactional: false,
			content: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x ON purser.t (col);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_y ON purser.t (other_col);`,
		},
		concurrentIndexGuardMigration("purser", "v0.3.0", "idx_x", "idx_y"),
	}
	if err := validatePostgresMigrationSet(migrations); err != nil {
		t.Fatalf("validatePostgresMigrationSet returned error: %v", err)
	}
}

func TestValidateMigrationSet_NotxRequiresPostdeployValidityGuard(t *testing.T) {
	migrations := []Migration{{
		Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
		Path: "migrations/purser/v0.3.0/expand/001_idx.notx.sql", Transactional: false,
		content: "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x ON purser.t (col);",
	}}
	err := validatePostgresMigrationSet(migrations)
	if err == nil || !strings.Contains(err.Error(), "postdeploy pg_index guard") {
		t.Fatalf("validation error = %v, want missing postdeploy validity guard", err)
	}
}

func TestValidateMigrationSet_NotxGuardRequiresExactIndexInSameRelease(t *testing.T) {
	expand := Migration{
		Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
		Path: "migrations/purser/v0.3.0/expand/001_idx.notx.sql", Transactional: false,
		content: "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x ON purser.t (col);",
	}
	for _, test := range []struct {
		name  string
		guard Migration
	}{
		{name: "partial name", guard: concurrentIndexGuardMigration("purser", "v0.3.0", "idx_x_shadow")},
		{name: "wrong release", guard: concurrentIndexGuardMigration("purser", "v0.3.1", "idx_x")},
		{name: "comment only", guard: Migration{
			Database: "purser", Version: "v0.3.0", Phase: "postdeploy", Sequence: 99,
			Path: "migrations/purser/v0.3.0/postdeploy/099_validate_indexes.sql", Transactional: true,
			content: `DO $$ BEGIN
-- idx_x
PERFORM pg_index.indisvalid, pg_index.indisready FROM pg_index;
END $$;`,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validatePostgresMigrationSet([]Migration{expand, test.guard})
			if err == nil || !strings.Contains(err.Error(), "postdeploy pg_index guard") {
				t.Fatalf("validation error = %v, want exact same-release guard rejection", err)
			}
		})
	}
}

func concurrentIndexGuardMigration(database, version string, indexNames ...string) Migration {
	return Migration{
		Database: database, Version: version, Phase: "postdeploy", Sequence: 99,
		Path:          "migrations/" + database + "/" + version + "/postdeploy/099_validate_indexes.sql",
		Transactional: true,
		content: "SELECT pg_index.indisvalid, pg_index.indisready FROM pg_index WHERE indexrelid IN (to_regclass('" +
			strings.Join(indexNames, "'), to_regclass('") + "'));",
	}
}

func TestValidateMigrationSet_NotxRejectsUnsupportedStatement(t *testing.T) {
	migrations := []Migration{
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/expand/001_indexes.notx.sql",
			Transactional: false,
			content: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x ON purser.t (col);
ANALYZE purser.t;`,
		},
	}
	err := validatePostgresMigrationSet(migrations)
	if err == nil || !strings.Contains(err.Error(), "only CREATE INDEX CONCURRENTLY is supported") {
		t.Fatalf("validation error = %v, want unsupported-statement MigrationValidationError", err)
	}
}

func TestValidateMigrationSet_NotxRequiresIfNotExistsForEveryConcurrentIndex(t *testing.T) {
	migrations := []Migration{
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/expand/001_idx.notx.sql",
			Transactional: false,
			content: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x ON purser.t (col);
CREATE INDEX CONCURRENTLY idx_y ON purser.t (other_col);`,
		},
	}
	err := validatePostgresMigrationSet(migrations)
	if err == nil {
		t.Fatal("expected validation error for mixed safe and unsafe concurrent indexes")
	}
	if !IsMigrationValidationError(err) {
		t.Fatalf("got %T, want MigrationValidationError", err)
	}
}

func TestValidateMigrationSet_AddConstraintRequiresNotValidPerStatement(t *testing.T) {
	migrations := []Migration{
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/expand/001_constraints.sql",
			Transactional: true,
			content: `ALTER TABLE purser.a ADD CONSTRAINT a_fk FOREIGN KEY (tenant_id) REFERENCES purser.tenants(id) NOT VALID;
ALTER TABLE purser.b ADD CONSTRAINT b_fk FOREIGN KEY (tenant_id) REFERENCES purser.tenants(id);`,
		},
	}
	err := validatePostgresMigrationSet(migrations)
	if err == nil {
		t.Fatal("expected validation error for validated ADD CONSTRAINT in expand")
	}
	if !IsMigrationValidationError(err) {
		t.Fatalf("got %T, want MigrationValidationError", err)
	}
	if !strings.Contains(err.Error(), `new constraint "b_fk"`) {
		t.Fatalf("validation error = %v, want the unsafe constraint name", err)
	}
}

func TestValidateMigrationSet_NotValidConstraintRequiresPostdeployValidation(t *testing.T) {
	migrations := []Migration{
		{
			Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
			Path: "migrations/purser/v0.3.0/expand/001_constraints.sql", Transactional: true,
			content: `ALTER TABLE purser.a ADD CONSTRAINT a_fk FOREIGN KEY (tenant_id) REFERENCES purser.tenants(id) NOT VALID;`,
		},
	}
	err := validatePostgresMigrationSet(migrations)
	if err == nil || !strings.Contains(err.Error(), `NOT VALID constraint "a_fk" requires a same-release postdeploy`) {
		t.Fatalf("validation error = %v, want missing postdeploy validation", err)
	}
}

func TestValidateMigrationSet_NotValidConstraintWithPostdeployValidation(t *testing.T) {
	migrations := []Migration{
		{
			Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
			Path: "migrations/purser/v0.3.0/expand/001_constraints.sql", Transactional: true,
			content: `DO $$ BEGIN ALTER TABLE purser.a ADD CONSTRAINT a_fk CHECK (tenant_id IS NOT NULL) NOT VALID; END $$;`,
		},
		{
			Database: "purser", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
			Path: "migrations/purser/v0.3.0/postdeploy/001_validate.sql", Transactional: true,
			content: `ALTER TABLE purser.a VALIDATE CONSTRAINT a_fk;`,
		},
	}
	if err := validatePostgresMigrationSet(migrations); err != nil {
		t.Fatalf("validatePostgresMigrationSet rejected paired constraint validation: %v", err)
	}
}

func TestValidateMigrationSet_InlineAddedColumnConstraint(t *testing.T) {
	for _, definition := range []string{
		`ADD COLUMN samples INT[] DEFAULT ARRAY[1,2] CHECK (cardinality(samples) > 0)`,
		`ADD COLUMN samples INT[][] DEFAULT ARRAY[[1,2],[3,4]] CHECK (cardinality(samples) > 0)`,
		`ADD CONSTRAINT safe CHECK (true) NOT VALID, ADD COLUMN samples INT[] DEFAULT ARRAY[1,2] CHECK (cardinality(samples) > 0)`,
		`ADD COLUMN reason TEXT CHECK (reason <> '')`,
		`ADD COLUMN reason TEXT CONSTRAINT reason_valid CHECK (reason <> '')`,
		`ADD COLUMN owner UUID REFERENCES purser.owners(id)`,
		`ADD CONSTRAINT safe CHECK (true) NOT VALID, ADD COLUMN owner UUID REFERENCES purser.owners(id)`,
		`ADD COLUMN owner UUID REFERENCES purser.owners(id), ADD CONSTRAINT safe CHECK (true) NOT VALID`,
		`ADD CHECK (true) NOT VALID, ADD COLUMN owner UUID REFERENCES purser.owners(id)`,
		`ADD COLUMN amount NUMERIC(10, 2), ADD CONSTRAINT safe CHECK (amount >= 0) NOT VALID, ADD COLUMN owner UUID REFERENCES purser.owners(id)`,
		`ADD CONSTRAINT safe CHECK (true) NOT VALID, ADD COLUMN "owner,name" UUID REFERENCES purser.owners(id)`,
	} {
		t.Run(definition, func(t *testing.T) {
			err := validatePostgresMigrationSet([]Migration{{Database: "purser", Version: "v0.3.14", Phase: "expand", Sequence: 1, Path: "migrations/purser/v0.3.14/expand/001.sql", Transactional: true, content: "ALTER TABLE purser.a " + definition + ";"}})
			if err == nil || !strings.Contains(err.Error(), "constraints on added columns must be separate named ADD CONSTRAINT") {
				t.Fatalf("inline validated constraint escaped expand safety validation: %v", err)
			}
		})
	}
}

func TestValidateMigrationSet_ExecutableInlineConstraints(t *testing.T) {
	for name, content := range map[string]string{
		"separate statements in DO": `DO $$ BEGIN
ALTER TABLE purser.a ADD CONSTRAINT safe CHECK (true) NOT VALID;
ALTER TABLE purser.a ADD COLUMN owner UUID REFERENCES purser.owners(id);
END $$;`,
		"mixed clauses in DO":                      `DO $body$ BEGIN ALTER TABLE purser.a ADD CONSTRAINT safe CHECK (true) NOT VALID, ADD COLUMN owner UUID REFERENCES purser.owners(id); END $body$;`,
		"dollar data cannot swallow comma":         `ALTER TABLE purser.a ADD CONSTRAINT safe CHECK (reason <> $q$($q$) NOT VALID, ADD COLUMN owner UUID REFERENCES purser.owners(id);`,
		"standard backslash does not escape quote": `ALTER TABLE purser.a ADD CONSTRAINT safe CHECK (reason <> '\') NOT VALID, ADD COLUMN owner UUID REFERENCES purser.owners(id);`,
	} {
		t.Run(name, func(t *testing.T) {
			err := validatePostgresMigrationSet([]Migration{{Database: "purser", Version: "v0.3.14", Phase: "expand", Sequence: 1, Path: "migrations/purser/v0.3.14/expand/001.sql", Transactional: true, content: content}})
			if err == nil || !strings.Contains(err.Error(), "constraints on added columns must be separate named ADD CONSTRAINT") {
				t.Fatalf("executed inline constraint escaped validation: %v", err)
			}
		})
	}
}

func TestValidateMigrationSet_ExecutableConstraintControls(t *testing.T) {
	for _, content := range []string{
		`ALTER TABLE purser.a ADD COLUMN samples INT[] DEFAULT ARRAY[1,2], ADD CONSTRAINT safe CHECK (cardinality(samples) > 0) NOT VALID;`,
		`ALTER TABLE purser.a ADD COLUMN samples INT[][] DEFAULT ARRAY[[1,2],[3,4]], ADD CONSTRAINT safe CHECK (cardinality(samples) > 0) NOT VALID;`,
		`DO $$ BEGIN ALTER TABLE purser.a ADD CONSTRAINT safe CHECK (reason <> $q$($q$) NOT VALID; ALTER TABLE purser.a ADD COLUMN owner UUID; END $$;`,
		`DO $$ BEGIN RAISE NOTICE 'ALTER TABLE purser.a ADD COLUMN owner UUID REFERENCES purser.owners(id)'; ALTER TABLE purser.a ADD CONSTRAINT safe CHECK (true) NOT VALID; END $$;`,
		`ALTER TABLE purser.a ADD CONSTRAINT safe CHECK (reason <> '\') NOT VALID, ADD COLUMN owner UUID;`,
		`ALTER TABLE purser.a ADD CONSTRAINT safe CHECK (reason <> E'\'') NOT VALID, ADD COLUMN owner UUID;`,
	} {
		migrations := []Migration{
			{Database: "purser", Version: "v0.3.14", Phase: "expand", Sequence: 1, Path: "migrations/purser/v0.3.14/expand/001.sql", Transactional: true, content: content},
			{Database: "purser", Version: "v0.3.14", Phase: "postdeploy", Sequence: 1, Path: "migrations/purser/v0.3.14/postdeploy/001.sql", Transactional: true, content: `ALTER TABLE purser.a VALIDATE CONSTRAINT safe;`},
		}
		if err := validatePostgresMigrationSet(migrations); err != nil {
			t.Fatalf("valid staged constraint rejected: %s: %v", content, err)
		}
	}
}

func TestValidateMigrationSet_ConstraintMarkersInCommentsAndStringsDoNotCount(t *testing.T) {
	tests := []struct {
		name       string
		expandSQL  string
		postdeploy string
		want       string
	}{
		{
			name:      "comment cannot mark add as not valid",
			expandSQL: `ALTER TABLE purser.a ADD CONSTRAINT a_fk CHECK (tenant_id IS NOT NULL); -- NOT VALID`,
			want:      `new constraint "a_fk" in expand must be NOT VALID`,
		},
		{
			name:       "string cannot validate constraint",
			expandSQL:  `ALTER TABLE purser.a ADD CONSTRAINT a_fk CHECK (tenant_id IS NOT NULL) NOT VALID;`,
			postdeploy: `DO $$ BEGIN RAISE NOTICE 'VALIDATE CONSTRAINT a_fk'; END $$;`,
			want:       `NOT VALID constraint "a_fk" requires a same-release postdeploy`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			migrations := []Migration{{
				Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
				Path: "migrations/purser/v0.3.0/expand/001.sql", Transactional: true, content: tt.expandSQL,
			}}
			if tt.postdeploy != "" {
				migrations = append(migrations, Migration{
					Database: "purser", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
					Path: "migrations/purser/v0.3.0/postdeploy/001.sql", Transactional: true, content: tt.postdeploy,
				})
			}
			err := validatePostgresMigrationSet(migrations)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateMigrationSet_PostdeployValidationRequiresMatchingExpandConstraint(t *testing.T) {
	migrations := []Migration{{
		Database: "purser", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
		Path: "migrations/purser/v0.3.0/postdeploy/001.sql", Transactional: true,
		content: `ALTER TABLE purser.a VALIDATE CONSTRAINT typo_fk;`,
	}}
	err := validatePostgresMigrationSet(migrations)
	if err == nil || !strings.Contains(err.Error(), `VALIDATE CONSTRAINT "typo_fk" has no same-release`) {
		t.Fatalf("validation error = %v, want orphan validation rejection", err)
	}
}

func TestValidateMigrationSet_PostdeployCanInstallThenValidateConstraint(t *testing.T) {
	migrations := []Migration{
		{
			Database: "commodore", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
			Path: "migrations/commodore/v0.3.0/postdeploy/001_install.sql", Transactional: true,
			content: `ALTER TABLE commodore.push_targets ADD CONSTRAINT platform_check CHECK (platform = lower(platform)) NOT VALID;`,
		},
		{
			Database: "commodore", Version: "v0.3.0", Phase: "postdeploy", Sequence: 2,
			Path: "migrations/commodore/v0.3.0/postdeploy/002_validate.sql", Transactional: true,
			content: `ALTER TABLE commodore.push_targets VALIDATE CONSTRAINT platform_check;`,
		},
	}
	if err := validatePostgresMigrationSet(migrations); err != nil {
		t.Fatalf("postdeploy install/validate split was rejected: %v", err)
	}
}

func TestValidateMigrationSet_PostdeployConstraintCannotPrecedeNormalization(t *testing.T) {
	migrations := []Migration{
		{
			Database: "commodore", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
			Path: "migrations/commodore/v0.3.0/postdeploy/001_install.sql", Transactional: true,
			content: `ALTER TABLE commodore.push_targets ADD CONSTRAINT platform_check CHECK (platform = lower(platform)) NOT VALID;`,
		},
		{
			Database: "commodore", Version: "v0.3.0", Phase: "postdeploy", Sequence: 2,
			Path: "migrations/commodore/v0.3.0/postdeploy/002_normalize.sql", Transactional: true,
			content: `UPDATE commodore.push_targets SET platform = lower(platform); ALTER TABLE commodore.push_targets VALIDATE CONSTRAINT platform_check;`,
		},
	}
	err := validatePostgresMigrationSet(migrations)
	if err == nil || !strings.Contains(err.Error(), "precedes normalization") {
		t.Fatalf("validation error=%v, want install-before-normalization rejection", err)
	}
}

func TestValidateMigrationSet_PostdeployAliasNormalizationIsDetected(t *testing.T) {
	migrations := []Migration{
		{
			Database: "commodore", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
			Path: "migrations/commodore/v0.3.0/postdeploy/001_install.sql", Transactional: true,
			content: `ALTER TABLE commodore.push_targets ADD CONSTRAINT platform_check CHECK (platform = lower(platform)) NOT VALID;`,
		},
		{
			Database: "commodore", Version: "v0.3.0", Phase: "postdeploy", Sequence: 2,
			Path: "migrations/commodore/v0.3.0/postdeploy/002_normalize.sql", Transactional: true,
			content: `UPDATE commodore.push_targets AS target SET platform = lower(target.platform); ALTER TABLE commodore.push_targets VALIDATE CONSTRAINT platform_check;`,
		},
	}
	err := validatePostgresMigrationSet(migrations)
	if err == nil || !strings.Contains(err.Error(), "precedes normalization") {
		t.Fatalf("validation error=%v, want aliased normalization ordering rejection", err)
	}
}

func TestValidateMigrationSet_PostdeployValidationCannotPrecedeInstall(t *testing.T) {
	migrations := []Migration{
		{
			Database: "commodore", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
			Path: "migrations/commodore/v0.3.0/postdeploy/001_validate.sql", Transactional: true,
			content: `ALTER TABLE commodore.push_targets VALIDATE CONSTRAINT platform_check;`,
		},
		{
			Database: "commodore", Version: "v0.3.0", Phase: "postdeploy", Sequence: 2,
			Path: "migrations/commodore/v0.3.0/postdeploy/002_install.sql", Transactional: true,
			content: `ALTER TABLE commodore.push_targets ADD CONSTRAINT platform_check CHECK (platform = lower(platform)) NOT VALID;`,
		},
	}
	err := validatePostgresMigrationSet(migrations)
	if err == nil || !strings.Contains(err.Error(), "precedes its ADD CONSTRAINT") {
		t.Fatalf("validation error=%v, want reversed postdeploy ordering rejection", err)
	}
}

func TestValidateMigrationSet_SameFileValidationCannotPrecedeInstall(t *testing.T) {
	migrations := []Migration{{
		Database: "commodore", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
		Path: "migrations/commodore/v0.3.0/postdeploy/001_constraint.sql", Transactional: true,
		content: `ALTER TABLE commodore.push_targets VALIDATE CONSTRAINT platform_check;
			ALTER TABLE commodore.push_targets ADD CONSTRAINT platform_check CHECK (platform = lower(platform)) NOT VALID;`,
	}}
	err := validatePostgresMigrationSet(migrations)
	if err == nil || !strings.Contains(err.Error(), "precedes its ADD CONSTRAINT") {
		t.Fatalf("validation error=%v, want same-file ordering rejection", err)
	}
}

func TestValidateMigrationSet_ContractValidationDoesNotSatisfyExpand(t *testing.T) {
	migrations := []Migration{
		{
			Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
			Path: "migrations/purser/v0.3.0/expand/001.sql", Transactional: true,
			content: `ALTER TABLE purser.a ADD CONSTRAINT a_fk CHECK (tenant_id IS NOT NULL) NOT VALID;`,
		},
		{
			Database: "purser", Version: "v0.3.0", Phase: "contract", Sequence: 1,
			Path: "migrations/purser/v0.3.0/contract/001.sql", Transactional: true,
			content: `ALTER TABLE purser.a VALIDATE CONSTRAINT a_fk;`,
		},
	}
	err := validatePostgresMigrationSet(migrations)
	if err == nil || !strings.Contains(err.Error(), `requires a same-release postdeploy`) {
		t.Fatalf("validation error = %v, want contract validation rejection", err)
	}
}

func TestValidateMigrationSet_SameReleaseDropDoesNotRequireValidation(t *testing.T) {
	migrations := []Migration{
		{
			Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
			Path: "migrations/purser/v0.3.0/expand/001.sql", Transactional: true,
			content: `ALTER TABLE purser.a ADD CONSTRAINT compatibility_check CHECK (tenant_id IS NOT NULL) NOT VALID;`,
		},
		{
			Database: "purser", Version: "v0.3.0", Phase: "contract", Sequence: 1,
			Path: "migrations/purser/v0.3.0/contract/001.sql", Transactional: true,
			content: `ALTER TABLE purser.a DROP CONSTRAINT IF EXISTS compatibility_check;`,
		},
	}
	if err := validatePostgresMigrationSet(migrations); err != nil {
		t.Fatalf("same-release compatibility constraint drop rejected: %v", err)
	}
}

func TestValidateMigrationSet_DollarQuoteBlockNotMisparsed(t *testing.T) {
	migrations := []Migration{
		{
			Database:      "purser",
			Version:       "v0.3.0",
			Phase:         "expand",
			Sequence:      1,
			Path:          "migrations/purser/v0.3.0/expand/001_doblock.sql",
			Transactional: true,
			content: `DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'foo_idx') THEN
    EXECUTE 'CREATE INDEX foo_idx ON foo (id);';
  END IF;
END $$;`,
		},
	}
	if err := validatePostgresMigrationSet(migrations); err != nil {
		t.Fatalf("validatePostgresMigrationSet rejected a valid DO $$ block: %v", err)
	}
}

func TestValidateMigrationSet_NotxGuardCannotComeFromStringLiteral(t *testing.T) {
	migrations := []Migration{
		{
			Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
			Path: "migrations/purser/v0.3.0/expand/001_idx.notx.sql", Transactional: false,
			content: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x ON purser.t (id);`,
		},
		{
			Database: "purser", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
			Path: "migrations/purser/v0.3.0/postdeploy/001.sql", Transactional: true,
			content: `SELECT 'idx_x pg_index indisvalid indisready';`,
		},
	}
	if err := validatePostgresMigrationSet(migrations); err == nil || !strings.Contains(err.Error(), "postdeploy pg_index guard") {
		t.Fatalf("validation error = %v, want literal-only guard rejection", err)
	}
}

func TestValidateMigrationSet_RejectsAnonymousExpandConstraint(t *testing.T) {
	for _, sql := range []string{
		`ALTER TABLE purser.t ADD CHECK (amount >= 0) NOT VALID;`,
		`ALTER TABLE purser.t ADD FOREIGN KEY (tenant_id) REFERENCES purser.tenants(id) NOT VALID;`,
	} {
		migration := Migration{
			Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
			Path: "migrations/purser/v0.3.0/expand/001.sql", Transactional: true, content: sql,
		}
		if err := validatePostgresMigrationSet([]Migration{migration}); err == nil || !strings.Contains(err.Error(), "explicit CONSTRAINT name") {
			t.Fatalf("validation error = %v, want anonymous-constraint rejection", err)
		}
	}
}

func TestValidateMigrationSet_DollarBodyQuoteCannotHideFollowingDDL(t *testing.T) {
	migrations := []Migration{
		{
			Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
			Path: "migrations/purser/v0.3.0/expand/001.sql", Transactional: true,
			content: `ALTER TABLE purser.t ADD CONSTRAINT amount_check CHECK (amount >= 0) NOT VALID;`,
		},
		{
			Database: "purser", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
			Path: "migrations/purser/v0.3.0/postdeploy/001.sql", Transactional: true,
			content: `DO $body$ BEGIN RAISE NOTICE 'language-body quote; END $body$;
ALTER TABLE purser.t VALIDATE CONSTRAINT amount_check;`,
		},
	}
	if err := validatePostgresMigrationSet(migrations); err != nil {
		t.Fatalf("DDL following dollar body was hidden by a body-local quote: %v", err)
	}
}

func TestValidateMigrationSet_CommentMarkerInsideDollarBodyStringCannotHideDDL(t *testing.T) {
	migrations := []Migration{
		{
			Database: "purser", Version: "v0.3.0", Phase: "expand", Sequence: 1,
			Path: "migrations/purser/v0.3.0/expand/001.sql", Transactional: true,
			content: `ALTER TABLE purser.t ADD CONSTRAINT amount_check CHECK (amount >= 0) NOT VALID;`,
		},
		{
			Database: "purser", Version: "v0.3.0", Phase: "postdeploy", Sequence: 1,
			Path: "migrations/purser/v0.3.0/postdeploy/001.sql", Transactional: true,
			content: `DO $body$ BEGIN RAISE NOTICE '-- diagnostic'; ALTER TABLE purser.t VALIDATE CONSTRAINT amount_check; END $body$;`,
		},
	}
	if err := validatePostgresMigrationSet(migrations); err != nil {
		t.Fatalf("comment marker inside body string hid following DDL: %v", err)
	}
}

func TestValidateMigrationSet_RejectsContractValidationEvenWhenExpandIsValidated(t *testing.T) {
	migration := Migration{
		Database: "purser", Version: "v0.3.0", Phase: "contract", Sequence: 1,
		Path: "migrations/purser/v0.3.0/contract/001.sql", Transactional: true,
		content: `ALTER TABLE purser.t VALIDATE CONSTRAINT amount_check;`,
	}
	if err := validatePostgresMigrationSet([]Migration{migration}); err == nil || !strings.Contains(err.Error(), "belongs in postdeploy") {
		t.Fatalf("validation error = %v, want contract-phase validation rejection", err)
	}
}

func TestExpandMigrationSeparatesStoredRoutineDefinitionFromExecution(t *testing.T) {
	routine := `CREATE OR REPLACE FUNCTION purser.fence() RETURNS TRIGGER LANGUAGE plpgsql AS $body$
 BEGIN UPDATE purser.billing_tiers SET updated_at=clock_timestamp() WHERE id=NEW.fallback_tier_id; RETURN NEW; END;
 $body$;`
	registered := routine + `CREATE OR REPLACE TRIGGER fallback_fence BEFORE INSERT ON purser.subscription_operator_grants FOR EACH ROW EXECUTE FUNCTION purser.fence();`
	function := `CREATE OR REPLACE FUNCTION purser.rewrite() RETURNS void LANGUAGE SQL AS $$UPDATE purser.billing_tiers SET updated_at=NOW();$$;`
	procedure := `CREATE OR REPLACE PROCEDURE purser.touch_tiers() LANGUAGE SQL AS $$UPDATE purser.billing_tiers SET updated_at=NOW();$$;`
	for _, test := range []struct {
		name, sql string
		wantError bool
	}{
		{"registered trigger function", registered, false},
		{"identity table alongside registered trigger", registered + `CREATE TABLE purser.ids(id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY);`, false},
		{"table-returning routine alongside registered trigger", registered + `CREATE FUNCTION purser.ids() RETURNS TABLE(id BIGINT) LANGUAGE SQL AS $$SELECT 1::bigint$$;`, false},
		{"unlogged fresh table initialization", `CREATE UNLOGGED TABLE purser.fresh(id BIGINT PRIMARY KEY); INSERT INTO purser.fresh VALUES(1) ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id;`, false},
		{"unlogged if-not-exists table is not fresh", `CREATE UNLOGGED TABLE IF NOT EXISTS purser.fresh(id BIGINT PRIMARY KEY); INSERT INTO purser.fresh VALUES(1) ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id;`, true},
		{"literal seed cannot rewrite live rows", `INSERT INTO purser.meter_definitions(meter,allowed_dimensions) VALUES('x','{kind}') ON CONFLICT(meter) DO UPDATE SET allowed_dimensions=EXCLUDED.allowed_dimensions;`, true},
		{"new destination does not hide source merge", `CREATE TABLE purser.new_tiers(id BIGINT); WITH changed AS (MERGE INTO purser.billing_tiers t USING purser.copy c ON t.id=c.id WHEN MATCHED THEN DELETE RETURNING t.id) INSERT INTO purser.new_tiers SELECT id FROM changed ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id;`, true},
		{"unbounded upsert", `INSERT INTO purser.billing_tiers SELECT * FROM purser.copy ON CONFLICT(tier_name) DO UPDATE SET base_price=EXCLUDED.base_price;`, true},
		{"seed initialized table", `CREATE TABLE purser.counters(id BIGINT PRIMARY KEY,value BIGINT); INSERT INTO purser.counters SELECT id,MAX(value) FROM purser.old_counters GROUP BY id ON CONFLICT(id) DO UPDATE SET value=GREATEST(purser.counters.value,EXCLUDED.value);`, false},
		{"existing table declaration is not initialization", `CREATE TABLE IF NOT EXISTS purser.billing_tiers(tier_name TEXT,base_price NUMERIC); INSERT INTO purser.billing_tiers VALUES('pro',0) ON CONFLICT(tier_name) DO UPDATE SET base_price=EXCLUDED.base_price;`, true},
		{"upsert CTE does not hide following merge", `CREATE TABLE purser.new_tiers(id BIGINT PRIMARY KEY); WITH seeded AS (INSERT INTO purser.new_tiers VALUES(1) ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id RETURNING id) MERGE INTO purser.billing_tiers t USING seeded s ON t.id=s.id WHEN MATCHED THEN DELETE;`, true},
		{"upsert rewrites rows", `INSERT INTO purser.billing_tiers(tier_name) VALUES('x') ON CONFLICT(tier_name) DO UPDATE SET base_price=0;`, true},
		{"merge updates rows", `MERGE INTO purser.billing_tiers AS t USING purser.copy AS c ON t.id=c.id WHEN MATCHED THEN UPDATE SET base_price=0;`, true},
		{"merge deletes rows", `MERGE INTO purser.billing_tiers AS t USING purser.copy AS c ON t.id=c.id WHEN MATCHED THEN DELETE;`, true},
		{"truncate rows", `TRUNCATE TABLE purser.billing_tiers;`, true},
		{"execute literal rewrite", `DO $$BEGIN EXECUTE 'UPDATE purser.billing_tiers SET base_price=0'; END;$$;`, true},
		{"CTAS fires registered trigger", registered + `CREATE TABLE purser.copy AS WITH w AS (INSERT INTO purser.subscription_operator_grants DEFAULT VALUES RETURNING *) SELECT * FROM w;`, true},
		{"materialized view executes a trigger", registered + `CREATE MATERIALIZED VIEW purser.copy AS WITH w AS (INSERT INTO purser.subscription_operator_grants DEFAULT VALUES RETURNING *) SELECT * FROM w;`, true},
		{"SELECT INTO fires trigger", registered + `WITH w AS (INSERT INTO purser.subscription_operator_grants DEFAULT VALUES RETURNING *) SELECT * INTO purser.copy FROM w;`, true},
		{"single quoted function called", `CREATE FUNCTION purser.rewrite() RETURNS void LANGUAGE SQL AS 'UPDATE purser.billing_tiers SET base_price=0'; SELECT purser.rewrite();`, true},
		{"single quoted DO", `DO 'BEGIN UPDATE purser.billing_tiers SET base_price=0; END';`, true},
		{"quoted update table", `UPDATE "purser"."billing_tiers" SET base_price=0;`, true},
		{"quoted delete table", `DELETE FROM "purser"."billing_tiers";`, true},
		{"diagnostic string", `DO 'BEGIN RAISE NOTICE ''UPDATE "purser"."billing_tiers" SET base_price=0''; END';`, false},
		{"unregistered trigger function", routine, false},
		{"stored procedure", procedure, false},
		{"stored admission function", function, false},
		{"routine invoked by column default", function + `ALTER TABLE purser.billing_tiers ADD COLUMN admission_result text DEFAULT purser.rewrite();`, true},
		{"defined then selected", function + `SELECT purser.rewrite();`, true},
		{"defined then called", procedure + `CALL purser.touch_tiers();`, true},
		{"defined then called in DO", function + `DO $$BEGIN PERFORM purser.rewrite(); END;$$;`, true},
		{"trigger fired during expansion", registered + `INSERT INTO purser.subscription_operator_grants DEFAULT VALUES;`, true},
		{"executed DO", `DO $$BEGIN UPDATE purser.billing_tiers SET updated_at=NOW(); END;$$;`, true},
		{"rewrite after definition", registered + `UPDATE purser.billing_tiers SET updated_at=NOW();`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validatePostgresMigrationSet([]Migration{{Database: "purser", Version: "v0.3.14", Phase: "expand", Sequence: 1, Path: "migrations/purser/v0.3.14/expand/001_fence.sql", Transactional: true, content: test.sql}})
			if (err != nil) != test.wantError {
				t.Fatalf("migration error=%v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestShippedUpsertExceptionsAreExactAndCannotMove(t *testing.T) {
	migrations, err := discoverAllPostgresMigrationsForValidation()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, m := range migrations {
		if _, ok := shippedExpandUpserts[m.Path]; !ok {
			continue
		}
		seen++
		if !immutableShippedUpsert(m) {
			t.Fatalf("shipped checksum changed: %s", m.Path)
		}
		altered := m
		altered.content += " INSERT INTO purser.billing_tiers(tier_name) VALUES('free') ON CONFLICT(tier_name) DO UPDATE SET base_price=0;"
		if immutableShippedUpsert(altered) {
			t.Fatal("modified shipped file retained exemption")
		}
		moved := m
		moved.Path = "migrations/purser/v0.3.14/expand/999_copy.sql"
		if immutableShippedUpsert(moved) {
			t.Fatal("copy to a new release retained exemption")
		}
	}
	if seen != len(shippedExpandUpserts) {
		t.Fatalf("found %d pinned files, want %d", seen, len(shippedExpandUpserts))
	}
}

func TestShippedInlineConstraintExceptionsAreExactAndCannotMove(t *testing.T) {
	migrations, err := discoverAllPostgresMigrationsForValidation()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, m := range migrations {
		if _, ok := shippedInlineConstraints[m.Path]; !ok {
			continue
		}
		seen++
		if !immutableShippedInlineConstraint(m) {
			t.Fatalf("shipped checksum changed: %s", m.Path)
		}
		altered := m
		altered.content += " INSERT INTO purser.billing_tiers(tier_name) VALUES('free') ON CONFLICT(tier_name) DO UPDATE SET base_price=0;"
		if immutableShippedInlineConstraint(altered) {
			t.Fatal("modified shipped file retained exemption")
		}
		moved := m
		moved.Path = "migrations/purser/v0.3.14/expand/999_copy.sql"
		if immutableShippedInlineConstraint(moved) {
			t.Fatal("copy to a new release retained exemption")
		}
	}
	if seen != len(shippedInlineConstraints) {
		t.Fatalf("found %d pinned files, want %d", seen, len(shippedInlineConstraints))
	}
}
