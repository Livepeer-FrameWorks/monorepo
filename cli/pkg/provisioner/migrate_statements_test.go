package provisioner

import (
	"reflect"
	"strings"
	"testing"
)

// TestNonTransactionalStatementsSplitLikeTheLayoutRewriter proves a .notx.sql file splits on the statement boundaries
// the tokenizer sees: semicolons inside E” strings, nested block comments, and dollar-quoted bodies do not split.
func TestNonTransactionalStatementsSplitLikeTheLayoutRewriter(t *testing.T) {
	content := "/* outer /* nested; */ still comment; */\n" +
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_a ON purser.t (a) WHERE note <> E'it\\'s; fine';\n" +
		"-- trailing; comment\n" +
		"CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS \"idx_B\" ON ONLY purser.t (b);"
	for _, engine := range []SQLEngine{SQLEnginePostgres, SQLEngineYugabyte} {
		statements, guards, err := migrationStatements(Migration{Transactional: false, content: content}, engine)
		if err != nil {
			t.Fatal(err)
		}
		if len(statements) != 2 || !strings.HasSuffix(statements[0], "E'it\\'s; fine'") || !strings.HasPrefix(statements[1], "CREATE UNIQUE INDEX") {
			t.Fatalf("%s statements = %q", engine, statements)
		}
		if want := []string{"purser.idx_a", "purser.idx_B"}; !reflect.DeepEqual(guards, want) {
			t.Fatalf("%s guards = %q, want %q", engine, guards, want)
		}
	}
}

func TestTransactionalMigrationRunsAsOneScriptOnPostgres(t *testing.T) {
	statements, guards, err := migrationStatements(Migration{Transactional: true, content: "ALTER TABLE a ADD COLUMN b int; UPDATE a SET b = 1;"}, SQLEnginePostgres)
	if err != nil || len(statements) != 1 || len(guards) != 0 {
		t.Fatalf("statements=%q guards=%q err=%v", statements, guards, err)
	}
}

// TestTransactionalMigrationRunsStatementByStatementOnYugabyte pins that a YugabyteDB item carries one entry per
// statement, split where the layout rewriter splits, so the role can apply and retry each one in autocommit.
func TestTransactionalMigrationRunsStatementByStatementOnYugabyte(t *testing.T) {
	content := "INSERT INTO a VALUES (1) ON CONFLICT DO NOTHING;\n-- note; not a statement\nDO $$ BEGIN PERFORM 1; END $$;\nALTER TABLE a ADD COLUMN IF NOT EXISTS b int;\n"
	statements, guards, err := migrationStatements(Migration{Transactional: true, content: content}, SQLEngineYugabyte)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"INSERT INTO a VALUES (1) ON CONFLICT DO NOTHING", "DO $$ BEGIN PERFORM 1; END $$", "ALTER TABLE a ADD COLUMN IF NOT EXISTS b int"}
	if !reflect.DeepEqual(statements, want) || len(guards) != 0 {
		t.Fatalf("statements = %q guards = %q, want %q and no guards", statements, guards, want)
	}
}

func TestConcurrentIndexBuildMustNameItsIndexAndSchema(t *testing.T) {
	for _, sql := range []string{
		"CREATE INDEX CONCURRENTLY ON purser.t (a);",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_a ON t (a);",
		"CREATE INDEX CONCURRENTLY IF idx_a ON purser.t (a);",
	} {
		if _, _, err := migrationStatements(Migration{content: sql}, SQLEnginePostgres); err == nil {
			t.Errorf("migrationStatements(%q) accepted a build the invalid-index guard cannot find", sql)
		}
	}
}
