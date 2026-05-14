package provisioner

import (
	"fmt"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// BuildMigrationItems collects discovered migrations for configured databases
// and one migration phase, capped at targetVersion. Returns entries suitable
// for postgres_migrate_items / yugabyte_migrate_items role vars.
//
// targetVersion must be a concrete vX.Y.Z. Empty is rejected — callers must
// resolve channel names to a concrete version before reaching this function.
func BuildMigrationItems(dbNames []string, phase, targetVersion string) ([]map[string]any, error) {
	databases := make([]SchemaDatabase, 0, len(dbNames))
	for _, db := range dbNames {
		databases = append(databases, SchemaDatabase{Name: db})
	}
	return BuildMigrationItemsForDatabases(databases, phase, targetVersion)
}

func BuildMigrationItemsForDatabases(databases []SchemaDatabase, phase, targetVersion string) ([]map[string]any, error) {
	if _, ok := migrationPhaseOrder[phase]; !ok {
		return nil, fmt.Errorf("invalid migration phase %q", phase)
	}
	if targetVersion == "" {
		return nil, fmt.Errorf("BuildMigrationItems: targetVersion required (resolve channel to concrete vX.Y.Z first)")
	}

	all, err := discoverMigrations("migrations")
	if err != nil {
		return nil, fmt.Errorf("discover migrations: %w", err)
	}
	if len(all) == 0 || len(databases) == 0 {
		return nil, nil
	}

	targetsBySource := make(map[string][]string, len(databases))
	for _, database := range databases {
		target := database.Name
		if target == "" {
			continue
		}
		source := database.SourceName
		if source == "" {
			source = target
		}
		targetsBySource[source] = append(targetsBySource[source], target)
	}

	items := make([]map[string]any, 0, len(all))
	for _, m := range all {
		if m.Phase != phase {
			continue
		}
		targets := targetsBySource[m.Database]
		if len(targets) == 0 {
			continue
		}
		if compareSemver(m.Version, targetVersion) > 0 {
			continue
		}
		for _, target := range targets {
			items = append(items, map[string]any{
				"db":            target,
				"version":       m.Version,
				"phase":         m.Phase,
				"sequence":      m.Sequence,
				"filename":      m.Filename,
				"checksum":      m.Checksum,
				"transactional": m.Transactional,
				"sql":           m.content,
			})
		}
	}
	return items, nil
}

// HasMigrations reports whether any embedded migration exists for the
// configured databases and phase, without requiring a target version.
func HasMigrations(dbNames []string, phase string) (bool, error) {
	databases := make([]SchemaDatabase, 0, len(dbNames))
	for _, db := range dbNames {
		databases = append(databases, SchemaDatabase{Name: db})
	}
	return HasMigrationsForDatabases(databases, phase)
}

func HasMigrationsForDatabases(databases []SchemaDatabase, phase string) (bool, error) {
	if _, ok := migrationPhaseOrder[phase]; !ok {
		return false, fmt.Errorf("invalid migration phase %q", phase)
	}
	if len(databases) == 0 {
		return false, nil
	}
	all, err := discoverMigrations("migrations")
	if err != nil {
		return false, fmt.Errorf("discover migrations: %w", err)
	}
	enabledSources := make(map[string]bool, len(databases))
	for _, database := range databases {
		source := database.SourceName
		if source == "" {
			source = database.Name
		}
		if source != "" {
			enabledSources[source] = true
		}
	}
	for _, m := range all {
		if m.Phase == phase && enabledSources[m.Database] {
			return true, nil
		}
	}
	return false, nil
}

// BuildClickHouseMigrationItems is the CH equivalent of BuildMigrationItems.
// Returns entries shaped {db, version, phase, sequence, filename, checksum, sql}
// suitable for the clickhouse_migrate_items role var consumed by
// roles/clickhouse/tasks/migrate.yml. ClickHouse has no DDL transactions, so
// the transactional flag is intentionally omitted.
func BuildClickHouseMigrationItems(dbNames []string, phase, targetVersion string) ([]map[string]any, error) {
	if _, ok := migrationPhaseOrder[phase]; !ok {
		return nil, fmt.Errorf("invalid migration phase %q", phase)
	}
	if targetVersion == "" {
		return nil, fmt.Errorf("BuildClickHouseMigrationItems: targetVersion required (resolve channel to concrete vX.Y.Z first)")
	}
	knownDBs, err := knownClickHouseDatabases()
	if err != nil {
		return nil, err
	}
	all, err := discoverMigrationsInFS(dbsql.Content, "clickhouse/migrations", knownDBs)
	if err != nil {
		return nil, fmt.Errorf("discover clickhouse migrations: %w", err)
	}
	if len(all) == 0 || len(dbNames) == 0 {
		return nil, nil
	}
	enabledDBs := make(map[string]bool, len(dbNames))
	for _, db := range dbNames {
		enabledDBs[db] = true
	}
	items := make([]map[string]any, 0, len(all))
	for _, m := range all {
		if m.Phase != phase || !enabledDBs[m.Database] {
			continue
		}
		if compareSemver(m.Version, targetVersion) > 0 {
			continue
		}
		items = append(items, map[string]any{
			"db":       m.Database,
			"version":  m.Version,
			"phase":    m.Phase,
			"sequence": m.Sequence,
			"filename": m.Filename,
			"checksum": m.Checksum,
			"sql":      m.content,
		})
	}
	return items, nil
}

// HasClickHouseMigrations reports whether any embedded ClickHouse migration
// exists for the configured databases and phase. Used by initClickHouse to
// decide whether to resolve a target version.
func HasClickHouseMigrations(dbNames []string, phase string) (bool, error) {
	if _, ok := migrationPhaseOrder[phase]; !ok {
		return false, fmt.Errorf("invalid migration phase %q", phase)
	}
	if len(dbNames) == 0 {
		return false, nil
	}
	knownDBs, err := knownClickHouseDatabases()
	if err != nil {
		return false, err
	}
	all, err := discoverMigrationsInFS(dbsql.Content, "clickhouse/migrations", knownDBs)
	if err != nil {
		return false, fmt.Errorf("discover clickhouse migrations: %w", err)
	}
	enabledDBs := make(map[string]bool, len(dbNames))
	for _, db := range dbNames {
		enabledDBs[db] = true
	}
	for _, m := range all {
		if m.Phase == phase && enabledDBs[m.Database] {
			return true, nil
		}
	}
	return false, nil
}
