//go:build schema_verify

package provisioner

import (
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// TestPostgresDemoSeedAppliesToCurrentBaseline proves each service-owned seed
// works with only that service's baseline. Applying it twice also proves every
// demo upsert remains compatible with the current unique constraints.
func TestPostgresDemoSeedAppliesToCurrentBaseline(t *testing.T) {
	requireDocker(t)

	const name = "fw-demo-seed-pg"
	pgStart(t, name)

	for _, database := range []string{"quartermaster", "purser", "commodore", "foghorn", "periscope"} {
		database := database
		t.Run(database, func(t *testing.T) {
			dbName := "demo_" + database
			pgCreateDB(t, name, dbName)

			schemaPath := "schema/" + database + ".sql"
			schemaSQL, err := dbsql.Content.ReadFile(schemaPath)
			if err != nil {
				t.Fatalf("read %s: %v", schemaPath, err)
			}
			pgApply(t, name, dbName, string(schemaSQL))

			seedPath := demoSeeds[database]
			demoSQL, err := dbsql.Content.ReadFile(seedPath)
			if err != nil {
				t.Fatalf("read %s: %v", seedPath, err)
			}
			pgApply(t, name, dbName, string(demoSQL))
			pgApply(t, name, dbName, string(demoSQL))
		})
	}
}
