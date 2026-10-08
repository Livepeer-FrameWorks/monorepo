package provisioner

import (
	"strings"
	"testing"
)

func TestYugabyteLocalQueriesScriptQuotesDatabasesAndRefusesTerminator(t *testing.T) {
	script, err := yugabyteLocalQueriesScript(5433, "yugabyte", []YugabyteLocalQuery{{Database: "it's", SQL: "SELECT $1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `fw_query 0 'it'\''s' <<'FW_SQL_EOF'`+"\nSELECT $1\nFW_SQL_EOF\n") {
		t.Fatalf("script does not pass the database quoted and the SQL through a quoted heredoc:\n%s", script)
	}
	if _, err := yugabyteLocalQueriesScript(5433, "yugabyte", []YugabyteLocalQuery{{Database: "db", SQL: "SELECT 1\nFW_SQL_EOF\nDROP TABLE x"}}); err == nil {
		t.Fatal("a statement containing the heredoc terminator was accepted")
	}
}

func TestParseYugabyteLocalQueriesSeparatesAnswers(t *testing.T) {
	queries := []YugabyteLocalQuery{{Database: "a"}, {Database: "b"}, {Database: "c"}}
	stdout := strings.Join([]string{
		"@@fw-query 0", "x|1", "y|2", "", "@@fw-done 0 0 ",
		"@@fw-query 1", "", "@@fw-done 1 2 FATAL:  database \"b\" does not exist ",
		"@@fw-query 2", "partial",
	}, "\n")
	results := parseYugabyteLocalQueries(stdout, queries)
	if results[0].Err != nil || len(results[0].Rows) != 2 || results[0].Rows[1][0] != "y" || results[0].Rows[1][1] != "2" {
		t.Fatalf("first answer = %+v", results[0])
	}
	if results[1].Err == nil || !strings.Contains(results[1].Err.Error(), `ysqlsh -d b exited 2: FATAL:  database "b" does not exist`) {
		t.Fatalf("failed query = %+v, want its exit status and stderr", results[1])
	}
	if results[2].Err == nil || !strings.Contains(results[2].Err.Error(), "returned no result") {
		t.Fatalf("unfinished query = %+v, want it reported as unanswered, not as its partial rows", results[2])
	}
}
