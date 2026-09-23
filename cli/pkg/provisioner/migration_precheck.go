package provisioner

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// A migration precheck is a read-only SELECT that returns the rows a migration item would fail on. It lives at
// prechecks/<database>/<version>/<phase>/<migration filename>, mirroring the item's path under migrations/, so a
// shipped migration gains a precheck without changing its bytes or its ledger checksum. The roles run it in a READ
// ONLY transaction immediately before the item and apply nothing when it returns rows.
const (
	migrationPrecheckRoot = "prechecks"
	// migrationPrecheckReportLimit bounds the rows a failed precheck prints; the total is reported separately.
	migrationPrecheckReportLimit = 50
)

type migrationPrecheck struct {
	// Path is the precheck file's embedded path.
	Path string
	// Query wraps the precheck in the report the roles run: the total row count and each row as JSON text, capped at
	// migrationPrecheckReportLimit rows.
	Query string
}

// migrationPrecheckMigrationPath returns the embedded path of the migration a precheck file is keyed to.
func migrationPrecheckMigrationPath(precheckPath string) string {
	return "migrations/" + strings.TrimPrefix(precheckPath, migrationPrecheckRoot+"/")
}

// migrationPrecheckSelect returns the precheck's SELECT without its trailing semicolon or comments. A precheck is
// exactly one SELECT with no INTO or row-locking clause and no dollar-quoted body; the READ ONLY transaction it runs
// in rejects any write a called function would attempt.
func migrationPrecheckSelect(src string) (string, error) {
	statements, err := sqlStatements(src)
	if err != nil {
		return "", err
	}
	if len(statements) != 1 {
		return "", fmt.Errorf("precheck must be a single SELECT statement, found %d statements", len(statements))
	}
	statement := statements[0]
	if !isSQLWord(statement[0], "select") {
		return "", errors.New("precheck must be a single SELECT statement")
	}
	for i, tok := range statement {
		switch {
		case tok.kind == sqlTokenDollarBody:
			return "", errors.New("precheck must not contain a dollar-quoted body")
		case isSQLWord(tok, "into"):
			return "", errors.New("precheck must not use SELECT ... INTO")
		case isSQLWord(tok, "for") && i+1 < len(statement) &&
			(isSQLWord(statement[i+1], "update") || isSQLWord(statement[i+1], "share") ||
				isSQLWord(statement[i+1], "no") || isSQLWord(statement[i+1], "key")):
			return "", errors.New("precheck must not take row locks")
		}
	}
	return strings.TrimSpace(src[statement[0].start:statement[len(statement)-1].end]), nil
}

// migrationPrecheckReportQuery wraps a precheck SELECT so one statement returns both the total and the first rows.
// The window count is computed before LIMIT, so precheck_total counts every row.
func migrationPrecheckReportQuery(selectSQL string) string {
	return "SELECT count(*) OVER () AS precheck_total, row_to_json(precheck)::text AS precheck_row\nFROM (\n" +
		selectSQL + "\n) AS precheck\nLIMIT " + strconv.Itoa(migrationPrecheckReportLimit)
}

// loadMigrationPrechecks returns the prechecks under fsys keyed by the embedded path of their migration.
func loadMigrationPrechecks(fsys fs.FS) (map[string]migrationPrecheck, error) {
	prechecks := map[string]migrationPrecheck{}
	if _, err := fs.Stat(fsys, migrationPrecheckRoot); errors.Is(err, fs.ErrNotExist) {
		return prechecks, nil
	}
	err := fs.WalkDir(fsys, migrationPrecheckRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if path.Ext(p) != ".sql" {
			return fmt.Errorf("precheck %s: precheck files must be .sql", p)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		selectSQL, err := migrationPrecheckSelect(string(data))
		if err != nil {
			return fmt.Errorf("precheck %s: %w", p, err)
		}
		prechecks[migrationPrecheckMigrationPath(p)] = migrationPrecheck{Path: p, Query: migrationPrecheckReportQuery(selectSQL)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return prechecks, nil
}

func embeddedMigrationPrechecks() (map[string]migrationPrecheck, error) {
	return loadMigrationPrechecks(dbsql.Content)
}

// validateMigrationPrechecks reports precheck files that are not a single SELECT, and prechecks keyed to no migration
// item: a missing migration file, or one below the schema floor that is never offered.
func validateMigrationPrechecks(fsys fs.FS, migrations []Migration) []MigrationValidationIssue {
	byPath := make(map[string]Migration, len(migrations))
	for _, migration := range migrations {
		byPath[migration.Path] = migration
	}
	var issues []MigrationValidationIssue
	if _, err := fs.Stat(fsys, migrationPrecheckRoot); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	err := fs.WalkDir(fsys, migrationPrecheckRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if path.Ext(p) != ".sql" {
			issues = append(issues, MigrationValidationIssue{Path: p, Message: "precheck files must be .sql"})
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if _, err := migrationPrecheckSelect(string(data)); err != nil {
			issues = append(issues, MigrationValidationIssue{Path: p, Message: err.Error()})
		}
		migrationPath := migrationPrecheckMigrationPath(p)
		migration, ok := byPath[migrationPath]
		switch {
		case !ok:
			issues = append(issues, MigrationValidationIssue{Path: p, Message: fmt.Sprintf("precheck names no migration item: %s does not exist", migrationPath)})
		case belowBaselineFloor(migration):
			issues = append(issues, MigrationValidationIssue{Path: p, Message: fmt.Sprintf("precheck names %s, which is below the schema floor %s and never applied", migrationPath, schemaMigrationBaselineFloor)})
		}
		return nil
	})
	if err != nil {
		issues = append(issues, MigrationValidationIssue{Path: migrationPrecheckRoot, Message: err.Error()})
	}
	return issues
}
