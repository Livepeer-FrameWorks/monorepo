package provisioner

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func rerunSafetyMigration(content string) Migration {
	return Migration{
		Database: "purser", Version: "v0.3.11", Phase: "expand", Sequence: 900, Filename: "900_rerun.sql",
		Path: "migrations/purser/v0.3.11/expand/900_rerun.sql", Transactional: true,
		Checksum: fmt.Sprintf("%x", sha256.Sum256([]byte(content))), content: content,
	}
}

// TestMigrationRerunSafetyRejectsStatementsThatFailOrRepeatOnRerun lists the statement forms that fail, or change data
// again, when YugabyteDB reruns an unrecorded item over the statements it already applied.
func TestMigrationRerunSafetyRejectsStatementsThatFailOrRepeatOnRerun(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"CREATE TABLE purser.t (id int PRIMARY KEY);", "CREATE TABLE must use IF NOT EXISTS"},
		{"CREATE SEQUENCE purser.s;", "CREATE SEQUENCE must use IF NOT EXISTS"},
		{"CREATE UNIQUE INDEX idx_t ON purser.t (id);", "CREATE INDEX idx_t must use IF NOT EXISTS"},
		{"CREATE TYPE purser.k AS ENUM ('a');", "CREATE TYPE purser.k"},
		{"CREATE VIEW purser.v AS SELECT 1;", "CREATE VIEW purser.v"},
		{"CREATE TRIGGER trg AFTER INSERT ON purser.t FOR EACH ROW EXECUTE FUNCTION purser.f();", "CREATE TRIGGER trg must follow"},
		{"DROP TRIGGER trg ON purser.t; CREATE TRIGGER trg AFTER INSERT ON purser.t FOR EACH ROW EXECUTE FUNCTION purser.f();", "DROP TRIGGER must use IF EXISTS"},
		{"DROP TRIGGER IF EXISTS trg ON purser.other; CREATE TRIGGER trg AFTER INSERT ON purser.t FOR EACH ROW EXECUTE FUNCTION purser.f();", "CREATE TRIGGER trg must follow"},
		{"DROP TABLE purser.t;", "DROP TABLE must use IF EXISTS"},
		{"DROP INDEX CONCURRENTLY purser.idx_t;", "DROP INDEX must use IF EXISTS"},
		{"ALTER TABLE purser.t ADD COLUMN c int;", "ADD COLUMN on purser.t must use IF NOT EXISTS"},
		{"ALTER TABLE purser.t ADD c int;", "ADD COLUMN on purser.t must use IF NOT EXISTS"},
		{"ALTER TABLE purser.t ADD CONSTRAINT chk_c CHECK (c > 0) NOT VALID;", "ADD CONSTRAINT chk_c on purser.t must follow"},
		{"ALTER TABLE purser.t DROP CONSTRAINT IF EXISTS chk_old, ADD CONSTRAINT chk_new CHECK (c > 0);", "ADD CONSTRAINT chk_new"},
		{"ALTER TABLE purser.other DROP CONSTRAINT IF EXISTS chk_c; ALTER TABLE purser.t ADD CONSTRAINT chk_c CHECK (c > 0);", "ADD CONSTRAINT chk_c on purser.t"},
		{"ALTER TABLE purser.t ADD CONSTRAINT chk_c CHECK (c > 0); ALTER TABLE purser.t DROP CONSTRAINT IF EXISTS chk_c;", "ADD CONSTRAINT chk_c on purser.t"},
		{"ALTER TABLE purser.t ADD CHECK (c > 0);", "an unnamed constraint on purser.t"},
		{"ALTER TABLE purser.t ADD PRIMARY KEY (id);", "ADD PRIMARY KEY on purser.t must follow DROP CONSTRAINT IF EXISTS t_pkey"},
		{"ALTER TABLE purser.t DROP CONSTRAINT chk_c;", "DROP CONSTRAINT on purser.t must use IF EXISTS"},
		{"ALTER TABLE purser.t DROP COLUMN c;", "DROP COLUMN on purser.t must use IF EXISTS"},
		{"ALTER TABLE purser.t RENAME COLUMN a TO b;", "RENAME on purser.t"},
		{"ALTER TABLE purser.t RENAME TO u;", "RENAME on purser.t"},
		{"ALTER TABLE purser.t SET SCHEMA other;", "SET SCHEMA on purser.t"},
		{"ALTER TABLE purser.t ATTACH PARTITION purser.p FOR VALUES IN (1);", "ATTACH is not recognized"},
		{"ALTER INDEX purser.idx_a RENAME TO idx_b;", "ALTER INDEX ... RENAME"},
		{"ALTER TYPE purser.k ADD VALUE 'b';", "ADD VALUE must use IF NOT EXISTS"},
		{"INSERT INTO purser.t (id) VALUES (1);", "INSERT must use ON CONFLICT"},
		{"WITH src AS (SELECT 1 AS id) INSERT INTO purser.t (id) SELECT id FROM src;", "INSERT must use ON CONFLICT"},
		{"UPDATE purser.t SET n = n + 1 WHERE id = 1;", "UPDATE steps n by a literal"},
		{"UPDATE purser.t SET note = note || 'x';", "UPDATE steps note by a literal"},
		{"UPDATE purser.t SET n = 1 - n;", "UPDATE steps n by a literal"},
		{"LOCK TABLE purser.t;", "LOCK statements are not recognized"},
	} {
		issues := validateMigrationRerunSafety([]Migration{rerunSafetyMigration(tc.sql)})
		joined := fmt.Sprint(issues)
		if tc.want == "" {
			if len(issues) != 0 {
				t.Errorf("%q: unexpected issues %s", tc.sql, joined)
			}
			continue
		}
		if len(issues) == 0 || !strings.Contains(joined, tc.want) {
			t.Errorf("%q: issues %s, want one containing %q", tc.sql, joined, tc.want)
		}
	}
}

