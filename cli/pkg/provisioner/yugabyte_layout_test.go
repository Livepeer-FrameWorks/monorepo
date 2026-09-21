package provisioner

import (
	"context"
	"fmt"
	"gopkg.in/yaml.v3"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/internal/releases"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

func mustParseLayout(t *testing.T, doc string) *DatabaseLayout {
	t.Helper()
	layout, err := parseDatabaseLayout("demo", []byte(doc))
	if err != nil {
		t.Fatalf("parseDatabaseLayout: %v", err)
	}
	return layout
}

func TestYugabyteLayoutsCoverCatalogDatabases(t *testing.T) {
	names := releases.ServiceDatabaseNames()
	if len(names) == 0 {
		t.Fatal("release catalog declares no service databases")
	}
	for _, database := range names {
		t.Run(database, func(t *testing.T) {
			layout, err := LoadDatabaseLayout(database)
			if err != nil {
				t.Fatalf("LoadDatabaseLayout: %v", err)
			}
			baseline, err := dbsql.Content.ReadFile("schema/" + database + ".sql")
			if err != nil {
				t.Fatalf("read baseline: %v", err)
			}
			if validateErr := ValidateLayoutAgainstBaseline(layout, string(baseline)); validateErr != nil {
				t.Fatal(validateErr)
			}
			rewritten, err := RewriteDDLForLayout(layout, string(baseline))
			if err != nil {
				t.Fatalf("RewriteDDLForLayout: %v", err)
			}
			want := 0
			for _, table := range layout.DistributedTables {
				if !slices.Contains(layout.RetiredTables, table) {
					want++
				}
			}
			if got := strings.Count(rewritten, yugabyteColocationOptOut); got != want {
				t.Fatalf("baseline carries %d colocation opt-outs, want one per distributed table it creates (%d)", got, want)
			}
			requireExactPlacement(t, "schema/"+database+".sql", layout, rewritten)
			if !layout.Colocated() && rewritten != string(baseline) {
				t.Fatal("a distributed layout must leave the baseline unchanged")
			}
		})
	}
}

// TestEveryEmbeddedBaselineBelongsToTheCatalog binds baselines to the release catalog: a platform database whose
// baseline creates schema must be a catalog service database, so it must also declare a layout. Otherwise it would be
// treated as a third-party database and silently keep the default placement.
func TestEveryEmbeddedBaselineBelongsToTheCatalog(t *testing.T) {
	entries, err := fs.ReadDir(dbsql.Content, "schema")
	if err != nil {
		t.Fatalf("read embedded baselines: %v", err)
	}
	catalog := releases.ServiceDatabaseNames()
	for _, entry := range entries {
		database := strings.TrimSuffix(entry.Name(), ".sql")
		_, executable, readErr := embeddedBaselineSQL(database)
		if readErr != nil {
			t.Fatalf("read %s: %v", entry.Name(), readErr)
		}
		if executable && !slices.Contains(catalog, database) {
			t.Errorf("schema/%s creates schema but %s is not a service database in the release catalog", entry.Name(), database)
		}
	}
}

func TestThirdPartyDatabasesKeepDefaultPlacementWhateverTheirName(t *testing.T) {
	for _, name := range []string{"chatwoot", "Chatwoot", "listmonk_prod", "Metabase-2"} {
		if layout, err := yugabyteLayoutForSource(name); err != nil || layout != nil {
			t.Errorf("yugabyteLayoutForSource(%q) = %v, %v; want no layout and no error", name, layout, err)
		}
	}
}

func TestYugabyteLayoutFilesBelongToCatalogDatabases(t *testing.T) {
	entries, err := fs.ReadDir(dbsql.Content, "layout")
	if err != nil {
		t.Fatalf("read embedded layouts: %v", err)
	}
	catalog := releases.ServiceDatabaseNames()
	for _, entry := range entries {
		database := strings.TrimSuffix(entry.Name(), ".yaml")
		if !slices.Contains(catalog, database) {
			t.Errorf("layout %s has no service database in the release catalog", entry.Name())
		}
	}
}

func TestYugabyteLayoutAppliesToEveryEmbeddedMigration(t *testing.T) {
	all, err := discoverMigrations("migrations")
	if err != nil {
		t.Fatalf("discover migrations: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no embedded migrations discovered")
	}
	droppedByMigrations := map[string]map[string]struct{}{}
	for _, m := range all {
		rewritten, rewriteErr := yugabyteSQLForSource(m.Database, m.content)
		if rewriteErr != nil {
			t.Errorf("%s/%s/%s/%s: %v", m.Database, m.Version, m.Phase, m.Filename, rewriteErr)
			continue
		}
		if layout, layoutErr := yugabyteLayoutForSource(m.Database); layoutErr == nil && layout != nil {
			requireExactPlacement(t, m.Database+"/"+m.Version+"/"+m.Phase+"/"+m.Filename, layout, rewritten)
		}
		if droppedByMigrations[m.Database] == nil {
			droppedByMigrations[m.Database] = map[string]struct{}{}
		}
		for _, table := range droppedTables(t, m.content) {
			droppedByMigrations[m.Database][table] = struct{}{}
		}
	}
	for _, database := range releases.ServiceDatabaseNames() {
		layout, err := LoadDatabaseLayout(database)
		if err != nil {
			t.Fatalf("LoadDatabaseLayout(%s): %v", database, err)
		}
		for _, table := range layout.RetiredTables {
			if _, ok := droppedByMigrations[database][table]; !ok {
				t.Errorf("%s layout retires %s, but no embedded migration drops it", database, table)
			}
		}
	}
}

func droppedTables(t *testing.T, sql string) []string {
	t.Helper()
	tokens, err := tokenizeSQL(sql)
	if err != nil {
		t.Fatalf("tokenize migration: %v", err)
	}
	var tables []string
	for i := 0; i+2 < len(tokens); i++ {
		if !isSQLWord(tokens[i], "drop") || !isSQLWord(tokens[i+1], "table") {
			continue
		}
		j := i + 2
		if j+1 < len(tokens) && isSQLWord(tokens[j], "if") && isSQLWord(tokens[j+1], "exists") {
			j += 2
		}
		if name, _, nameErr := parseQualifiedTableName(sql, tokens, j); nameErr == nil {
			tables = append(tables, name)
		}
	}
	return tables
}

func TestParseDatabaseLayoutRejectsInvalidContracts(t *testing.T) {
	cases := map[string]string{
		"wrong database":            "database: other\nlayout: colocated\n",
		"unknown layout":            "database: demo\nlayout: sharded\n",
		"unknown field":             "database: demo\nlayout: colocated\nhot_tables: []\n",
		"distributed with lists":    "database: demo\nlayout: distributed\ndistributed_tables: [demo.a]\n",
		"unqualified table":         "database: demo\nlayout: colocated\ncolocated_tables: [a]\n",
		"uppercase table":           "database: demo\nlayout: colocated\ncolocated_tables: [demo.A]\n",
		"listed twice":              "database: demo\nlayout: colocated\ncolocated_tables: [demo.a]\ndistributed_tables: [demo.a]\n",
		"benchmark not colocated":   "database: demo\nlayout: colocated\ndistributed_tables: [demo.a]\nwrite_rate_benchmark: [demo.a]\n",
		"retired without placement": "database: demo\nlayout: colocated\nretired_tables: [demo.old]\n",
		"retired twice":             "database: demo\nlayout: colocated\ndistributed_tables: [demo.old]\nretired_tables: [demo.old, demo.old]\n",
		"colocated outbox":          "database: demo\nlayout: colocated\ncolocated_tables: [demo.event_outbox]\n",
		"colocated inbox":           "database: demo\nlayout: colocated\ncolocated_tables: [demo.webhook_inbox]\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDatabaseLayout("demo", []byte(doc)); err == nil {
				t.Fatal("expected layout to be rejected")
			}
		})
	}
}

func TestLoadDatabaseLayoutRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", "../schema/foghorn", "Foghorn", "foghorn.yaml"} {
		if _, err := LoadDatabaseLayout(name); err == nil {
			t.Errorf("LoadDatabaseLayout(%q) accepted an unsafe name", name)
		}
	}
}

func TestYugabyteLayoutForSourceRequiresPlatformDatabases(t *testing.T) {
	layout, err := yugabyteLayoutForSource("chatwoot")
	if err != nil || layout != nil {
		t.Fatalf("chatwoot layout = %v, %v; want no layout and no error for a database the platform does not own", layout, err)
	}
	layout, err = yugabyteLayoutForSource("foghorn")
	if err != nil || !layout.Colocated() {
		t.Fatalf("foghorn layout = %v, %v; want the colocated platform layout", layout, err)
	}
}

func TestValidateLayoutAgainstBaselineReportsUnclassifiedAndStale(t *testing.T) {
	layout := mustParseLayout(t, "database: demo\nlayout: colocated\ncolocated_tables: [demo.a, demo.gone]\n")
	err := ValidateLayoutAgainstBaseline(layout, "CREATE TABLE demo.a (id int); CREATE TABLE demo.b (id int);")
	if err == nil || !strings.Contains(err.Error(), "demo.b") || !strings.Contains(err.Error(), "demo.gone") {
		t.Fatalf("ValidateLayoutAgainstBaseline error = %v, want demo.b unclassified and demo.gone stale", err)
	}
	retired := mustParseLayout(t, "database: demo\nlayout: colocated\ncolocated_tables: [demo.a]\ndistributed_tables: [demo.old]\nretired_tables: [demo.old]\n")
	if err = ValidateLayoutAgainstBaseline(retired, "CREATE TABLE demo.a (id int);"); err != nil {
		t.Fatalf("a retired table absent from the baseline must validate: %v", err)
	}
	err = ValidateLayoutAgainstBaseline(retired, "CREATE TABLE demo.a (id int); CREATE TABLE demo.old (id int);")
	if err == nil || !strings.Contains(err.Error(), "retired tables the baseline still creates [demo.old]") {
		t.Fatalf("ValidateLayoutAgainstBaseline error = %v, want demo.old reported as retired but still created", err)
	}
}

