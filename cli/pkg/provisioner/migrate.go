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
	dbsql "frameworks/pkg/database/sql"
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

var expandUnsafeSQLPatterns = []struct {
	re      *regexp.Regexp
	message string
}{
	{regexp.MustCompile(`(?is)\bDROP\s+(TABLE|COLUMN|SCHEMA|TYPE|INDEX)\b`), "drop operations belong in contract migrations"},
	{regexp.MustCompile(`(?is)\bALTER\s+TABLE\b.*\bRENAME\b`), "renames are not expand-compatible with old binaries"},
	{regexp.MustCompile(`(?is)\bALTER\s+TABLE\b.*\bALTER\s+COLUMN\b.*\bTYPE\b`), "column type rewrites are not expand-compatible"},
	{regexp.MustCompile(`(?is)\bALTER\s+TABLE\b.*\bSET\s+NOT\s+NULL\b`), "SET NOT NULL requires a completed data migration and postdeploy/contract gating"},
	{regexp.MustCompile(`(?is)\bUPDATE\s+[A-Za-z_][A-Za-z0-9_.]*\b`), "bulk data rewrites belong in service-owned background data migrations"},
	{regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+[A-Za-z_][A-Za-z0-9_.]*\b`), "bulk deletes belong in service-owned background data migrations or contract"},
}

var (
	expandAddConstraintPattern         = regexp.MustCompile(`(?is)\bALTER\s+TABLE\b.*\bADD\s+CONSTRAINT\b`)
	notValidPattern                    = regexp.MustCompile(`(?is)\bNOT\s+VALID\b`)
	createIndexConcurrently            = regexp.MustCompile(`(?is)\bCREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY\b`)
	createIndexConcurrentlyIfNotExists = regexp.MustCompile(`(?is)\bCREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\b`)
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

func ValidateEmbeddedMigrations() error {
	migrations, err := discoverAllMigrationsForValidation()
	if err != nil {
		return err
	}
	return validateMigrationSet(migrations)
}

// discoverAllMigrationsForValidation returns the full embedded set with no
// target-version cap. Only the validator should consume this — runtime callers
// must use BuildMigrationItems with a concrete targetVersion.
func discoverAllMigrationsForValidation() ([]Migration, error) {
	knownDBs, err := knownMigrationDatabases()
	if err != nil {
		return nil, err
	}
	return discoverMigrationsInFS(dbsql.Content, "migrations", knownDBs)
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

func validateMigrationSet(migrations []Migration) error {
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

		content := migration.content
		if migration.Phase == "expand" {
			for _, pattern := range expandUnsafeSQLPatterns {
				if pattern.re.MatchString(content) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: pattern.message,
					})
				}
			}
			for _, stmt := range splitSQLStatements(content) {
				if expandAddConstraintPattern.MatchString(stmt) && !notValidPattern.MatchString(stmt) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: "new constraints in expand must be NOT VALID or moved to a later phase",
					})
				}
			}
		}

		hasConcurrentIndex := createIndexConcurrently.MatchString(content)
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
			for _, stmt := range splitSQLStatements(content) {
				if createIndexConcurrently.MatchString(stmt) && !createIndexConcurrentlyIfNotExists.MatchString(stmt) {
					issues = append(issues, MigrationValidationIssue{
						Path:    migration.Path,
						Message: ".notx.sql with CREATE INDEX CONCURRENTLY must use IF NOT EXISTS so partial-failure reruns are safe",
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