// TestMigrationRerunSafetyAcceptsIdempotentStatements lists the forms that converge when executed again.
func TestMigrationRerunSafetyAcceptsIdempotentStatements(t *testing.T) {
	for _, sql := range []string{
		"CREATE TABLE IF NOT EXISTS purser.t (id int PRIMARY KEY);",
		"CREATE SCHEMA IF NOT EXISTS purser; CREATE SEQUENCE IF NOT EXISTS purser.s; CREATE EXTENSION IF NOT EXISTS pgcrypto;",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_t ON purser.t (id);",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_t ON purser.t (id);",
		"DROP INDEX IF EXISTS purser.idx_t; CREATE UNIQUE INDEX idx_t ON purser.t (id);",
		"CREATE OR REPLACE FUNCTION purser.f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO purser.log VALUES (1); RETURN NEW; END $$;",
		"CREATE OR REPLACE TRIGGER trg AFTER INSERT ON purser.t FOR EACH ROW EXECUTE FUNCTION purser.f();",
		"DROP TRIGGER IF EXISTS trg ON purser.t; CREATE TRIGGER trg AFTER INSERT ON purser.t FOR EACH ROW EXECUTE FUNCTION purser.f();",
		"DROP VIEW IF EXISTS purser.v; CREATE VIEW purser.v AS SELECT 1;",
		"ALTER TABLE purser.t ADD COLUMN IF NOT EXISTS c int, ADD IF NOT EXISTS d int;",
		"ALTER TABLE purser.t DROP CONSTRAINT IF EXISTS chk_c; ALTER TABLE purser.t ADD CONSTRAINT chk_c CHECK (c > 0) NOT VALID;",
		"ALTER TABLE t DROP CONSTRAINT IF EXISTS chk_c, ADD CONSTRAINT chk_c CHECK (c > 0);",
		"ALTER TABLE purser.t DROP CONSTRAINT IF EXISTS t_pkey; ALTER TABLE purser.t ADD PRIMARY KEY (id);",
		"ALTER TABLE purser.t VALIDATE CONSTRAINT chk_c;",
		"ALTER TABLE ONLY purser.t ALTER COLUMN c SET NOT NULL, ALTER COLUMN d SET DEFAULT 0, ALTER COLUMN e TYPE text;",
		"ALTER TABLE purser.t DROP COLUMN IF EXISTS c, DROP IF EXISTS d;",
		"ALTER SEQUENCE purser.s INCREMENT BY 1; ALTER INDEX purser.idx_t SET (fillfactor = 90);",
		"ALTER TYPE purser.k ADD VALUE IF NOT EXISTS 'b';",
		"INSERT INTO purser.t (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET n = EXCLUDED.n;",
		"INSERT INTO purser.t (id) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM purser.t WHERE id = 1);",
		"WITH src AS (SELECT 1 AS id) INSERT INTO purser.t (id) SELECT id FROM src ON CONFLICT DO NOTHING;",
		"WITH stale AS (SELECT id FROM purser.t) UPDATE purser.t SET state = 'done' FROM stale WHERE t.id = stale.id;",
		"UPDATE purser.t SET features = features - ARRAY['a'], updated_at = NOW() WHERE features ?| ARRAY['a'];",
		"UPDATE purser.t AS t SET n = s.n + 1 FROM purser.s AS s WHERE s.id = t.id;",
		"DELETE FROM purser.t WHERE expired; TRUNCATE purser.scratch;",
		"DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'x') THEN ALTER TABLE purser.t ADD CONSTRAINT x CHECK (true); END IF; END $$;",
		"COMMENT ON COLUMN purser.t.c IS 'renamed'; GRANT SELECT ON purser.t TO purser_runtime; SELECT setval('purser.s', 10);",
	} {
		if issues := validateMigrationRerunSafety([]Migration{rerunSafetyMigration(sql)}); len(issues) != 0 {
			t.Errorf("%q: %v", sql, issues)
		}
	}
}

