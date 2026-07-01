package cmd

import (
	"strings"
	"testing"
)

// columnHashArgs must wrap AggregateFunction columns in finalizeAggregation (so
// cityHash64 can digest them) and pass everything else through, backtick-quoted.
func TestColumnHashArgs_aggregateStateWrapped(t *testing.T) {
	cols := []chColumn{
		{name: "window_start", typ: "DateTime"},
		{name: "tenant_id", typ: "UUID"},
		{name: "unique_users_state", typ: "AggregateFunction(uniqCombined, UInt64)"},
		{name: "seconds_observed", typ: "SimpleAggregateFunction(sum, UInt64)"},
	}
	got, finalized := columnHashArgs(cols)
	if !finalized {
		t.Fatalf("expected finalized=true when an AggregateFunction column is present")
	}
	want := "`window_start`, `tenant_id`, finalizeAggregation(`unique_users_state`), `seconds_observed`"
	if got != want {
		t.Fatalf("columnHashArgs =\n  %q\nwant\n  %q", got, want)
	}
}

// A table with no aggregate-state columns must be hashed column-wise with no
// finalizeAggregation wrapping, and report finalized=false.
func TestColumnHashArgs_plainColumns(t *testing.T) {
	got, finalized := columnHashArgs([]chColumn{{name: "id", typ: "UUID"}, {name: "n", typ: "UInt64"}})
	if finalized {
		t.Fatalf("expected finalized=false for plain columns")
	}
	if got != "`id`, `n`" {
		t.Fatalf("columnHashArgs = %q", got)
	}
}

// chClientScript must stage SQL to a temp file and run it via --queries-file —
// never --query — so the source credentials embedded in remote(...) never reach
// the destination's process argv, and must clean up via trap on EXIT.
func TestChClientScript_stagesQueriesFileNotArgv(t *testing.T) {
	m := &chMigrateCtx{db: "periscope", dstPort: 9000, user: "frameworks", pass: "s3cret"}
	sql := "SELECT * FROM remote('old:9000', periscope, t, 'frameworks', 's3cret')"
	script := m.chClientScript(sql)

	if strings.Contains(script, "--query '") || strings.Contains(script, "--query \"") {
		t.Fatalf("script must not pass SQL via --query (argv leak):\n%s", script)
	}
	if !strings.Contains(script, "--queries-file") {
		t.Fatalf("script must run via --queries-file:\n%s", script)
	}
	if !strings.Contains(script, "trap 'rm -f \"$f\"' EXIT") {
		t.Fatalf("script must clean up the temp file on EXIT:\n%s", script)
	}
	// The SQL (and thus the source creds) belongs in the heredoc body, not argv.
	if !strings.Contains(script, sql) {
		t.Fatalf("SQL should be written into the staged file:\n%s", script)
	}
	if !strings.Contains(script, "CLICKHOUSE_PASSWORD=") {
		t.Fatalf("destination password must ride the env, not argv:\n%s", script)
	}
}

// With no destination password, the env prefix must be omitted entirely.
func TestChClientScript_noPasswordNoEnvPrefix(t *testing.T) {
	m := &chMigrateCtx{db: "periscope", dstPort: 9000, user: "default"}
	if strings.Contains(m.chClientScript("SELECT 1"), "CLICKHOUSE_PASSWORD=") {
		t.Fatalf("no env prefix expected when password is empty")
	}
}
