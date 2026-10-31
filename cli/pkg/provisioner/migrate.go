package provisioner

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"frameworks/cli/internal/releases"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// Migration represents a single versioned SQL migration file. Consumed by
// BuildMigrationItems, which hands the set to the postgres / yugabyte role
// via *_migrate_items vars; the role's tasks/migrate.yml does the apply.
type Migration struct {
	Database      string // e.g. "purser"
	Version       string // e.g. "v1.1.0"
	Phase         string // expand, postdeploy, contract
	Sequence      int    // parsed from NNN prefix
	Filename      string // e.g. "001_add_invoice_field.sql"
	Path          string // full embed path
	Checksum      string // SHA-256 of content
	Transactional bool   // false for *.notx.sql files
	content       string
}

type MigrationValidationIssue struct {
	Path    string
	Message string
}

type MigrationValidationError struct {
	Issues []MigrationValidationIssue
}

func (e *MigrationValidationError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d migration validation issue(s):", len(e.Issues))
	for _, issue := range e.Issues {
		fmt.Fprintf(&b, "\n- %s: %s", issue.Path, issue.Message)
	}
	return b.String()
}

var migrationPhaseOrder = map[string]int{
	"expand":     0,
	"postdeploy": 1,
	"contract":   2,
}

func enforcesMigrationPhaseSafety(migration Migration) bool {
	// Everything still present after a schema consolidation is current migration history and must satisfy the phase
	// rules. Older SQL is absent, rather than retained behind a second historical validation floor.
	return !belowBaselineFloor(migration)
}

// schemaMigrationBaselineFloor is the schema-consolidation floor: migrations with
// a version strictly below it are folded into the baseline schema files
// (pkg/database/sql/schema/*.sql, clickhouse/periscope.sql) and are never offered.
//
// SAFETY INVARIANT: set the floor at or below the first version not fully
// applied by every live cluster. Existing clusters never re-apply the baseline,
// so raising it too far would silently skip migrations between the cluster's
// applied version and the floor. v0.3.0 is the deliberate hard boundary: fresh
// clusters are born at it and the only supported in-place source is the fully
// converged, republished v0.2.96 release.
//
// The minimum-upgrade-version guard ({Postgres,ClickHouse}BelowFloorGap in
// migration_floor_guard.go) enforces this: a cluster missing any < floor migration
// is refused with a stepping-stone message rather than silently stranded. DISTINCT
var schemaMigrationBaselineFloor = releases.SchemaMigrationFloor()

// belowBaselineFloor reports whether a migration predates the current schema
// consolidation and is therefore folded into the baseline. Such migrations are
// filtered from generated migration items; fresh installs get their effect via
// the baseline schema, while existing clusters must satisfy the below-floor guard.
func belowBaselineFloor(migration Migration) bool {
	return compareSemver(migration.Version, schemaMigrationBaselineFloor) < 0
}