func TestMigrationRerunSafetySkipsMigrationsBelowTheFloor(t *testing.T) {
	migration := rerunSafetyMigration("CREATE TABLE purser.t (id int);")
	migration.Version = "v0.2.96"
	if issues := validateMigrationRerunSafety([]Migration{migration}); len(issues) != 0 {
		t.Fatalf("below-floor migration checked: %v", issues)
	}
}

// TestRerunGuardsExecuteShippedStatementsUnderEquivalentConditions pins the SQL each guard kind produces and that a
// guarded file validates, while a guard pinned to other bytes, a guard on a statement it does not describe, and a
// guard on a statement that is already rerun-safe are rejected.
func TestRerunGuardsExecuteShippedStatementsUnderEquivalentConditions(t *testing.T) {
	saved := yugabyteRerunGuards
	t.Cleanup(func() { yugabyteRerunGuards = saved })

	content := "ALTER TABLE purser.t ADD CONSTRAINT chk_c CHECK (c IN ('a', 'b')) NOT VALID;\n" +
		"DROP TRIGGER trg ON purser.t;\n" +
		"CREATE TRIGGER trg AFTER INSERT ON purser.t FOR EACH ROW EXECUTE FUNCTION purser.f();\n" +
		"DROP INDEX IF EXISTS purser.idx_final;\n" +
		"ALTER INDEX purser.idx_final_v2 RENAME TO idx_final;\n"
	migration := rerunSafetyMigration(content)
	guards := []rerunGuard{{1, rerunGuardConstraintAbsent}, {2, rerunGuardDropTriggerIfExists}, {5, rerunGuardIndexRenamePending}}
	yugabyteRerunGuards = map[string]rerunGuardedFile{migration.Path: {checksum: migration.Checksum, guards: guards}}

	if issues := validateMigrationRerunSafety([]Migration{migration}); len(issues) != 0 {
		t.Fatalf("guarded file: %v", issues)
	}
	statements, _, err := migrationStatements(migration, SQLEngineYugabyte)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"DO $frameworks_rerun_guard$\nBEGIN\n  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = to_regclass('purser.t') AND conname = 'chk_c') THEN\n    EXECUTE $frameworks_rerun_statement$ALTER TABLE purser.t ADD CONSTRAINT chk_c CHECK (c IN ('a', 'b')) NOT VALID$frameworks_rerun_statement$;\n  END IF;\nEND\n$frameworks_rerun_guard$",
		"DROP TRIGGER IF EXISTS trg ON purser.t",
		"CREATE TRIGGER trg AFTER INSERT ON purser.t FOR EACH ROW EXECUTE FUNCTION purser.f()",
		"DO $frameworks_rerun_guard$\nBEGIN\n  IF to_regclass('purser.idx_final_v2') IS NOT NULL THEN\n    EXECUTE $frameworks_rerun_statement$DROP INDEX IF EXISTS purser.idx_final$frameworks_rerun_statement$;\n  END IF;\nEND\n$frameworks_rerun_guard$",
		"DO $frameworks_rerun_guard$\nBEGIN\n  IF to_regclass('purser.idx_final_v2') IS NOT NULL THEN\n    EXECUTE $frameworks_rerun_statement$ALTER INDEX purser.idx_final_v2 RENAME TO idx_final$frameworks_rerun_statement$;\n  END IF;\nEND\n$frameworks_rerun_guard$",
	}
	if strings.Join(statements, "\n;;\n") != strings.Join(want, "\n;;\n") {
		t.Fatalf("guarded statements:\n%s\nwant:\n%s", strings.Join(statements, "\n;;\n"), strings.Join(want, "\n;;\n"))
	}
	if postgres, _, err := migrationStatements(migration, SQLEnginePostgres); err != nil || len(postgres) != 1 || postgres[0] != content {
		t.Fatalf("PostgreSQL statements = %q, %v; want the file unchanged as one script", postgres, err)
	}

	for name, entry := range map[string]rerunGuardedFile{
		"other bytes":           {checksum: strings.Repeat("0", 64), guards: guards},
		"wrong statement kind":  {checksum: migration.Checksum, guards: []rerunGuard{{3, rerunGuardConstraintAbsent}}},
		"statement out of file": {checksum: migration.Checksum, guards: []rerunGuard{{6, rerunGuardDropTriggerIfExists}}},
		"already rerun-safe":    {checksum: migration.Checksum, guards: append(append([]rerunGuard{}, guards...), rerunGuard{4, rerunGuardConstraintAbsent})},
	} {
		yugabyteRerunGuards = map[string]rerunGuardedFile{migration.Path: entry}
		if issues := validateMigrationRerunSafety([]Migration{migration}); len(issues) == 0 {
			t.Errorf("%s: guard accepted", name)
		}
	}
	yugabyteRerunGuards = map[string]rerunGuardedFile{"migrations/purser/v0.3.11/expand/901_gone.sql": {checksum: migration.Checksum, guards: guards}}
	if issues := validateRerunGuardTargets([]Migration{migration}); len(issues) != 1 || !strings.Contains(issues[0].Message, "does not exist") {
		t.Fatalf("guard for a missing migration: %v", issues)
	}
}

