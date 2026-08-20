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

	demoSQL, err := dbsql.Content.ReadFile("seeds/demo/demo_data.sql")
	if err != nil {
		t.Fatalf("read demo seed: %v", err)
	}
	pgApply(t, name, "frameworks_demo", string(demoSQL))
	pgApply(t, name, "frameworks_demo", string(demoSQL))
}