const rewriteFixtureLayout = "database: demo\nlayout: colocated\ncolocated_tables: [demo.small]\ndistributed_tables: [demo.big, demo.odd]\n"

func TestRewriteDDLForLayoutPlacesOnlyDistributedTables(t *testing.T) {
	layout := mustParseLayout(t, rewriteFixtureLayout)
	src := `-- CREATE TABLE demo.commented (id int);
/* outer /* nested CREATE TABLE demo.nested (id int); */ still a comment */
CREATE TABLE IF NOT EXISTS demo.small (
    id int PRIMARY KEY,
    note text DEFAULT 'CREATE TABLE demo.in_string (x int);' CHECK (length(note) < (10 * (2 + 3)))
);
CREATE TABLE demo.big (
    id bigint PRIMARY KEY,
    pattern text DEFAULT E'it\'s (not a paren',
    label text DEFAULT 'it''s ) fine'
) ;
CREATE TABLE "demo"."odd" (id int);
CREATE OR REPLACE FUNCTION demo.f() RETURNS TABLE(id int) AS $fn$
BEGIN
  RETURN QUERY SELECT 1 WHERE '$$' <> $$x$$;
END;
$fn$ LANGUAGE plpgsql;
INSERT INTO demo.small (id) SELECT id FROM demo.big;
`
	got, err := RewriteDDLForLayout(layout, src)
	if err != nil {
		t.Fatalf("RewriteDDLForLayout: %v", err)
	}
	for _, want := range []string{
		"CHECK (length(note) < (10 * (2 + 3)))\n);\n",
		"label text DEFAULT 'it''s ) fine'\n) WITH (COLOCATION = false) ;",
		`CREATE TABLE "demo"."odd" (id int) WITH (COLOCATION = false);`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten SQL missing %q:\n%s", want, got)
		}
	}
	if count := strings.Count(got, yugabyteColocationOptOut); count != 2 {
		t.Fatalf("rewritten SQL carries %d opt-outs, want 2:\n%s", count, got)
	}
}