// TestEmbeddedRerunGuardsSurviveTheYugabyteLayoutRewrite builds every guarded shipped migration as YugabyteDB receives
// it, with its database's layout applied, so each guard still finds the statement it describes.
func TestEmbeddedRerunGuardsSurviveTheYugabyteLayoutRewrite(t *testing.T) {
	migrations, err := discoverAllPostgresMigrationsForValidation()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, migration := range migrations {
		if _, guarded := yugabyteRerunGuards[migration.Path]; !guarded {
			continue
		}
		found++
		rewritten, err := yugabyteSQLForSource(migration.Database, migration.content)
		if err != nil {
			t.Fatal(err)
		}
		migration.content = rewritten
		statements, _, err := migrationStatements(migration, SQLEngineYugabyte)
		if err != nil {
			t.Fatalf("%s: %v", migration.Path, err)
		}
		guardedStatements := 0
		for _, statement := range statements {
			if strings.HasPrefix(statement, "DO "+rerunGuardBlockTag) || strings.Contains(statement, " IF EXISTS ") {
				guardedStatements++
			}
		}
		if guardedStatements == 0 {
			t.Fatalf("%s: no guarded statement in %q", migration.Path, statements)
		}
	}
	if found != len(yugabyteRerunGuards) {
		t.Fatalf("found %d of %d guarded migrations", found, len(yugabyteRerunGuards))
	}
}