var expandUnsafeSQLPatterns = []struct {
	re      *regexp.Regexp
	message string
}{
	{regexp.MustCompile(`(?is)\bDROP\s+(TABLE|COLUMN|SCHEMA|TYPE|INDEX)\b`), "drop operations belong in contract migrations"},
	{regexp.MustCompile(`(?is)\bALTER\s+TABLE\b.*\bRENAME\b`), "renames are not expand-compatible with old binaries"},
	{regexp.MustCompile(`(?is)\bALTER\s+TABLE\b.*\bSET\s+NOT\s+NULL\b`), "SET NOT NULL requires a completed data migration and postdeploy/contract gating"},
	// Match a real data-rewrite UPDATE (UPDATE <table> ... SET), bounded to a single statement
	// so it does NOT false-positive on the trigger event clause "BEFORE/AFTER UPDATE ON" (valid
	// DDL) or on the word "UPDATE" in a comment. Real rewrites always have SET.
	{regexp.MustCompile(`(?is)\bUPDATE\s+[A-Za-z_][A-Za-z0-9_.]*[^;]*?\bSET\b`), "bulk data rewrites belong in service-owned background data migrations"},
	{regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+[A-Za-z_][A-Za-z0-9_.]*\b`), "bulk deletes belong in service-owned background data migrations or contract"},
}

var expandUpsertSQLPattern = regexp.MustCompile(`(?is)\bON\s+CONFLICT\b[^;]*?\bDO\s+UPDATE\s+SET\b`)

var expandExecutingSQLPatterns = []struct {
	re      *regexp.Regexp
	message string
}{
	{expandUpsertSQLPattern, "bulk upserts belong in service-owned background data migrations"},
	{regexp.MustCompile(`(?is)\bMERGE\s+INTO\b`), "bulk merges belong in service-owned background data migrations"},
	{regexp.MustCompile(`(?is)\bTRUNCATE\b`), "truncate operations belong in contract migrations"},
}

var (
	notValidPattern                    = regexp.MustCompile(`(?is)\bNOT\s+VALID\b`)
	createIndexConcurrently            = regexp.MustCompile(`(?is)\bCREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY\b`)
	createIndexConcurrentlyIfNotExists = regexp.MustCompile(`(?is)\bCREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\b`)
	createConcurrentIndexName          = regexp.MustCompile(`(?is)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\s+(?:[A-Za-z_][A-Za-z0-9_]*\.)?"?([A-Za-z_][A-Za-z0-9_]*)"?`)
	toRegclassReference                = regexp.MustCompile(`(?is)\bto_regclass\s*\(\s*'([^']+)'\s*\)`)
	castRegclassReference              = regexp.MustCompile(`(?is)'([^']+)'\s*::\s*regclass\b`)
	castNameReference                  = regexp.MustCompile(`(?is)'([^']+)'\s*::\s*name\b`)
	addConstraintName                  = regexp.MustCompile(`(?is)\bADD\s+CONSTRAINT\s+"?([A-Za-z_][A-Za-z0-9_]*)"?`)
	addAnonymousConstraint             = regexp.MustCompile(`(?is)\bADD\s+(?:CHECK\s*\(|FOREIGN\s+KEY\s*\()`)
	validateConstraintName             = regexp.MustCompile(`(?is)\bVALIDATE\s+CONSTRAINT\s+"?([A-Za-z_][A-Za-z0-9_]*)"?`)
	dropConstraintName                 = regexp.MustCompile(`(?is)\bDROP\s+CONSTRAINT(?:\s+IF\s+EXISTS)?\s+"?([A-Za-z_][A-Za-z0-9_]*)"?`)
	updateTableName                    = regexp.MustCompile(`(?is)\bUPDATE\s+([A-Za-z_][A-Za-z0-9_.]*)(?:\s+(?:AS\s+)?[A-Za-z_][A-Za-z0-9_]*)?\s+SET\b`)
	alterColumnType                    = regexp.MustCompile(`(?is)\bALTER\s+TABLE\b.*\bALTER\s+COLUMN\b.*\bTYPE\b`)
	compatiblePushTargetURIWidening    = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+commodore\.push_targets\s+ALTER\s+COLUMN\s+target_uri\s+TYPE\s+TEXT\s*;?\s*$`)
)

// discoverMigrations walks the embedded FS under root looking for migrations
// shaped as migrations/<database>/vX.Y.Z/<phase>/NNN_description.sql.
func discoverMigrations(root string) ([]Migration, error) {
	knownDBs, err := knownMigrationDatabases()
	if err != nil {
		return nil, err
	}
	return discoverMigrationsInFS(dbsql.Content, root, knownDBs)
}

func ValidateEmbeddedPostgresMigrations() error {
	migrations, err := discoverAllPostgresMigrationsForValidation()
	if err != nil {
		return err
	}
	var precheckErr, rerunErr error
	if issues := validateMigrationPrechecks(dbsql.Content, migrations); len(issues) > 0 {
		precheckErr = &MigrationValidationError{Issues: issues}
	}
	if issues := append(validateMigrationRerunSafety(migrations), validateRerunGuardTargets(migrations)...); len(issues) > 0 {
		rerunErr = &MigrationValidationError{Issues: issues}
	}
	return errors.Join(
		validatePostgresMigrationSet(migrations),
		validateEmbeddedMigrationReleaseCeiling(migrations),
		precheckErr,
		rerunErr,
	)
}

// ValidateEmbeddedClickHouseMigrations validates the embedded
// pkg/database/sql/clickhouse/migrations/ tree under CH-specific phase rules.
// Called by `frameworks cluster migrate validate` alongside the Postgres
// validator; combined issues surface to the operator.
func ValidateEmbeddedClickHouseMigrations() error {
	migrations, err := discoverAllClickHouseMigrationsForValidation()
	if err != nil {
		return err
	}
	return errors.Join(
		validateClickHouseMigrationSet(migrations),
		validateEmbeddedMigrationReleaseCeiling(migrations),
	)
}

func validateEmbeddedMigrationReleaseCeiling(migrations []Migration) error {
	catalog, err := releases.CatalogOrError()
	if err != nil {
		return fmt.Errorf("read release catalog for migration version ceiling: %w", err)
	}
	if len(catalog) == 0 {
		if len(migrations) == 0 {
			return nil
		}
		return fmt.Errorf("migration version ceiling: cli/internal/releases/catalog.yaml declares no releases")
	}
	return validateMigrationReleaseCeiling(migrations, catalog[len(catalog)-1].Version)
}

func validateMigrationReleaseCeiling(migrations []Migration, latestRelease string) error {
	var issues []MigrationValidationIssue
	for _, migration := range migrations {
		if compareSemver(migration.Version, latestRelease) <= 0 {
			continue
		}
		issues = append(issues, MigrationValidationIssue{
			Path: migration.Path,
			Message: fmt.Sprintf(
				"version %s exceeds latest declared release %s; current code migrations must target the next release in cli/internal/releases/catalog.yaml",
				migration.Version,
				latestRelease,
			),
		})
	}
	if len(issues) > 0 {
		return &MigrationValidationError{Issues: issues}
	}
	return nil
}

// discoverAllPostgresMigrationsForValidation returns the full embedded set with
// no target-version cap. Only the validator should consume this — runtime
// callers must use BuildMigrationItems with a concrete targetVersion.
func discoverAllPostgresMigrationsForValidation() ([]Migration, error) {
	knownDBs, err := knownMigrationDatabases()
	if err != nil {
		return nil, err
	}
	return discoverMigrationsInFS(dbsql.Content, "migrations", knownDBs)
}

// discoverAllClickHouseMigrationsForValidation is the CH equivalent of the
// Postgres discovery used for validation. It walks
// pkg/database/sql/clickhouse/migrations under the same
// <db>/<version>/<phase>/NNN_*.sql layout.
func discoverAllClickHouseMigrationsForValidation() ([]Migration, error) {
	knownDBs, err := knownClickHouseDatabases()
	if err != nil {
		return nil, err
	}
	return discoverMigrationsInFS(dbsql.Content, "clickhouse/migrations", knownDBs)
}

func discoverMigrationsInFS(fsys fs.FS, root string, knownDBs map[string]bool) ([]Migration, error) {
	var out []Migration
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sql") {
			return err
		}

		rel := strings.TrimPrefix(p, strings.TrimSuffix(root, "/")+"/")
		parts := strings.Split(rel, "/")
		if len(parts) != 4 {
			return fmt.Errorf("invalid migration path %q: expected %s/<database>/vX.Y.Z/<phase>/NNN_description.sql", p, root)
		}

		dbName, ver, phase := parts[0], parts[1], parts[2]
		if !knownDBs[dbName] {
			return fmt.Errorf("invalid migration path %q: unknown database %q", p, dbName)
		}
		if !strings.HasPrefix(ver, "v") {
			return fmt.Errorf("invalid migration path %q: version directory must start with v", p)
		}
		if _, ok := migrationPhaseOrder[phase]; !ok {
			return fmt.Errorf("invalid migration path %q: phase must be expand, postdeploy, or contract", p)
		}

		base := path.Base(p)
		seq := parseSequence(base)
		if seq <= 0 {
			return fmt.Errorf("invalid migration filename %q: expected NNN_description.sql", p)
		}

		data, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			return readErr
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(data))

		out = append(out, Migration{
			Database:      dbName,
			Version:       ver,
			Phase:         phase,
			Sequence:      seq,
			Filename:      base,
			Path:          p,
			Checksum:      checksum,
			Transactional: !strings.HasSuffix(base, ".notx.sql"),
			content:       string(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Database != out[j].Database {
			return out[i].Database < out[j].Database
		}
		if out[i].Version != out[j].Version {
			return compareSemver(out[i].Version, out[j].Version) < 0
		}
		if out[i].Phase != out[j].Phase {
			return migrationPhaseOrder[out[i].Phase] < migrationPhaseOrder[out[j].Phase]
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out, nil
}

func validatePostgresMigrationSet(migrations []Migration) error {
	issues := validateSequenceCollisions(migrations)
	postdeployGuards := make(map[string][]string)
	expandConstraints := make(map[string]map[string]string)
	postdeployConstraints := make(map[string]map[string]string)
	postdeployValidations := make(map[string]map[string]string)
	postdeployConstraintTables := make(map[string]map[string]string)
	postdeployUpdates := make(map[string]map[string][]string)
	contractDrops := make(map[string]map[string]struct{})
	migrationSequenceByPath := make(map[string]int, len(migrations))
	for _, migration := range migrations {
		migrationSequenceByPath[migration.Path] = migration.Sequence
		key := migration.Database + "\x00" + migration.Version
		code := stripSQLSingleQuotedLiterals(stripSQLComments(migration.content))
		if !enforcesMigrationPhaseSafety(migration) {
			continue
		}
		if migration.Phase == "postdeploy" {
			for _, statement := range splitSQLStatements(stripSQLComments(migration.content)) {
				postdeployGuards[key] = append(postdeployGuards[key], strings.ToLower(statement))
			}
			for _, name := range constraintNames(validateConstraintName, code) {
				if postdeployValidations[key] == nil {
					postdeployValidations[key] = make(map[string]string)
				}
				postdeployValidations[key][name] = migration.Path
			}
			statements := splitSQLStatements(code)
			validateAt := make(map[string]int)
			addAt := make(map[string]int)
			for statementIndex, statement := range statements {
				for _, name := range constraintNames(validateConstraintName, statement) {
					if _, exists := validateAt[name]; !exists {
						validateAt[name] = statementIndex
					}
				}
				if match := updateTableName.FindStringSubmatch(statement); len(match) == 2 {
					if postdeployUpdates[key] == nil {
						postdeployUpdates[key] = make(map[string][]string)
					}
					table := strings.ToLower(match[1])
					postdeployUpdates[key][table] = append(postdeployUpdates[key][table], migration.Path)
				}
				table := ""
				if match := regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_.]*)\b`).FindStringSubmatch(statement); len(match) == 2 {
					table = strings.ToLower(match[1])
				}
				for _, constraint := range addedConstraints(statement) {
					if !constraint.notValid {
						continue
					}
					if postdeployConstraints[key] == nil {
						postdeployConstraints[key] = make(map[string]string)
					}
					postdeployConstraints[key][constraint.name] = migration.Path
					if _, exists := addAt[constraint.name]; !exists {
						addAt[constraint.name] = statementIndex
					}
					if table != "" {
						if postdeployConstraintTables[key] == nil {
							postdeployConstraintTables[key] = make(map[string]string)
						}
						postdeployConstraintTables[key][constraint.name] = table
					}
				}
			}
			for name, validationIndex := range validateAt {
				if addIndex, ok := addAt[name]; ok && validationIndex < addIndex {
					issues = append(issues, MigrationValidationIssue{
						Path: migration.Path, Message: fmt.Sprintf("postdeploy VALIDATE CONSTRAINT %q precedes its ADD CONSTRAINT ... NOT VALID", name),
					})
				}
			}
		}
		if migration.Phase == "expand" && enforcesMigrationPhaseSafety(migration) {
			for _, statement := range splitSQLStatements(code) {
				for _, constraint := range addedConstraints(statement) {
					if !constraint.notValid {
						continue
					}
					if expandConstraints[key] == nil {
						expandConstraints[key] = make(map[string]string)
					}
					expandConstraints[key][constraint.name] = migration.Path
				}
			}
		}
		if migration.Phase == "contract" {
			for _, name := range constraintNames(validateConstraintName, code) {
				issues = append(issues, MigrationValidationIssue{
					Path:    migration.Path,
					Message: fmt.Sprintf("VALIDATE CONSTRAINT %q belongs in postdeploy, not contract", name),
				})
			}
			for _, name := range constraintNames(dropConstraintName, code) {
				if contractDrops[key] == nil {
					contractDrops[key] = make(map[string]struct{})
				}
				contractDrops[key][name] = struct{}{}
			}
		}
	}
	for _, migration := range migrations {
		content := stripSQLComments(migration.content)
		structuralContent := stripSQLSingleQuotedLiterals(content)
		if migration.Phase == "expand" && enforcesMigrationPhaseSafety(migration) {
			executingSQL := executingMigrationSQL(content)
			for _, pattern := range expandExecutingSQLPatterns {
				if pattern.re == expandUpsertSQLPattern && immutableShippedUpsert(migration) {
					continue
				}
				if pattern.re.MatchString(executingSQL) {
					issues = append(issues, MigrationValidationIssue{Path: migration.Path, Message: pattern.message})
				}
			}
			immediateSQL := expandMigrationSafetySQL(content)
			for _, pattern := range expandUnsafeSQLPatterns {
				if pattern.re.MatchString(immediateSQL) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: pattern.message,
					})
				}
			}
			for _, stmt := range splitSQLStatements(structuralContent) {
				if alterColumnType.MatchString(stmt) && !compatiblePushTargetURIWidening.MatchString(stmt) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "column type rewrites are not expand-compatible; only the metadata-only commodore.push_targets.target_uri varchar-to-text widening is permitted",
					})
				}
				if addAnonymousConstraint.MatchString(stmt) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "new CHECK and FOREIGN KEY constraints in expand must use an explicit CONSTRAINT name and NOT VALID",
					})
				}
				for _, constraint := range addedConstraints(stmt) {
					if !constraint.notValid {
						issues = append(issues, MigrationValidationIssue{
							Path:    migration.Path,
							Message: fmt.Sprintf("new constraint %q in expand must be NOT VALID or moved to a later phase", constraint.name),
						})
						continue
					}
					key := migration.Database + "\x00" + migration.Version
					if _, dropped := contractDrops[key][constraint.name]; dropped {
						continue
					}
					if _, validated := postdeployValidations[key][constraint.name]; !validated {
						issues = append(issues, MigrationValidationIssue{
							Path:    migration.Path,
							Message: fmt.Sprintf("NOT VALID constraint %q requires a same-release postdeploy VALIDATE CONSTRAINT", constraint.name),
						})
					}
				}
			}
		}

		hasConcurrentIndex := createIndexConcurrently.MatchString(structuralContent)
		if hasConcurrentIndex && migration.Transactional {
			issues = append(issues, MigrationValidationIssue{
				Path:    migration.Path,
				Message: "CREATE INDEX CONCURRENTLY must use a .notx.sql filename",
			})
		}
		if !hasConcurrentIndex && !migration.Transactional {
			issues = append(issues, MigrationValidationIssue{
				Path:    migration.Path,
				Message: ".notx.sql is reserved for SQL that requires autocommit, such as CREATE INDEX CONCURRENTLY",
			})
		}
		if !migration.Transactional && hasConcurrentIndex {
			// The statements checked here are the ones the roles execute, split by the same tokenizer.
			statements, _, splitErr := migrationStatements(migration, SQLEnginePostgres)
			if splitErr != nil {
				issues = append(issues, MigrationValidationIssue{Path: migration.Path, Message: splitErr.Error()})
			}
			for _, stmt := range statements {
				if !createIndexConcurrently.MatchString(stmt) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: ".notx.sql statements must each require autocommit; only CREATE INDEX CONCURRENTLY is supported",
					})
					continue
				}
				if !createIndexConcurrentlyIfNotExists.MatchString(stmt) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: ".notx.sql with CREATE INDEX CONCURRENTLY must use IF NOT EXISTS for idempotent reruns; postdeploy must still reject an invalid leftover index",
					})
				}
				match := createConcurrentIndexName.FindStringSubmatch(stmt)
				if len(match) != 2 {
					continue
				}
				indexName := strings.ToLower(match[1])
				if !hasConcurrentIndexGuard(postdeployGuards[migration.Database+"\x00"+migration.Version], indexName) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: fmt.Sprintf("concurrent index %q requires a same-release postdeploy pg_index guard for indisvalid and indisready", match[1]),
					})
				}
			}
		}
	}
	for key, constraints := range postdeployConstraints {
		for name, constraintPath := range constraints {
			table := postdeployConstraintTables[key][name]
			for _, updatePath := range postdeployUpdates[key][table] {
				if migrationSequenceByPath[updatePath] < migrationSequenceByPath[constraintPath] {
					continue
				}
				issues = append(issues, MigrationValidationIssue{
					Path:    constraintPath,
					Message: fmt.Sprintf("postdeploy ADD CONSTRAINT %q precedes normalization of %s", name, table),
				})
			}
		}
	}
	for key, validations := range postdeployValidations {
		for name, validationPath := range validations {
			if _, ok := expandConstraints[key][name]; ok {
				continue
			}
			if constraintPath, ok := postdeployConstraints[key][name]; ok {
				if migrationSequenceByPath[constraintPath] <= migrationSequenceByPath[validationPath] {
					continue
				}
				issues = append(issues, MigrationValidationIssue{
					Path:    validationPath,
					Message: fmt.Sprintf("postdeploy VALIDATE CONSTRAINT %q precedes its ADD CONSTRAINT ... NOT VALID", name),
				})
				continue
			}
			issues = append(issues, MigrationValidationIssue{
				Path:    validationPath,
				Message: fmt.Sprintf("postdeploy VALIDATE CONSTRAINT %q has no same-release ADD CONSTRAINT ... NOT VALID", name),
			})
		}
	}
	for key, constraints := range postdeployConstraints {
		for name, constraintPath := range constraints {
			if _, ok := postdeployValidations[key][name]; ok {
				continue
			}
			issues = append(issues, MigrationValidationIssue{
				Path:    constraintPath,
				Message: fmt.Sprintf("postdeploy NOT VALID constraint %q requires a same-release postdeploy VALIDATE CONSTRAINT", name),
			})
		}
	}
	if len(issues) > 0 {
		return &MigrationValidationError{Issues: issues}
	}
	return nil
}