func TestRewriteDDLForLayoutRejectsUnplaceableForms(t *testing.T) {
	layout := mustParseLayout(t, rewriteFixtureLayout)
	cases := map[string]string{
		"storage clause":      "CREATE TABLE demo.big (id int) WITH (fillfactor = 70);",
		"clause already set":  "CREATE TABLE demo.big (id int) WITH (COLOCATION = false);",
		"partition by":        "CREATE TABLE demo.big (id int) PARTITION BY RANGE (id);",
		"partition of":        "CREATE TABLE demo.big PARTITION OF demo.parent FOR VALUES FROM (1) TO (2);",
		"create table as":     "CREATE TABLE demo.big AS SELECT 1;",
		"typed table":         "CREATE TABLE demo.big OF demo.row_type;",
		"inherits":            "CREATE TABLE demo.big (id int) INHERITS (demo.parent);",
		"tablespace":          "CREATE TABLE demo.big (id int) TABLESPACE fast;",
		"temporary":           "CREATE TEMP TABLE demo.big (id int);",
		"unlogged":            "CREATE UNLOGGED TABLE demo.big (id int);",
		"unqualified":         "CREATE TABLE big (id int);",
		"materialized view":   "CREATE MATERIALIZED VIEW demo.mv AS SELECT 1;",
		"select into":         "SELECT 1 AS id INTO demo.copy FROM demo.small;",
		"do block":            "DO $$ BEGIN CREATE TABLE demo.big (id int); END $$;",
		"nested dollar body":  "CREATE FUNCTION demo.f() RETURNS void AS $outer$ BEGIN PERFORM $inner$ x $inner$; CREATE TABLE demo.big (id int); END $outer$ LANGUAGE plpgsql;",
		"unclassified table":  "CREATE TABLE demo.unknown (id int);",
		"unterminated string": "CREATE TABLE demo.big (note text DEFAULT 'oops);",
		"unterminated dollar": "DO $$ BEGIN",
		"unterminated ident":  `CREATE TABLE "demo.big (id int);`,
		"unbalanced paren":    "CREATE TABLE demo.big (id int;",
		"schema with table":   "CREATE SCHEMA demo2 CREATE TABLE demo.big (id int);",
		"with select into":    "WITH x AS (SELECT 1 AS id) SELECT id INTO demo.copy FROM x;",
		"byte-order mark":     "\ufeffCREATE TABLE demo.big (id int);",
		"psql meta-command":   "\\connect demo\nCREATE TABLE demo.big (id int);",
		"copy from stdin":     "COPY demo.small (id) FROM stdin;",
		"explain create":      "EXPLAIN ANALYZE CREATE TABLE demo.big AS SELECT 1;",
		"foreign table":       "CREATE FOREIGN TABLE demo.big (id int) SERVER remote;",
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := RewriteDDLForLayout(layout, sql); err == nil {
				t.Fatalf("expected rejection, got:\n%s", got)
			}
		})
	}
}

func TestRewriteDDLForLayoutAcceptsInsertAfterWith(t *testing.T) {
	layout := mustParseLayout(t, rewriteFixtureLayout)
	sql := "WITH x AS (SELECT 1 AS id) INSERT INTO demo.small (id) SELECT id FROM x;"
	if got, err := RewriteDDLForLayout(layout, sql); err != nil || got != sql {
		t.Fatalf("RewriteDDLForLayout = %q, %v; want the INSERT unchanged", got, err)
	}
}

// placedTables maps every table the SQL creates to whether its statement carries the colocation opt-out.
func placedTables(t *testing.T, sql string) map[string]bool {
	t.Helper()
	statements, err := sqlStatements(sql)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	placed := map[string]bool{}
	for _, stmt := range statements {
		if len(stmt) < 3 || !isSQLWord(stmt[0], "create") {
			continue
		}
		i := 1
		for i < len(stmt) && isTableModifier(stmt[i]) {
			i++
		}
		if i >= len(stmt) || !isSQLWord(stmt[i], "table") {
			continue
		}
		i++
		if i+2 < len(stmt) && isSQLWord(stmt[i], "if") && isSQLWord(stmt[i+1], "not") && isSQLWord(stmt[i+2], "exists") {
			i += 3
		}
		table, _, nameErr := parseQualifiedTableName(sql, stmt, i)
		if nameErr != nil {
			t.Fatalf("created table name: %v", nameErr)
		}
		text := sql[stmt[0].start:stmt[len(stmt)-1].end]
		placed[table] = strings.HasSuffix(text, strings.TrimSpace(yugabyteColocationOptOut))
	}
	return placed
}

