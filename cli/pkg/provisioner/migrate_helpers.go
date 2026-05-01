package provisioner

import (
	"fmt"
)

// BuildMigrationItems collects discovered migrations for configured databases
// and one migration phase, capped at targetVersion. Returns entries suitable
// for postgres_migrate_items / yugabyte_migrate_items role vars.
//
// targetVersion must be a concrete vX.Y.Z. Empty is rejected — callers must
// resolve channel names to a concrete version before reaching this function.
func BuildMigrationItems(dbNames []string, phase, targetVersion string) ([]map[string]any, error) {
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
			"db":            m.Database,
			"version":       m.Version,
			"phase":         m.Phase,
			"sequence":      m.Sequence,
			"filename":      m.Filename,
			"checksum":      m.Checksum,
			"transactional": m.Transactional,
			"sql":           m.content,
		})
	}
	return items, nil
}

// HasMigrations reports whether any embedded migration exists for the
// configured databases and phase, without requiring a target version.
func HasMigrations(dbNames []string, phase string) (bool, error) {
	if _, ok := migrationPhaseOrder[phase]; !ok {
		return false, fmt.Errorf("invalid migration phase %q", phase)
	}
	if len(dbNames) == 0 {
		return false, nil
	}
	all, err := discoverMigrations("migrations")
	if err != nil {
		return false, fmt.Errorf("discover migrations: %w", err)
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
