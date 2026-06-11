package provisioner

import (
	"fmt"
	"strings"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
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

// AnalyticsRODatabase returns the first manifest database that carries an
// analytics_ro static seed, or "" when the manifest defines none of them.
// Used to decide where (and whether) to apply the role's password.
func AnalyticsRODatabase(dbNames []string) string {
	for _, db := range dbNames {
		if path, ok := staticSeeds[db]; ok && strings.Contains(path, "analytics_ro") {
			return db
		}
	}
	return ""
}

// BuildAnalyticsROPasswordItem returns the seed item that sets the
// frameworks_analytics_ro password. The role itself is created (idempotently,
// without a password) by the analytics_ro_*.sql static seeds; the password
// comes from manifest env so it never lands in an embedded SQL file.
func BuildAnalyticsROPasswordItem(db, password string) map[string]any {
	quoted := strings.ReplaceAll(password, "'", "''")
	return map[string]any{
		"db":  db,
		"sql": fmt.Sprintf("ALTER ROLE frameworks_analytics_ro WITH LOGIN PASSWORD '%s'", quoted),
	}
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