// requireExactPlacement checks each table the rewritten SQL creates carries the opt-out exactly when the layout
// distributes it.
func requireExactPlacement(t *testing.T, label string, layout *DatabaseLayout, rewritten string) {
	t.Helper()
	for table, optedOut := range placedTables(t, rewritten) {
		want := layout.Colocated() && slices.Contains(layout.DistributedTables, table)
		if optedOut != want {
			t.Errorf("%s: %s carries the colocation opt-out=%v, want %v", label, table, optedOut, want)
		}
	}
}

func TestRewriteDDLForLayoutValidatesDistributedAndMissingLayouts(t *testing.T) {
	distributed := &DatabaseLayout{Database: "demo", Layout: DatabaseLayoutDistributed}
	valid := "CREATE TABLE demo.anything (id int);"
	for name, layout := range map[string]*DatabaseLayout{"distributed": distributed, "missing": nil} {
		got, err := RewriteDDLForLayout(layout, valid)
		if err != nil || got != valid {
			t.Fatalf("%s layout rewrote valid SQL to %q, %v; want it unchanged", name, got, err)
		}
		if _, err := RewriteDDLForLayout(layout, "CREATE UNLOGGED TABLE demo.anything (id int);"); err == nil {
			t.Fatalf("%s layout accepted an unplaceable table form", name)
		}
	}
}

func TestYugabytePlacementDriftReportsDatabaseBeforeRelations(t *testing.T) {
	layout := mustParseLayout(t, rewriteFixtureLayout)
	relations, err := ParseYugabyteRelationPlacements(strings.Join([]string{
		"demo.big|r|demo.big|f|1",
		"demo.big_v|i|demo.big|t|1",
		"demo.small|r|demo.small|t|1",
		"demo.small_k|i|demo.small|t|1",
		"public._migrations|r|public._migrations|t|1",
	}, "\n"))
	if err != nil {
		t.Fatalf("ParseYugabyteRelationPlacements: %v", err)
	}
	if drift := YugabytePlacementDrift(layout, false, relations); len(drift) != 1 || drift[0] != "database is distributed, layout declares colocated" {
		t.Fatalf("drift in a distributed database = %v, want only the database-level mismatch", drift)
	}
	want := []string{"demo.big_v (index on demo.big) is colocated, layout declares distributed"}
	if drift := YugabytePlacementDrift(layout, true, relations); !slices.Equal(drift, want) {
		t.Fatalf("relation drift = %v, want %v", drift, want)
	}
	if drift := YugabytePlacementDrift(nil, false, relations); len(drift) != 0 {
		t.Fatalf("a database without a layout reported drift %v", drift)
	}
	if _, err := ParseYugabyteRelationPlacements("demo.big|r|demo.big|maybe|1"); err == nil {
		t.Fatal("ParseYugabyteRelationPlacements accepted a non-boolean placement")
	}
}

