package provisioner

import (
	"fmt"

	dbsql "frameworks/pkg/database/sql"
)

// BuildPostgresSeedItems materializes the static or demo seed SQL as role
// variables for the frameworks.infra.postgres (and frameworks.infra.yugabyte)
// tasks/seed.yml path.
//
// kind: "static" | "demo". dbNames is the manifest-provided list of
// databases to filter by — embedded seeds for databases not in that list
// are skipped so partial deployments don't fail.
//
// Returns a list of {db, sql} entries ready to drop into
// ServiceConfig.Metadata as "postgres_seed_items" or "yugabyte_seed_items".
func BuildPostgresSeedItems(kind string, dbNames []string) ([]map[string]any, error) {
	source, err := pickPostgresSeedMap(kind)
	if err != nil {
		return nil, err
	}
	want := dbSet(dbNames)
	items := make([]map[string]any, 0, len(source))
	for db, path := range source {
		if _, ok := want[db]; !ok {
			continue
		}
		content, err := dbsql.Content.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s seed for %s (%s): %w", kind, db, path, err)
		}
		items = append(items, map[string]any{
			"db":  db,
			"sql": string(content),
		})
	}
	return items, nil
}

func pickPostgresSeedMap(kind string) (map[string]string, error) {
	switch kind {
	case "static":
		return staticSeeds, nil
	case "demo":
		return demoSeeds, nil
	default:
		return nil, fmt.Errorf("unknown postgres seed kind %q (want static or demo)", kind)
	}
}

// BuildClickHouseDemoSeedItems returns the single demo-fixture item the
// frameworks.infra.clickhouse tasks/seed.yml path expects. Each entry is
// {name, database, sql}. ClickHouse currently only ships a demo seed;
// extend this if static seeds land later.
func BuildClickHouseDemoSeedItems() ([]map[string]any, error) {
	content, err := dbsql.Content.ReadFile("seeds/demo/clickhouse_demo_data.sql")
	if err != nil {
		return nil, fmt.Errorf("read clickhouse demo seed: %w", err)
	}
	return []map[string]any{{
		"name":     "periscope-demo",
		"database": "periscope",
		"sql":      string(content),
	}}, nil
}