func constraintNames(pattern *regexp.Regexp, content string) []string {
	matches := pattern.FindAllStringSubmatch(content, -1)
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 2 {
			names = append(names, strings.ToLower(match[1]))
		}
	}
	return names
}

// immediateMigrationSQL hides only registered trigger routines in a DDL-only
// migration. Any executable statement keeps bodies visible: it could invoke a
// routine directly or fire a newly installed trigger.
func immediateMigrationSQL(content string) string {
	statements, err := sqlStatements(content)
	if err != nil {
		return content
	}
	registered := map[string]bool{}
	for _, statement := range statements {
		if len(statement) == 0 {
			continue
		}
		if !isSQLWord(statement[0], "create") && !isSQLWord(statement[0], "alter") && !isSQLWord(statement[0], "comment") && !isSQLWord(statement[0], "grant") && !isSQLWord(statement[0], "revoke") {
			return content
		}

		// CREATE ... AS executes its query, which can fire registered triggers.
		if migrationCreatesRelationAsQuery(statement) {
			return content
		}
		i := 1
		if len(statement) > 3 && isSQLWord(statement[1], "or") && isSQLWord(statement[2], "replace") {
			i = 3
		}
		if len(statement) <= i || !isSQLWord(statement[0], "create") || !isSQLWord(statement[i], "trigger") {
			continue
		}
		for j := i + 1; j+2 < len(statement); j++ {
			if isSQLWord(statement[j], "execute") && (isSQLWord(statement[j+1], "function") || isSQLWord(statement[j+1], "procedure")) {
				if name, _, ok := qualifiedName(statement, j+2); ok {
					registered[name] = true
				}
			}
		}
	}
	var executed strings.Builder
	for _, statement := range statements {
		i := 1
		if len(statement) > 3 && isSQLWord(statement[1], "or") && isSQLWord(statement[2], "replace") {
			i = 3
		}
		if len(statement) > i && isSQLWord(statement[0], "create") && isSQLWord(statement[i], "function") {
			name, _, ok := qualifiedName(statement, i+1)
			if ok && registered[name] && hasWordPair(statement, "returns", "trigger") {
				continue
			}
		}
		if len(statement) > 0 {
			executed.WriteString(content[statement[0].start:statement[len(statement)-1].end])
			executed.WriteByte(';')
		}
	}
	return executed.String()
}

