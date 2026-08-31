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
	if err == nil || !strings.Contains(err.Error(), `VALIDATE CONSTRAINT "typo_fk" has no same-release expand`) {
		t.Fatalf("validation error = %v, want orphan validation rejection", err)
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
