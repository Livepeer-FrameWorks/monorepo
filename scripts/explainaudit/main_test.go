package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func TestParamRefsBindComparedColumns(t *testing.T) {
	text := `SELECT o.id FROM foghorn.processing_jobs o
JOIN foghorn.artifacts a ON a.artifact_hash = o.artifact_hash
WHERE o.tenant_id = $1 AND $2 <= o.next_attempt_at AND a.status = ANY($3::text[]) AND o.kind IN ('a', $4)
ORDER BY o.next_attempt_at LIMIT $5`
	refs := paramRefs(text, 5)
	want := map[int]bindRef{
		1: {qualifier: "o", column: "tenant_id", op: "="},
		2: {qualifier: "o", column: "next_attempt_at", op: ">="},
		3: {qualifier: "a", column: "status", op: "="},
		4: {qualifier: "o", column: "kind", op: "="},
	}
	for n, w := range want {
		if len(refs[n]) == 0 || refs[n][0] != w {
			t.Errorf("$%d: got %+v, want %+v", n, refs[n], w)
		}
	}
	if len(refs[5]) != 0 {
		t.Errorf("$5 (LIMIT) bound to a column: %+v", refs[5])
	}
}

func TestParamRefsBindInsertColumnsByPosition(t *testing.T) {
	refs := paramRefs(`INSERT INTO lookout.incidents (id, tenant_id, created_at) VALUES ($1, $2::uuid, now()) ON CONFLICT DO NOTHING`, 2)
	if len(refs[2]) == 0 || refs[2][0].column != "tenant_id" || refs[2][0].qualifier != "incidents" {
		t.Fatalf("$2: got %+v", refs[2])
	}
}

func TestTableAliasesSkipKeywords(t *testing.T) {
	tables := map[string]*table{"x.a": {}, "x.b": {}}
	aliases, referenced := tableAliases(`SELECT 1 FROM x.a WHERE true AND EXISTS (SELECT 1 FROM x.b bb WHERE bb.id = 1)`, tables)
	if aliases["a"] != "x.a" || aliases["bb"] != "x.b" || aliases["where"] != "" || len(referenced) != 2 {
		t.Fatalf("aliases %v referenced %v", aliases, referenced)
	}
}