// executingMigrationSQL excludes stored definitions from direct data-operation
// checks when the script only declares DDL. A call, DO, DML or CTAS keeps routine
// bodies visible because it may execute them. Routine definitions also undergo
// the separate rewrite checks in expandMigrationSafetySQL.
func executingMigrationSQL(content string) string {
	statements, err := sqlStatements(content)
	if err != nil {
		return content
	}
	executable := false
	for _, statement := range statements {
		if len(statement) == 0 {
			continue
		}
		if migrationCreatesRelationAsQuery(statement) || (!isSQLWord(statement[0], "create") && !isSQLWord(statement[0], "alter") && !isSQLWord(statement[0], "comment") && !isSQLWord(statement[0], "grant") && !isSQLWord(statement[0], "revoke")) {
			executable = true
		}
	}
	created := map[string]bool{}
	var direct strings.Builder
	for _, statement := range statements {
		if len(statement) == 0 {
			continue
		}
		if !executable && isSQLWord(statement[0], "create") && (hasSQLWord(statement, "function") || hasSQLWord(statement, "procedure")) {
			continue
		}
		// IF NOT EXISTS may name a live table. Only an unconditional creation
		// proves the destination is new, and only for subsequent statements.
		if len(statement) > 2 && isSQLWord(statement[0], "create") {
			tableAt := 1
			if isSQLWord(statement[tableAt], "unlogged") {
				tableAt++
			}
			if tableAt+1 < len(statement) && isSQLWord(statement[tableAt], "table") && !isSQLWord(statement[tableAt+1], "if") {
				if name, _, ok := qualifiedName(statement, tableAt+1); ok {
					created[name] = true
				}
			}
		}

		insertAt := -1
		for i, tok := range statement {
			if isSQLWord(tok, "insert") {
				if insertAt >= 0 {
					insertAt = -1
					break
				}
				insertAt = i
			}
		}
		if insertAt >= 0 && insertAt+2 < len(statement) && isSQLWord(statement[insertAt+1], "into") {
			target, _, ok := qualifiedName(statement, insertAt+2)
			if ok && created[target] {
				conflict, end := initializationConflictClause(statement, insertAt)
				if conflict >= 0 {
					direct.WriteString(content[statement[0].start:statement[conflict].start])
					if end < len(statement) {
						direct.WriteString(content[statement[end].start:statement[len(statement)-1].end])
					}
					direct.WriteByte(';')
					continue
				}
			}
		}
		direct.WriteString(content[statement[0].start:statement[len(statement)-1].end])
		direct.WriteByte(';')
	}
	return expandMigrationSafetySQL(direct.String())
}

