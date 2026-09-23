package provisioner

import (
	"errors"
	"fmt"
	"strings"

	"frameworks/cli/internal/releases"

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

// BuildMigrationItemsForDatabases builds migration items for PostgreSQL targets. Ledger comparisons use it because
// item identity and checksums do not depend on the engine.
func BuildMigrationItemsForDatabases(databases []SchemaDatabase, phase, targetVersion string) ([]map[string]any, error) {
	return BuildMigrationItemsForEngine(databases, phase, targetVersion, SQLEnginePostgres)
}

// BuildMigrationItemsForEngine builds migration items for the engine that will apply them. YugabyteDB items carry SQL
// with the source database's layout applied; checksums remain those of the embedded files.
func BuildMigrationItemsForEngine(databases []SchemaDatabase, phase, targetVersion string, engine SQLEngine) ([]map[string]any, error) {
	if err := validateSQLEngine(engine); err != nil {
		return nil, err
	}
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
	return buildMigrationItemsFromList(all, databases, phase, targetVersion, engine)
}

// buildMigrationItemsFromList applies the logical-source→physical-target remap,
// the phase filter, the baseline floor, and the target-version cap to an already
// discovered migration list. Split out from BuildMigrationItemsForEngine so the
// selection logic is testable without depending on which versions happen to be in
// the embedded tree (which the consolidation floor + deletion change over time).
func buildMigrationItemsFromList(all []Migration, databases []SchemaDatabase, phase, targetVersion string, engine SQLEngine) ([]map[string]any, error) {
	bySource := targetsBySource(databases)
	// Compare against the target's BASE version: a canary (e.g. v1.2.3-rc1, which sorts BEFORE the final v1.2.3)
	// still runs every migration the v1.2.3 line introduces — the rc binary needs that schema.
	baseTarget := releases.BaseVersion(targetVersion)
	prechecks, err := embeddedMigrationPrechecks()
	if err != nil {
		return nil, fmt.Errorf("load migration prechecks: %w", err)
	}

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
		if compareSemver(m.Version, baseTarget) > 0 {
			continue
		}
		if engine == SQLEngineYugabyte {
			rewritten, err := yugabyteSQLForSource(m.Database, m.content)
			if err != nil {
				return nil, fmt.Errorf("apply YugabyteDB layout to migration %s/%s/%s/%s: %w", m.Database, m.Version, m.Phase, m.Filename, err)
			}
			m.content = rewritten
		}
		for _, target := range targets {
			item, err := migrationItem(target, m, prechecks[m.Path])
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
	}
	return items, nil
}

// targetsBySource maps each embedded source database name to the physical target
// database name(s) it provisions (logical→physical, e.g. foghorn→foghorn_eu).
func targetsBySource(databases []SchemaDatabase) map[string][]SchemaDatabase {
	bySource := make(map[string][]SchemaDatabase, len(databases))
	for _, database := range databases {
		target := database.Name
		if target == "" {
			continue
		}
		source := database.SourceName
		if source == "" {
			source = target
		}
		database.Name = target
		bySource[source] = append(bySource[source], database)
	}
	return bySource
}

// migrationLedgerItem identifies a migration on its physical target database: what the ledger records for it.
func migrationLedgerItem(target SchemaDatabase, m Migration) map[string]any {
	owner := target.Owner
	if owner == "" {
		owner = target.Name
	}
	schema := target.Schema
	if schema == "" {
		schema = target.SourceName
	}
	if schema == "" {
		schema = target.Name
	}
	return map[string]any{
		"db":            target.Name,
		"owner":         owner,
		"schema":        schema,
		"version":       m.Version,
		"phase":         m.Phase,
		"sequence":      m.Sequence,
		"filename":      m.Filename,
		"checksum":      m.Checksum,
		"transactional": m.Transactional,
		"sql":           m.content,
	}
}

// migrationItem is a migration ready for the roles to apply: its ledger identity plus the statements to execute.
func migrationItem(target SchemaDatabase, m Migration, precheck migrationPrecheck) (map[string]any, error) {
	statements, guards, err := migrationStatements(m)
	if err != nil {
		return nil, fmt.Errorf("migration %s/%s/%s/%s: %w", m.Database, m.Version, m.Phase, m.Filename, err)
	}
	item := migrationLedgerItem(target, m)
	item["statements"] = statements
	// invalid_index_guards names the indexes this item builds concurrently. An interrupted build leaves an invalid
	// index that IF NOT EXISTS would skip, so the roles drop an invalid one before applying the item.
	item["invalid_index_guards"] = guards
	// precheck_query, when set, is the read-only report the roles run immediately before this item; any row it returns
	// fails the item before its first statement. Empty means the item has no precheck.
	item["precheck_path"] = precheck.Path
	item["precheck_query"] = precheck.Query
	return item, nil
}

// migrationStatements returns the statements a migration item executes. A transactional migration runs as one
// script. A non-transactional one is split with the tokenizer the YugabyteDB layout rewriter uses, so both agree on
// statement boundaries; its concurrent index builds are returned as schema.index guards.
func migrationStatements(m Migration) ([]string, []string, error) {
	if m.Transactional {
		return []string{m.content}, []string{}, nil
	}
	statements, err := sqlStatements(m.content)
	if err != nil {
		return nil, nil, err
	}
	texts := make([]string, 0, len(statements))
	guards := []string{}
	for _, statement := range statements {
		texts = append(texts, strings.TrimSpace(m.content[statement[0].start:statement[len(statement)-1].end]))
		guard, err := concurrentIndexGuard(statement)
		if err != nil {
			return nil, nil, err
		}
		if guard != "" {
			guards = append(guards, guard)
		}
	}
	return texts, guards, nil
}

// concurrentIndexGuard returns schema.index for CREATE [UNIQUE] INDEX CONCURRENTLY [IF NOT EXISTS] index ON [ONLY]
// schema.table, and "" for any other statement. A concurrent build must name its index and a schema-qualified table,
// because the guard that repairs an interrupted build finds the index by both.
func concurrentIndexGuard(statement []sqlToken) (string, error) {
	i := 0
	next := func(words ...string) bool {
		if i < len(statement) && statement[i].kind == sqlTokenWord {
			for _, word := range words {
				if statement[i].text == word {
					i++
					return true
				}
			}
		}
		return false
	}
	if !next("create") {
		return "", nil
	}
	next("unique")
	if !next("index") || !next("concurrently") {
		return "", nil
	}
	if next("if") {
		if !next("not") || !next("exists") {
			return "", errors.New("malformed CREATE INDEX CONCURRENTLY IF NOT EXISTS")
		}
	}
	identifier := func() (string, bool) {
		if i < len(statement) && (statement[i].kind == sqlTokenWord || statement[i].kind == sqlTokenQuotedIdentifier) {
			i++
			return statement[i-1].text, true
		}
		return "", false
	}
	if i < len(statement) && statement[i].kind == sqlTokenWord && statement[i].text == "on" {
		return "", errors.New("CREATE INDEX CONCURRENTLY must name its index")
	}
	index, ok := identifier()
	if !ok || !next("on") {
		return "", errors.New("CREATE INDEX CONCURRENTLY must name its index")
	}
	next("only")
	schema, ok := identifier()
	if !ok || i >= len(statement) || statement[i].kind != sqlTokenPunct || statement[i].text != "." {
		return "", fmt.Errorf("CREATE INDEX CONCURRENTLY %s must build on a schema-qualified table", index)
	}
	return schema + "." + index, nil
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
			items = append(items, migrationLedgerItem(target, m))
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
	baseTarget := releases.BaseVersion(targetVersion)
	items := make([]map[string]any, 0, len(all))
	for _, m := range all {
		if m.Phase != phase || !enabledDBs[m.Database] {
			continue
		}
		if belowBaselineFloor(m) {
			continue
		}
		if compareSemver(m.Version, baseTarget) > 0 {
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
