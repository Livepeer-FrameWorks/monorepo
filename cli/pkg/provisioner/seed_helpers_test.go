package provisioner

import (
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// Every seed path the provisioner references must exist in the embedded FS.
// The seed maps and embed directives are maintained independently, so this
// guards cluster seed against referencing SQL files that are present on disk
// but absent from the compiled CLI.
func TestSeedPathsAreEmbedded(t *testing.T) {
	paths := make(map[string]string)
	for db, path := range staticSeeds {
		paths["static/"+db] = path
	}
	for db, path := range demoSeeds {
		paths["demo/"+db] = path
	}
	paths["clickhouse-demo"] = "seeds/demo/clickhouse_demo_data.sql"

	for name, path := range paths {
		content, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Errorf("%s: embedded seed %s unreadable: %v", name, path, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("%s: embedded seed %s is empty", name, path)
		}
	}
}

func TestBuildPostgresSeedItemsStatic(t *testing.T) {
	items, err := BuildPostgresSeedItems("static", []string{"quartermaster", "commodore", "purser"})
	if err != nil {
		t.Fatalf("BuildPostgresSeedItems(static) returned error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	for _, item := range items {
		if item["sql"] == "" {
			t.Errorf("seed item for %v has empty sql", item["db"])
		}
	}
}

func TestBuildPostgresSeedItemsFiltersToManifestDatabases(t *testing.T) {
	items, err := BuildPostgresSeedItems("static", []string{"purser"})
	if err != nil {
		t.Fatalf("BuildPostgresSeedItems(static, purser) returned error: %v", err)
	}
	if len(items) != 1 || items[0]["db"] != "purser" {
		t.Fatalf("items = %v, want exactly the purser seed", items)
	}
}
