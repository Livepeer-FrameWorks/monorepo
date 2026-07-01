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
	return buildMigrationItemsFromList(all, databases, phase, targetVersion), nil
}

// buildMigrationItemsFromList applies the logical-source→physical-target remap,
// the phase filter, the baseline floor, and the target-version cap to an already
// discovered migration list. Split out from BuildMigrationItemsForDatabases so the
// selection logic is testable without depending on which versions happen to be in
// the embedded tree (which the consolidation floor + deletion change over time).
func buildMigrationItemsFromList(all []Migration, databases []SchemaDatabase, phase, targetVersion string) []map[string]any {
	bySource := targetsBySource(databases)

	items := make([]map[string]any, 0, len(all))
	for _, m := range all {
		if m.Phase != phase {
			continue
		}
		if belowBaselineFloor(m) {
			continue
		}
		targets := bySource[m.Database]
		if len(targets) == 0 {
			continue
		}
		if compareSemver(m.Version, targetVersion) > 0 {
			continue
		}
		for _, target := range targets {
			items = append(items, migrationItem(target, m))
		}
	}
	return items
}

// targetsBySource maps each embedded source database name to the physical target
// database name(s) it provisions (logical→physical, e.g. foghorn→foghorn_eu).
func targetsBySource(databases []SchemaDatabase) map[string][]string {
	bySource := make(map[string][]string, len(databases))
	for _, database := range databases {
		target := database.Name
		if target == "" {
			continue
		}
		source := database.SourceName
		if source == "" {
			source = target
		}
		bySource[source] = append(bySource[source], target)
	}
	return bySource
}

func migrationItem(target string, m Migration) map[string]any {
	return map[string]any{
		"db":            target,
		"version":       m.Version,
		"phase":         m.Phase,
		"sequence":      m.Sequence,
		"filename":      m.Filename,
		"checksum":      m.Checksum,
		"transactional": m.Transactional,
		"sql":           m.content,
	}
}

// belowFloorItemsFromList returns the migrations strictly BELOW the baseline floor
// (across all phases) for the given databases — the complement of what the build
// functions offer. These are folded into the baseline; the minimum-upgrade-version
// guard checks a cluster has applied all of them before it may upgrade past the
// floor (an existing cluster never re-applies the baseline, so a gap strands it).
func belowFloorItemsFromList(all []Migration, databases []SchemaDatabase) []map[string]any {
	bySource := targetsBySource(databases)
	items := make([]map[string]any, 0)
	for _, m := range all {
		if !belowBaselineFloor(m) {
			continue
		}
		for _, target := range bySource[m.Database] {
			items = append(items, migrationItem(target, m))
		}
	}
	return items
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
		if m.Phase == phase && !belowBaselineFloor(m) && enabledSources[m.Database] {
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
		if belowBaselineFloor(m) {
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
		if m.Phase == phase && !belowBaselineFloor(m) && enabledDBs[m.Database] {
			return true, nil
		}
	}
	return false, nil
}