// initializationConflictClause bounds only the destination's ON CONFLICT action.
// RETURNING expressions, enclosing CTEs and the main statement remain inspected.
func initializationConflictClause(statement []sqlToken, insertAt int) (int, int) {
	depth, conflict, conflictDepth := 0, -1, 0
	for i := insertAt; i < len(statement); i++ {
		tok := statement[i]
		if conflict >= 0 && depth == conflictDepth && (tok.text == ")" || isSQLWord(tok, "returning")) {
			return conflict, i
		}
		if tok.text == "(" {
			depth++
		}
		if tok.text == ")" {
			depth--
		}
		if conflict < 0 && i+1 < len(statement) && isSQLWord(tok, "on") && isSQLWord(statement[i+1], "conflict") {
			conflict, conflictDepth = i, depth
		}
	}
	return conflict, len(statement)
}

// migrationCreatesRelationAsQuery distinguishes an executing CREATE ... AS query
// from AS inside a column definition or a routine returning TABLE.
func migrationCreatesRelationAsQuery(statement []sqlToken) bool {
	if len(statement) < 3 || !isSQLWord(statement[0], "create") {
		return false
	}
	i := 1
	for i < len(statement) && (isSQLWord(statement[i], "temporary") || isSQLWord(statement[i], "temp") || isSQLWord(statement[i], "unlogged") || isSQLWord(statement[i], "global") || isSQLWord(statement[i], "local")) {
		i++
	}
	if i+1 < len(statement) && isSQLWord(statement[i], "materialized") && isSQLWord(statement[i+1], "view") {
		i += 2
	} else if i < len(statement) && isSQLWord(statement[i], "table") {
		i++
	} else {
		return false
	}
	if i+2 < len(statement) && isSQLWord(statement[i], "if") && isSQLWord(statement[i+1], "not") && isSQLWord(statement[i+2], "exists") {
		i += 3
	}
	_, next, ok := qualifiedName(statement, i)
	if !ok {
		return false
	}
	depth := 0
	for _, tok := range statement[next:] {
		if tok.kind == sqlTokenPunct {
			if tok.text == "(" {
				depth++
			}
			if tok.text == ")" {
				depth--
			}
		}
		if depth == 0 && isSQLWord(tok, "as") {
			return true
		}
	}
	return false
}

