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
	statements, guards, err := migrationStatements(Migration{Transactional: false, content: content})
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 2 || !strings.HasSuffix(statements[0], "E'it\\'s; fine'") || !strings.HasPrefix(statements[1], "CREATE UNIQUE INDEX") {
		t.Fatalf("statements = %q", statements)
	}
	if want := []string{"purser.idx_a", "purser.idx_B"}; !reflect.DeepEqual(guards, want) {
		t.Fatalf("guards = %q, want %q", guards, want)
	}
}

func TestTransactionalMigrationRunsAsOneScriptWithoutGuards(t *testing.T) {
	statements, guards, err := migrationStatements(Migration{Transactional: true, content: "ALTER TABLE a ADD COLUMN b int; UPDATE a SET b = 1;"})
	if err != nil || len(statements) != 1 || len(guards) != 0 {
		t.Fatalf("statements=%q guards=%q err=%v", statements, guards, err)
	}
}

func TestConcurrentIndexBuildMustNameItsIndexAndSchema(t *testing.T) {
	for _, sql := range []string{
		"CREATE INDEX CONCURRENTLY ON purser.t (a);",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_a ON t (a);",
		"CREATE INDEX CONCURRENTLY IF idx_a ON purser.t (a);",
	} {
		if _, _, err := migrationStatements(Migration{content: sql}); err == nil {
			t.Errorf("migrationStatements(%q) accepted a build the invalid-index guard cannot find", sql)
		}
	}
}
