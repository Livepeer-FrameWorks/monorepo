package provisioner

import (
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

	err := validateMigrationSet(migrations)
	if err == nil {
		t.Fatal("validateMigrationSet returned nil error")
	}
	if !IsMigrationValidationError(err) {
		t.Fatalf("validateMigrationSet error type = %T, want MigrationValidationError", err)
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
			content:       "ALTER TABLE purser.billing_invoices ALTER COLUMN rating_version SET NOT NULL;",
		},
	}

	if err := validateMigrationSet(migrations); err != nil {
		t.Fatalf("validateMigrationSet returned error: %v", err)
	}
}

func TestValidateEmbeddedMigrations(t *testing.T) {
	if err := ValidateEmbeddedMigrations(); err != nil {
		t.Fatalf("ValidateEmbeddedMigrations returned error: %v", err)
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
	err := validateMigrationSet(migrations)
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
	}
	if err := validateMigrationSet(migrations); err != nil {
		t.Fatalf("validateMigrationSet returned error: %v", err)
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
	err := validateMigrationSet(migrations)
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
	err := validateMigrationSet(migrations)
	if err == nil {
		t.Fatal("expected validation error for validated ADD CONSTRAINT in expand")
	}
	if !IsMigrationValidationError(err) {
		t.Fatalf("got %T, want MigrationValidationError", err)
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
	if err := validateMigrationSet(migrations); err != nil {
		t.Fatalf("validateMigrationSet rejected a valid DO $$ block: %v", err)
	}
}