// expandMigrationSafetySQL exposes executable routine bodies while keeping data
// literals and diagnostics out of the rewrite checks. Quoted identifiers become
// ordinary identifier tokens so quoting cannot hide a write target. This is an
// authoring-mistake guard, not a sandbox for hostile SQL: encoded strings and
// constructed EXECUTE expressions and volatile DEFAULT functions require code review.
func expandMigrationSafetySQL(content string) string {
	content = immediateMigrationSQL(content)
	statements, err := sqlStatements(content)
	if err != nil {
		return content
	}
	var out strings.Builder
	for _, statement := range statements {
		routine := len(statement) > 0 && (isSQLWord(statement[0], "do") ||
			(isSQLWord(statement[0], "create") && (hasSQLWord(statement, "function") || hasSQLWord(statement, "procedure"))))
		for i, tok := range statement {
			switch tok.kind {
			case sqlTokenString, sqlTokenDollarBody:
				body := routine && (isSQLWord(statement[0], "do") || (i > 0 && isSQLWord(statement[i-1], "as"))) || (i > 0 && isSQLWord(statement[i-1], "execute"))
				if body {
					code := tok.text
					if tok.kind == sqlTokenString {
						raw := content[tok.start:tok.end]
						start := strings.IndexByte(raw, '\'')
						code = strings.ReplaceAll(raw[start+1:len(raw)-1], "''", "'")
					}
					out.WriteString(expandMigrationSafetySQL(code))
				}
			case sqlTokenQuotedIdentifier:
				out.WriteString("quoted_identifier")
			default:
				out.WriteString(tok.text)
			}
			out.WriteByte(' ')
		}
		out.WriteByte(';')
	}
	return out.String()
}

func hasSQLWord(tokens []sqlToken, word string) bool {
	for _, tok := range tokens {
		if isSQLWord(tok, word) {
			return true
		}
	}
	return false
}

// stripSQLSingleQuotedLiterals prevents comments, diagnostics, or dynamic SQL
// strings from satisfying structural migration rules. Dollar-quoted DO bodies
// remain visible because they contain the statements the migration executes.
func stripSQLSingleQuotedLiterals(content string) string {
	var out strings.Builder
	out.Grow(len(content))
	var dollarTag string
	inSingle := false
	for i := 0; i < len(content); {
		if dollarTag != "" && strings.HasPrefix(content[i:], dollarTag) {
			out.WriteString(dollarTag)
			i += len(dollarTag)
			dollarTag = ""
			inSingle = false
			continue
		}
		if !inSingle && dollarTag == "" && content[i] == '$' {
			if tag, ok := readDollarTag(content[i:]); ok {
				out.WriteString(tag)
				i += len(tag)
				dollarTag = tag
				continue
			}
		}
		if !inSingle && content[i] != '\'' {
			out.WriteByte(content[i])
			i++
			continue
		}
		if !inSingle {
			inSingle = true
			out.WriteByte(' ')
			i++
			continue
		}
		if content[i] == '\\' && i+1 < len(content) {
			out.WriteString("  ")
			i += 2
			continue
		}
		if content[i] == '\'' {
			if i+1 < len(content) && content[i+1] == '\'' {
				out.WriteString("  ")
				i += 2
				continue
			}
			inSingle = false
		}
		out.WriteByte(' ')
		i++
	}
	return out.String()
}

type addedConstraint struct {
	name     string
	notValid bool
}

func addedConstraints(statement string) []addedConstraint {
	matches := addConstraintName.FindAllStringSubmatchIndex(statement, -1)
	constraints := make([]addedConstraint, 0, len(matches))
	for i, match := range matches {
		end := len(statement)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		constraints = append(constraints, addedConstraint{
			name:     strings.ToLower(statement[match[2]:match[3]]),
			notValid: notValidPattern.MatchString(statement[match[0]:end]),
		})
	}
	return constraints
}

func hasConcurrentIndexGuard(statements []string, indexName string) bool {
	for _, statement := range statements {
		structural := strings.ToLower(stripSQLSingleQuotedLiterals(statement))
		if strings.Contains(structural, "pg_index") && strings.Contains(structural, "indisvalid") &&
			strings.Contains(structural, "indisready") && statementReferencesRegclass(statement, indexName) {
			return true
		}
	}
	return false
}

