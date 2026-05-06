package provisioner

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestDiscoverClickHouseMigrationsLayout(t *testing.T) {
	fsys := fstest.MapFS{
		"clickhouse/migrations/periscope/v0.2.31/expand/001_add_table.sql":     {Data: []byte("CREATE TABLE IF NOT EXISTS periscope.example (id UUID) ENGINE = MergeTree ORDER BY id;")},
		"clickhouse/migrations/periscope/v0.2.31/expand/002_add_column.sql":    {Data: []byte("ALTER TABLE periscope.example ADD COLUMN IF NOT EXISTS name String;")},
		"clickhouse/migrations/periscope/v0.2.31/postdeploy/001_rebuild.sql":   {Data: []byte("ALTER TABLE periscope.example UPDATE name = '' WHERE 1;")},
		"clickhouse/migrations/periscope/v0.2.32/contract/001_drop_legacy.sql": {Data: []byte("ALTER TABLE periscope.example DROP COLUMN legacy;")},
	}

	got, err := discoverMigrationsInFS(fsys, "clickhouse/migrations", map[string]bool{"periscope": true})
	if err != nil {
		t.Fatalf("discoverMigrationsInFS returned error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4", len(got))
	}
	if got[0].Database != "periscope" || got[0].Version != "v0.2.31" || got[0].Phase != "expand" || got[0].Sequence != 1 {
		t.Fatalf("got[0] = %#v", got[0])
	}
	if got[3].Phase != "contract" {
		t.Fatalf("got[3].Phase = %q, want contract", got[3].Phase)
	}
}

func TestValidateClickHouseMigrationSetRequiresIfNotExists(t *testing.T) {
	migrations := []Migration{
		{
			Database: "periscope",
			Version:  "v0.2.32",
			Phase:    "expand",
			Sequence: 1,
			Path:     "clickhouse/migrations/periscope/v0.2.32/expand/001_bad.sql",
			content:  "CREATE TABLE periscope.foo (id UUID) ENGINE = MergeTree ORDER BY id;",
		},
	}
	err := validateClickHouseMigrationSet(migrations)
	if err == nil {
		t.Fatal("expected validation error for CREATE TABLE without IF NOT EXISTS")
	}
	if !IsMigrationValidationError(err) {
		t.Fatalf("got %T, want MigrationValidationError", err)
	}
	if !strings.Contains(err.Error(), "IF NOT EXISTS") {
		t.Fatalf("error message missing IF NOT EXISTS hint: %v", err)
	}
}

func TestValidateClickHouseMigrationSetRejectsDropInExpand(t *testing.T) {
	migrations := []Migration{
		{
			Database: "periscope",
			Version:  "v0.2.32",
			Phase:    "expand",
			Sequence: 1,
			Path:     "clickhouse/migrations/periscope/v0.2.32/expand/001_bad.sql",
			content:  "DROP TABLE periscope.example;",
		},
	}
	err := validateClickHouseMigrationSet(migrations)
	if err == nil {
		t.Fatal("expected validation error for DROP in expand")
	}
	if !IsMigrationValidationError(err) {
		t.Fatalf("got %T, want MigrationValidationError", err)
	}
}

func TestValidateClickHouseMigrationSetRejectsRenameInExpand(t *testing.T) {
	migrations := []Migration{
		{
			Database: "periscope",
			Version:  "v0.2.32",
			Phase:    "expand",
			Sequence: 1,
			Path:     "clickhouse/migrations/periscope/v0.2.32/expand/001_bad.sql",
			content:  "RENAME TABLE periscope.a TO periscope.b;",
		},
	}
	if err := validateClickHouseMigrationSet(migrations); err == nil {
		t.Fatal("expected validation error for RENAME in expand")
	}
}

func TestValidateClickHouseMigrationSetRejectsModifyTypeInExpand(t *testing.T) {
	migrations := []Migration{
		{
			Database: "periscope",
			Version:  "v0.2.32",
			Phase:    "expand",
			Sequence: 1,
			Path:     "clickhouse/migrations/periscope/v0.2.32/expand/001_bad.sql",
			content:  "ALTER TABLE periscope.example MODIFY COLUMN name LowCardinality(String);",
		},
	}
	if err := validateClickHouseMigrationSet(migrations); err == nil {
		t.Fatal("expected validation error for MODIFY COLUMN in expand")
	}
}