func baselineTableTail(t *testing.T, sql, table string) string {
	t.Helper()
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS "+table+" (")
	if start < 0 {
		t.Fatalf("baseline has no CREATE TABLE for %s", table)
	}
	closing := strings.Index(sql[start:], "\n)")
	end := strings.IndexByte(sql[start+closing:], ';')
	if closing < 0 || end < 0 {
		t.Fatalf("baseline CREATE TABLE for %s has no closing parenthesis", table)
	}
	return sql[start+closing+1 : start+closing+end+1]
}

func TestBuildSchemaItemsForYugabyteAppliesLogicalLayout(t *testing.T) {
	databases := []SchemaDatabase{{Name: "foghorn_eu", Owner: "foghorn_eu", SourceName: "foghorn", Schema: "foghorn"}}
	read := func(engine SQLEngine) string {
		items, cleanup, err := BuildSchemaItemsForEngine(databases, engine)
		t.Cleanup(cleanup)
		if err != nil || len(items) != 1 {
			t.Fatalf("BuildSchemaItemsForEngine(%s) = %d items, %v", engine, len(items), err)
		}
		src, ok := items[0]["src"].(string)
		if !ok {
			t.Fatalf("schema src has type %T", items[0]["src"])
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read schema src: %v", err)
		}
		return string(data)
	}
	baseline, err := dbsql.Content.ReadFile("schema/foghorn.sql")
	if err != nil {
		t.Fatalf("read foghorn baseline: %v", err)
	}
	if read(SQLEnginePostgres) != string(baseline) {
		t.Fatal("PostgreSQL schema items must carry the embedded baseline unchanged")
	}
	yugabyte := read(SQLEngineYugabyte)
	if got := baselineTableTail(t, yugabyte, "foghorn.dvr_segments"); got != ")"+yugabyteColocationOptOut+";" {
		t.Fatalf("distributed foghorn.dvr_segments ends with %q", got)
	}
	if got := baselineTableTail(t, yugabyte, "foghorn.node_outputs"); got != ");" {
		t.Fatalf("colocated foghorn.node_outputs ends with %q", got)
	}
}

func TestBuildMigrationItemsForYugabyteRewritesSQLButKeepsIdentity(t *testing.T) {
	content := "CREATE TABLE IF NOT EXISTS foghorn.artifact_event_outbox (id bigint PRIMARY KEY);"
	all := []Migration{{
		Database: "foghorn", Version: "v0.3.5", Phase: "expand", Sequence: 1,
		Filename: "001_outbox.sql", Checksum: "checksum-1", Transactional: true, content: content,
	}}
	databases := []SchemaDatabase{{Name: "foghorn_eu", Owner: "foghorn_eu", SourceName: "foghorn", Schema: "foghorn"}}
	postgres, err := buildMigrationItemsFromList(all, databases, "expand", "v99.0.0", SQLEnginePostgres)
	if err != nil || len(postgres) != 1 {
		t.Fatalf("postgres items = %v, %v", postgres, err)
	}
	yugabyte, err := buildMigrationItemsFromList(all, databases, "expand", "v99.0.0", SQLEngineYugabyte)
	if err != nil || len(yugabyte) != 1 {
		t.Fatalf("yugabyte items = %v, %v", yugabyte, err)
	}
	want := strings.TrimSuffix(content, ";") + yugabyteColocationOptOut + ";"
	if postgres[0]["sql"] != content || yugabyte[0]["sql"] != want {
		t.Fatalf("sql postgres=%q yugabyte=%q, want yugabyte %q", postgres[0]["sql"], yugabyte[0]["sql"], want)
	}
	if statements, ok := yugabyte[0]["statements"].([]string); !ok || len(statements) != 1 || statements[0] != want {
		t.Fatalf("yugabyte statements = %#v", yugabyte[0]["statements"])
	}
	if postgres[0]["checksum"] != "checksum-1" || yugabyte[0]["checksum"] != "checksum-1" {
		t.Fatal("ledger checksum must stay the embedded file checksum for both engines")
	}
}

func TestBuildMigrationItemsForYugabyteScansAutocommitMigrations(t *testing.T) {
	all := []Migration{{
		Database: "foghorn", Version: "v0.3.5", Phase: "expand", Sequence: 2,
		Filename: "002_indexes.notx.sql", Checksum: "checksum-2", Transactional: false,
		content: "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_a ON foghorn.artifacts (id);\nCREATE TABLE foghorn.unclassified_table (id int);",
	}}
	databases := []SchemaDatabase{{Name: "foghorn"}}
	if _, err := buildMigrationItemsFromList(all, databases, "expand", "v99.0.0", SQLEnginePostgres); err != nil {
		t.Fatalf("postgres items: %v", err)
	}
	_, err := buildMigrationItemsFromList(all, databases, "expand", "v99.0.0", SQLEngineYugabyte)
	if err == nil || !strings.Contains(err.Error(), "foghorn.unclassified_table") {
		t.Fatalf("yugabyte items error = %v, want the unclassified table in an autocommit migration rejected", err)
	}
}

func TestBuildItemsRejectUnknownEngine(t *testing.T) {
	if _, _, err := BuildSchemaItemsForEngine([]SchemaDatabase{{Name: "foghorn"}}, "cockroach"); err == nil {
		t.Fatal("BuildSchemaItemsForEngine accepted an unknown engine")
	}
	if _, err := BuildMigrationItemsForEngine([]SchemaDatabase{{Name: "foghorn"}}, "expand", "v0.3.4", "cockroach"); err == nil {
		t.Fatal("BuildMigrationItemsForEngine accepted an unknown engine")
	}
}

func TestYugabyteRoleVarsResolveColocationFromLayoutSource(t *testing.T) {
	vars, err := yugabyteRoleVars(context.Background(), nilHost(), ServiceConfig{
		Metadata: map[string]any{
			"postgres_password":         "owner-secret",
			"postgres_runtime_password": "runtime-secret",
			"databases": []map[string]string{
				{"name": "foghorn_eu", "owner": "foghorn_eu", "layout_source": "foghorn"},
				{"name": "quartermaster", "owner": "quartermaster"},
				{"name": "skipper", "owner": "skipper"},
				{"name": "chatwoot", "owner": "chatwoot"},
			},
		},
	}, mockPrivateerHelpers())
	if err != nil {
		t.Fatalf("yugabyteRoleVars: %v", err)
	}
	dbs, ok := vars["yugabyte_databases"].([]map[string]any)
	if !ok {
		t.Fatalf("yugabyte_databases = %#v", vars["yugabyte_databases"])
	}
	want := map[string]bool{"foghorn_eu": true, "quartermaster": true, "skipper": false, "chatwoot": false}
	for _, db := range dbs {
		name, _ := db["name"].(string)
		if got, wantColocated := db["colocated"], want[name]; got != wantColocated {
			t.Errorf("%s colocated = %v, want %v", name, got, wantColocated)
		}
	}
	placements, ok := vars["yugabyte_database_placements"].([]map[string]any)
	if !ok || len(placements) != len(dbs) {
		t.Fatalf("yugabyte_database_placements = %#v, want one per database", vars["yugabyte_database_placements"])
	}
	for _, placement := range placements {
		name, _ := placement["name"].(string)
		if placement["colocated"] != want[name] || placement["owner"] == "" || len(placement) != 3 {
			t.Errorf("placement %v, want only name, owner, and colocated=%v", placement, want[name])
		}
	}
}

func TestYugabyteInitCreatesColocatedDatabasesOnlyWhenAbsent(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/tasks/init.yml")
	var tasks []struct {
		Name  string         `yaml:"name"`
		Query map[string]any `yaml:"community.postgresql.postgresql_query"`
		DB    map[string]any `yaml:"community.postgresql.postgresql_db"`
		Loop  any            `yaml:"loop"`
		NoLog bool           `yaml:"no_log"`
		When  any            `yaml:"when"`
	}
	if err := yaml.Unmarshal([]byte(content), &tasks); err != nil {
		t.Fatalf("parse init.yml: %v", err)
	}
	index := map[string]int{}
	for i, task := range tasks {
		index[task.Name] = i
	}
	validate, okValidate := index["Validate database identifiers used in CREATE DATABASE"]
	probe, okProbe := index["Read existing databases"]
	colocated, okColocated := index["Create colocated databases"]
	plain, okPlain := index["Create databases"]
	if !okValidate || !okProbe || !okColocated || !okPlain || validate >= probe || probe >= colocated || colocated >= plain {
		t.Fatalf("init.yml must validate identifiers, read existing databases, then create colocated and ordinary databases in that order")
	}
	when := func(i int) []string {
		var conditions []string
		switch value := tasks[i].When.(type) {
		case string:
			conditions = append(conditions, value)
		case []any:
			for _, condition := range value {
				conditions = append(conditions, fmt.Sprint(condition))
			}
		}
		return conditions
	}
	create := tasks[colocated]
	if query, _ := create.Query["query"].(string); !strings.Contains(query, `WITH OWNER = "{{ item.owner }}" COLOCATION = true`) || create.Query["autocommit"] != true {
		t.Fatalf("colocated create = %+v", create.Query)
	}
	for _, task := range []int{colocated, plain} {
		if loop, _ := tasks[task].Loop.(string); tasks[task].NoLog || !strings.Contains(loop, "yugabyte_database_placements") {
			t.Fatalf("%q must loop over the credential-free placements with its output visible (loop %q, no_log %v)", tasks[task].Name, tasks[task].Loop, tasks[task].NoLog)
		}
	}
	if !slices.Contains(when(colocated), "item.name not in (yugabyte_existing_databases.query_result | default([]) | map(attribute='datname') | list)") ||
		!slices.Contains(when(colocated), "item.colocated | bool") {
		t.Fatalf("colocated create runs when %v, want only for absent colocated databases", when(colocated))
	}
	if !slices.Contains(when(plain), "not (item.colocated | bool)") {
		t.Fatalf("ordinary create runs when %v, want only for distributed databases", when(plain))
	}
}