func statementReferencesRegclass(statement, indexName string) bool {
	for _, pattern := range []*regexp.Regexp{toRegclassReference, castRegclassReference, castNameReference} {
		for _, match := range pattern.FindAllStringSubmatch(statement, -1) {
			if len(match) != 2 {
				continue
			}
			qualified := strings.Trim(strings.ToLower(strings.TrimSpace(match[1])), `"`)
			parts := strings.Split(qualified, ".")
			if strings.Trim(parts[len(parts)-1], `"`) == indexName {
				return true
			}
		}
	}
	return false
}

// validateSequenceCollisions reports duplicate (database, version, phase,
// sequence) keys. Shared between Postgres and ClickHouse validation since the
// embed-tree layout is the same.
func validateSequenceCollisions(migrations []Migration) []MigrationValidationIssue {
	var issues []MigrationValidationIssue
	seen := map[string]string{}
	for _, migration := range migrations {
		key := strings.Join([]string{
			migration.Database,
			migration.Version,
			migration.Phase,
			strconv.Itoa(migration.Sequence),
		}, ":")
		if existing, ok := seen[key]; ok {
			issues = append(issues, MigrationValidationIssue{
				Path:    migration.Path,
				Message: fmt.Sprintf("sequence collides with %s", existing),
			})
		}
		seen[key] = migration.Path
	}
	return issues
}

