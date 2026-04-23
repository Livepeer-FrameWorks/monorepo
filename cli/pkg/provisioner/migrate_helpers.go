package provisioner

import (
	"fmt"
)

// BuildMigrationItems collects every discovered migration under
// pkg/database/sql/migrations for each database in dbNames. Returns a list
// of {db, version, sequence, filename, checksum, sql} entries suitable for
// postgres_migrate_items / yugabyte_migrate_items role vars.
func BuildMigrationItems(dbNames []string) ([]map[string]any, error) {
	all, err := discoverMigrations("migrations")
	if err != nil {
		return nil, fmt.Errorf("discover migrations: %w", err)
	}
	if len(all) == 0 || len(dbNames) == 0 {
		return nil, nil
	}
	items := make([]map[string]any, 0, len(all)*len(dbNames))
	for _, db := range dbNames {
		for _, m := range all {
			items = append(items, map[string]any{
				"db":       db,
				"version":  m.Version,
				"sequence": m.Sequence,
				"filename": m.Filename,
				"checksum": m.Checksum,
				"sql":      m.content,
			})
		}
	}
	return items, nil
}
