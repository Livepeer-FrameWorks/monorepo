//go:build schema_verify

package provisioner

import (
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// TestPostgresDemoSeedAppliesToCurrentBaseline protects the local first-boot
// path. Applying the seed twice also proves that every demo upsert remains
// compatible with the current unique constraints.
func TestPostgresDemoSeedAppliesToCurrentBaseline(t *testing.T) {
	requireDocker(t)

	const name = "fw-demo-seed-pg"
	pgStart(t, name)
	pgCreateDB(t, name, "frameworks_demo")

	for _, file := range pgBaselineFiles(t) {
		schemaSQL, err := dbsql.Content.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		pgApply(t, name, "frameworks_demo", string(schemaSQL))
	}

	for _, database := range []string{"quartermaster", "purser", "commodore", "foghorn", "periscope"} {
		path := demoSeeds[database]
		demoSQL, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s demo seed: %v", database, err)
		}
		pgApply(t, name, "frameworks_demo", string(demoSQL))
		pgApply(t, name, "frameworks_demo", string(demoSQL))
	}
}