// ClickHouse-specific validation rules. Day-one rules:
//   - DROP TABLE/COLUMN/VIEW/DICTIONARY → contract only
//   - RENAME → contract only
//   - ALTER TABLE … MODIFY COLUMN <name> <newtype> (type rewrites) → postdeploy only
//   - ALTER TABLE … UPDATE/DELETE (mutations) → postdeploy/contract only
//   - CREATE TABLE/VIEW/DICTIONARY without IF NOT EXISTS → reject (idempotent
//     re-apply is the reconciliation contract; missing IF NOT EXISTS breaks it)
var (
	chDropPattern         = regexp.MustCompile(`(?is)\bDROP\s+(TABLE|COLUMN|VIEW|DICTIONARY|DATABASE)\b`)
	chRenamePattern       = regexp.MustCompile(`(?is)\bRENAME\s+(TABLE|COLUMN|DICTIONARY)\b`)
	chModifyTypePattern   = regexp.MustCompile(`(?is)\bALTER\s+TABLE\b[^;]*\bMODIFY\s+COLUMN\b`)
	chMutationPattern     = regexp.MustCompile(`(?is)\bALTER\s+TABLE\b[^;]*\b(UPDATE|DELETE)\b`)
	chCreateObjectPattern = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?(?:MATERIALIZED\s+)?(?:TABLE|VIEW|DICTIONARY)\b`)
	chCreateOrReplaceView = regexp.MustCompile(`(?is)\bCREATE\s+OR\s+REPLACE\s+VIEW\b`)
	chIfNotExistsPattern  = regexp.MustCompile(`(?is)\bIF\s+NOT\s+EXISTS\b`)
	// Dictionary attribute DEFAULTs must be plain literals; ClickHouse
	// rejects expressions like `DEFAULT toDateTime(0)` with a SYNTAX_ERROR
	// at apply time (table-column DEFAULT expressions are fine).
	chCreateDictionaryPattern = regexp.MustCompile(`(?is)\bCREATE\s+DICTIONARY\b`)
	chDictExprDefaultPattern  = regexp.MustCompile(`(?i)\bDEFAULT\s+[A-Za-z_][A-Za-z0-9_]*\s*\(`)
)

func validateClickHouseMigrationSet(migrations []Migration) error {
	issues := validateSequenceCollisions(migrations)
	for _, migration := range migrations {
		content := migration.content

		if enforcesMigrationPhaseSafety(migration) {
			switch migration.Phase {
			case "expand":
				if chDropPattern.MatchString(content) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "DROP statements belong in contract migrations",
					})
				}
				if chRenamePattern.MatchString(content) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "RENAME statements belong in contract migrations",
					})
				}
				if chModifyTypePattern.MatchString(content) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "ALTER TABLE … MODIFY COLUMN type rewrites are heavy in ClickHouse and belong in postdeploy",
					})
				}
				if chMutationPattern.MatchString(content) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "ALTER TABLE UPDATE/DELETE mutations belong in postdeploy or contract",
					})
				}
			case "postdeploy":
				if chDropPattern.MatchString(content) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "DROP statements belong in contract migrations",
					})
				}
				if chRenamePattern.MatchString(content) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "RENAME statements belong in contract migrations",
					})
				}
			}
		}

		// Reconciliation requires IF NOT EXISTS on every CREATE so the same
		// migration re-applies cleanly on a freshly-baselined cluster.
		if enforcesMigrationPhaseSafety(migration) {
			for _, stmt := range splitSQLStatements(content) {
				if chCreateObjectPattern.MatchString(stmt) &&
					!chIfNotExistsPattern.MatchString(stmt) &&
					!chCreateOrReplaceView.MatchString(stmt) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "CREATE TABLE/VIEW/DICTIONARY must use IF NOT EXISTS for idempotent re-apply against an existing baseline",
					})
				}
				if chCreateDictionaryPattern.MatchString(stmt) && chDictExprDefaultPattern.MatchString(stmt) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "CREATE DICTIONARY attribute DEFAULTs must be plain literals (e.g. '1970-01-01 00:00:00', not toDateTime(0)); ClickHouse rejects expression defaults at apply time",
					})
				}
			}
		}
	}
	if len(issues) > 0 {
		return &MigrationValidationError{Issues: issues}
	}
	return nil
}

func IsMigrationValidationError(err error) bool {
	var validationErr *MigrationValidationError
	return errors.As(err, &validationErr)
}

func knownMigrationDatabases() (map[string]bool, error) {
	out := map[string]bool{}
	entries, err := fs.ReadDir(dbsql.Content, "schema")
	if err != nil {
		return nil, fmt.Errorf("read schema databases: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		out[strings.TrimSuffix(entry.Name(), ".sql")] = true
	}
	return out, nil
}

// knownClickHouseDatabases discovers ClickHouse databases by listing
// pkg/database/sql/clickhouse/<db>.sql baseline files. Used by both the
// migration discovery and the validate subcommand.
func knownClickHouseDatabases() (map[string]bool, error) {
	out := map[string]bool{}
	entries, err := fs.ReadDir(dbsql.Content, "clickhouse")
	if err != nil {
		return nil, fmt.Errorf("read clickhouse databases: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		out[strings.TrimSuffix(entry.Name(), ".sql")] = true
	}
	return out, nil
}

func parseSequence(filename string) int {
	// NNN_description.sql -> NNN
	idx := strings.Index(filename, "_")
	if idx <= 0 {
		return 0
	}
	n, _ := strconv.Atoi(filename[:idx]) //nolint:errcheck // best-effort parse, returns 0 on failure
	return n
}

func splitSQLStatements(content string) []string {
	var out []string
	var b strings.Builder
	var dollarTag string
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(content); i++ {
		c := content[i]
		next := byte(0)
		if i+1 < len(content) {
			next = content[i+1]
		}

		b.WriteByte(c)

		switch {
		case inLineComment:
			if c == '\n' {
				inLineComment = false
			}
			continue
		case inBlockComment:
			if c == '*' && next == '/' {
				b.WriteByte(next)
				i++
				inBlockComment = false
			}
			continue
		case dollarTag != "":
			if strings.HasPrefix(content[i:], dollarTag) {
				for j := 1; j < len(dollarTag); j++ {
					b.WriteByte(content[i+j])
				}
				i += len(dollarTag) - 1
				dollarTag = ""
			}
			continue
		case inSingle:
			if c == '\'' && next == '\'' {
				b.WriteByte(next)
				i++
				continue
			}
			if c == '\'' {
				inSingle = false
			}
			continue
		case inDouble:
			if c == '"' {
				inDouble = false
			}
			continue
		}

		if c == '-' && next == '-' {
			b.WriteByte(next)
			i++
			inLineComment = true
			continue
		}
		if c == '/' && next == '*' {
			b.WriteByte(next)
			i++
			inBlockComment = true
			continue
		}
		if c == '\'' {
			inSingle = true
			continue
		}
		if c == '"' {
			inDouble = true
			continue
		}
		if c == '$' {
			if tag, ok := readDollarTag(content[i:]); ok {
				dollarTag = tag
				for j := 1; j < len(tag); j++ {
					b.WriteByte(content[i+j])
				}
				i += len(tag) - 1
				continue
			}
		}
		if c == ';' {
			stmt := strings.TrimSpace(b.String())
			if stmt != "" {
				out = append(out, stmt)
			}
			b.Reset()
		}
	}
	if stmt := strings.TrimSpace(b.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

func stripSQLComments(content string) string {
	var b strings.Builder
	var dollarTag string
	inSingle := false
	inDouble := false
	inDollarSingle := false
	inDollarDouble := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(content); i++ {
		c := content[i]
		next := byte(0)
		if i+1 < len(content) {
			next = content[i+1]
		}

		switch {
		case inLineComment:
			if c == '\n' {
				b.WriteByte(c)
				inLineComment = false
			}
			continue
		case inBlockComment:
			if c == '*' && next == '/' {
				b.WriteByte(' ')
				i++
				inBlockComment = false
			}
			continue
		case dollarTag != "":
			if strings.HasPrefix(content[i:], dollarTag) {
				b.WriteByte(c)
				for j := 1; j < len(dollarTag); j++ {
					b.WriteByte(content[i+j])
				}
				i += len(dollarTag) - 1
				dollarTag = ""
				inDollarSingle = false
				inDollarDouble = false
			} else if inDollarSingle {
				b.WriteByte(c)
				if c == '\'' && next == '\'' {
					b.WriteByte(next)
					i++
				} else if c == '\'' {
					inDollarSingle = false
				}
			} else if inDollarDouble {
				b.WriteByte(c)
				if c == '"' && next == '"' {
					b.WriteByte(next)
					i++
				} else if c == '"' {
					inDollarDouble = false
				}
			} else if c == '-' && next == '-' {
				i++
				inLineComment = true
			} else if c == '/' && next == '*' {
				i++
				inBlockComment = true
			} else {
				b.WriteByte(c)
				switch c {
				case '\'':
					inDollarSingle = true
				case '"':
					inDollarDouble = true
				}
			}
			continue
		case inSingle:
			b.WriteByte(c)
			if c == '\'' && next == '\'' {
				b.WriteByte(next)
				i++
			} else if c == '\'' {
				inSingle = false
			}
			continue
		case inDouble:
			b.WriteByte(c)
			if c == '"' {
				inDouble = false
			}
			continue
		}

		if c == '-' && next == '-' {
			i++
			inLineComment = true
			continue
		}
		if c == '/' && next == '*' {
			i++
			inBlockComment = true
			continue
		}
		b.WriteByte(c)
		switch c {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '$':
			if tag, ok := readDollarTag(content[i:]); ok {
				dollarTag = tag
				for j := 1; j < len(tag); j++ {
					b.WriteByte(content[i+j])
				}
				i += len(tag) - 1
			}
		}
	}
	return b.String()
}

func readDollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '$':
			return s[:i+1], true
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || (i > 1 && c >= '0' && c <= '9'):
			continue
		default:
			return "", false
		}
	}
	return "", false
}

// compareSemver compares two version strings like "v1.2.3".
// Returns -1 if a < b, 0 if equal, 1 if a > b.
// Falls back to lexicographic comparison on parse failure.
func compareSemver(a, b string) int {
	return releases.CompareSemver(a, b)
}