func TestValidateClickHouseMigrationSetRejectsMutationInExpand(t *testing.T) {
	migrations := []Migration{
		{
			Database: "periscope",
			Version:  "v0.2.32",
			Phase:    "expand",
			Sequence: 1,
			Path:     "clickhouse/migrations/periscope/v0.2.32/expand/001_bad.sql",
			content:  "ALTER TABLE periscope.example UPDATE name = '' WHERE 1;",
		},
	}
	if err := validateClickHouseMigrationSet(migrations); err == nil {
		t.Fatal("expected validation error for ALTER UPDATE in expand")
	}
}

func TestValidateClickHouseMigrationSetAcceptsSafeExpand(t *testing.T) {
	migrations := []Migration{
		{
			Database: "periscope",
			Version:  "v0.2.32",
			Phase:    "expand",
			Sequence: 1,
			Path:     "clickhouse/migrations/periscope/v0.2.32/expand/001_ok.sql",
			content:  "CREATE TABLE IF NOT EXISTS periscope.foo (id UUID) ENGINE = MergeTree ORDER BY id;\nALTER TABLE periscope.example ADD COLUMN IF NOT EXISTS name Nullable(String);",
		},
	}
	if err := validateClickHouseMigrationSet(migrations); err != nil {
		t.Fatalf("validateClickHouseMigrationSet rejected a safe expand: %v", err)
	}
}

func TestValidateClickHouseMigrationSetAcceptsContractDrop(t *testing.T) {
	migrations := []Migration{
		{
			Database: "periscope",
			Version:  "v0.2.32",
			Phase:    "contract",
			Sequence: 1,
			Path:     "clickhouse/migrations/periscope/v0.2.32/contract/001_drop.sql",
			content:  "DROP TABLE periscope.legacy;",
		},
	}
	if err := validateClickHouseMigrationSet(migrations); err != nil {
		t.Fatalf("validateClickHouseMigrationSet rejected contract DROP: %v", err)
	}
}

func TestValidateClickHouseMigrationSetAcceptsPostdeployMutation(t *testing.T) {
	migrations := []Migration{
		{
			Database: "periscope",
			Version:  "v0.2.32",
			Phase:    "postdeploy",
			Sequence: 1,
			Path:     "clickhouse/migrations/periscope/v0.2.32/postdeploy/001_rebuild.sql",
			content:  "ALTER TABLE periscope.example UPDATE name = '' WHERE name IS NULL;",
		},
	}
	if err := validateClickHouseMigrationSet(migrations); err != nil {
		t.Fatalf("validateClickHouseMigrationSet rejected postdeploy mutation: %v", err)
	}
}

func TestBuildClickHouseSchemaItemsSkipsMissingDatabases(t *testing.T) {
	items, err := BuildClickHouseSchemaItems([]string{"periscope", "nonexistent"})
	if err != nil {
		t.Fatalf("BuildClickHouseSchemaItems returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1 (only periscope baseline ships)", len(items))
	}
	if items[0]["database"] != "periscope" {
		t.Fatalf("items[0].database = %v, want periscope", items[0]["database"])
	}
	if !strings.Contains(items[0]["sql"].(string), "CREATE") {
		t.Fatal("items[0].sql does not contain CREATE — embedded baseline empty?")
	}
}

func TestBuildClickHouseMigrationItemsRequiresTargetVersion(t *testing.T) {
	if _, err := BuildClickHouseMigrationItems([]string{"periscope"}, "expand", ""); err == nil {
		t.Fatal("expected error for empty targetVersion")
	}
}

func TestBuildClickHouseMigrationItemsFiltersByVersion(t *testing.T) {
	// v0.2.31 has the existing 001_orchestrator_visibility migration; capping
	// at a lower version filters it out.
	items, err := BuildClickHouseMigrationItems([]string{"periscope"}, "expand", "v0.0.1")
	if err != nil {
		t.Fatalf("BuildClickHouseMigrationItems returned error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items at v0.0.1 cap, got %d", len(items))
	}
}