func TestEvaluatorFoldsConcatenationAndSprintf(t *testing.T) {
	src := `package p
const table = "foghorn.jobs"
func f(schema string) {
	q := "SELECT id FROM " + table + " WHERE a = $1"
	db.QueryContext(ctx, q, arg.TenantID)
	db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s.outbox WHERE id = $1", schema), id)
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]ast.Expr{}
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				consts[value.Names[0].Name] = value.Values[0]
			}
		}
	}
	b := &catalogBuilder{constCache: map[string]map[string]ast.Expr{"": consts}, fset: fset}
	var got []query
	b.scanParsed(file, "", func(q query) { got = append(got, q) })
	if len(got) != 2 {
		t.Fatalf("got %d statements: %+v", len(got), got)
	}
	if got[0].SQL != "SELECT id FROM foghorn.jobs WHERE a = $1" || got[0].ParamNames[0] != "tenant_id" {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].SQL != "DELETE FROM "+dynamicMarker+".outbox WHERE id = $1" || !got[1].Dynamic {
		t.Errorf("second: %+v", got[1])
	}
}

func TestSnakeCase(t *testing.T) {
	for in, want := range map[string]string{"TenantID": "tenant_id", "tenantID": "tenant_id", "NextAttemptAt": "next_attempt_at", "ID": "id", "streamIDs": "stream_ids"} {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// The plan shape of the production finding: a claim over a HASH-sharded (next_attempt_at) index reads the whole
// queue and sorts it.
func TestAnalyzeFlagsSeqScanAndSortOfLargeQueue(t *testing.T) {
	plan := `[{"Plan":{"Node Type":"Limit","Actual Rows":10,"Actual Loops":1,"Plans":[{"Node Type":"LockRows","Actual Rows":10,"Actual Loops":1,"Plans":[
{"Node Type":"Sort","Actual Rows":10,"Actual Loops":1,"Sort Key":["next_attempt_at"],"Plans":[
{"Node Type":"Seq Scan","Relation Name":"q","Actual Rows":2500,"Actual Loops":1,"Storage Table Rows Scanned":50000,"Storage Table Read Requests":49}]}]}]},
"Execution Time":80.5,"Storage Read Requests":59,"Storage Rows Scanned":50010}]`
	s := &seeder{tables: map[string]*table{"x.q": {name: "q", seededRows: 60000, deletedRows: 10000}}}
	var r result
	analyze(json.RawMessage(plan), s, &r)
	for _, flag := range []string{"SEQ_SCAN_LARGE", "SORT_LARGE", "READ_AMPLIFICATION", "MANY_READ_RPCS", "SLOW"} {
		if !hasFlag(r, flag) {
			t.Errorf("missing %s in %v", flag, r.Flags)
		}
	}
}

func TestAnalyzeFlagsPerRowSubplanAndUnbatchedNestedLoop(t *testing.T) {
	plan := `[{"Plan":{"Node Type":"Nested Loop","Actual Rows":300,"Actual Loops":1,"Plans":[
{"Node Type":"Index Scan","Relation Name":"a","Index Name":"a_pkey","Actual Rows":300,"Actual Loops":1},
{"Node Type":"Index Scan","Relation Name":"b","Index Name":"b_pkey","Actual Rows":1,"Actual Loops":300},
{"Node Type":"Index Scan","Parent Relationship":"SubPlan","Subplan Name":"SubPlan 1","Relation Name":"c","Index Name":"c_pkey","Actual Rows":1,"Actual Loops":300}]},
"Execution Time":5,"Storage Read Requests":601,"Storage Rows Scanned":901}]`
	s := &seeder{tables: map[string]*table{}}
	var r result
	analyze(json.RawMessage(plan), s, &r)
	for _, flag := range []string{"NESTED_LOOP_UNBATCHED", "CORRELATED_SUBPLAN", "MANY_READ_RPCS"} {
		if !hasFlag(r, flag) {
			t.Errorf("missing %s in %v", flag, r.Flags)
		}
	}
}

func TestBudgetRejectsNewFlagAndGrowth(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/budget.json"
	base := []result{{ID: "db:Q", Layout: "distributed", Flags: []string{"SEQ_SCAN_LARGE"}, Metrics: &metrics{StorageRowsRead: 5000, ReadRequests: 5}}}
	if err := writeBudget(path, base); err != nil {
		t.Fatal(err)
	}
	if err := checkBudget(path, base); err != nil {
		t.Fatalf("unchanged run failed: %v", err)
	}
	grown := []result{{ID: "db:Q", Layout: "distributed", Flags: []string{"SEQ_SCAN_LARGE", "SORT_LARGE"}, Metrics: &metrics{StorageRowsRead: 20000, ReadRequests: 5}}}
	if err := checkBudget(path, grown); err == nil {
		t.Fatal("a new flag and a 4x scan passed the budget")
	}
	fresh := []result{{ID: "db:Other", Layout: "declared", Flags: []string{"CORRELATED_SUBPLAN"}, Metrics: &metrics{}}}
	if err := checkBudget(path, fresh); err == nil {
		t.Fatal("a newly flagged statement passed the budget")
	}
}

func hasFlag(r result, flag string) bool {
	for _, f := range r.Flags {
		if f == flag {
			return true
		}
	}
	return false
}

func TestBudgetDoesNotPassWhenExplainStopsWorking(t *testing.T) {
	path := t.TempDir() + "/budget.json"
	base := []result{{ID: "db:Q", Layout: "distributed", Status: "analyzed", Flags: []string{"SEQ_SCAN_LARGE"}, Metrics: &metrics{StorageRowsRead: 5000}}}
	if err := writeBudget(path, base); err != nil {
		t.Fatal(err)
	}
	broken := []result{{ID: "db:Q", Layout: "distributed", Status: "not_audited", Reason: "execute and plan failed"}}
	if err := checkBudget(path, broken); err == nil {
		t.Fatal("a failed EXPLAIN silently passed the performance budget")
	}
}

func TestRecordedPlansMeetCurrentBudget(t *testing.T) {
	path := os.Getenv("EXPLAIN_AUDIT_REPORT")
	if path == "" {
		t.Skip("EXPLAIN_AUDIT_REPORT selects an existing real-engine results.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	if err := checkBudget("budget.json", results); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetChecksRequestGrowthWithoutPlanFlags(t *testing.T) {
	path := t.TempDir() + "/budget.json"
	base := []result{{ID: "db:Q", Layout: "distributed", Flags: []string{"MANY_READ_RPCS"}, Metrics: &metrics{ReadRequests: 5}}}
	if err := writeBudget(path, base); err != nil {
		t.Fatal(err)
	}
	grown := []result{{ID: "db:Q", Layout: "distributed", Status: "analyzed", Metrics: &metrics{ReadRequests: 25}}}
	if err := checkBudget(path, grown); err == nil {
		t.Fatal("request growth passed because no generic plan flag was present")
	}
}

func TestSprintfFoldSupportsRepeatedIndexedArguments(t *testing.T) {
	got := sprintfFold("SELECT %[1]s FROM %[2]s WHERE %[1]s = $1", []string{"id", "x.rows"})
	if got != "SELECT id FROM x.rows WHERE id = $1" {
		t.Fatalf("indexed format not resolved: %s", got)
	}
}

func TestClaimBucketBindingsStayInRange(t *testing.T) {
	b := &binder{s: &seeder{tables: map[string]*table{}}}
	got := b.bind("SELECT 1 WHERE $1::bigint < 0 OR mod(5, $2::bigint) = $1::bigint", []string{"claim_bucket", "claim_buckets"}, []string{"bigint", "bigint"})
	if got[0].Value != "0" || got[1].Value != "1" {
		t.Fatalf("invalid single-bucket fixture: %+v", got)
	}
}

func TestAuditRecognizesSQLWithLeadingHint(t *testing.T) {
	if !sqlStartRe.MatchString("/*+ IndexScan(c pending) */ UPDATE x.queue c SET leased = true") {
		t.Fatal("hinted SQL disappeared from the audit catalog")
	}
}

func TestBudgetRejectsEmptyAudit(t *testing.T) {
	path := t.TempDir() + "/budget.json"
	if err := writeBudget(path, nil); err != nil {
		t.Fatal(err)
	}
	if err := checkBudget(path, nil); err == nil {
		t.Fatal("empty audit passed")
	}
}
