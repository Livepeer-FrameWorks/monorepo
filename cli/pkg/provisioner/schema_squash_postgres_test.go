//go:build schema_verify

package provisioner

import (
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// Postgres proxy for the YugabyteDB target (ysql is PG-compatible for schema DDL).
// pgvector image because a baseline uses CREATE EXTENSION vector (Yugabyte ships it).
const pgHarnessImage = "pgvector/pgvector:pg15"

// pgIntrospectQuery dumps a database's logical schema as sorted text: columns
// (schema/table/column/type/nullability/default), indexes, and constraints across
// all user schemas. _migrations is excluded (it exists only in the replayed DB).
const pgIntrospectQuery = `
SELECT 'col|' || table_schema || '|' || table_name || '|' || column_name || '|' ||
       data_type || '|' || is_nullable || '|' || coalesce(column_default, '')
  FROM information_schema.columns
 WHERE table_schema NOT IN ('pg_catalog','information_schema')
   AND table_name <> '_migrations'
UNION ALL
SELECT 'idx|' || schemaname || '|' || indexname || '|' || indexdef
  FROM pg_indexes
 WHERE schemaname NOT IN ('pg_catalog','information_schema')
   AND tablename <> '_migrations'
UNION ALL
SELECT 'con|' || tc.table_schema || '|' || tc.table_name || '|' || tc.constraint_type || '|' || tc.constraint_name
  FROM information_schema.table_constraints tc
 WHERE tc.table_schema NOT IN ('pg_catalog','information_schema')
   AND tc.table_name <> '_migrations'
   -- Exclude Postgres's synthesized NOT NULL check constraints: their names embed
   -- table OIDs (e.g. 21489_21843_3_not_null) so they differ between the two DBs
   -- as pure noise. NOT NULL is already compared via is_nullable in the column rows.
   AND NOT (tc.constraint_type = 'CHECK' AND tc.constraint_name LIKE '%_not_null')
 ORDER BY 1`

func pgStart(t *testing.T, name string) {
	t.Helper()
	rmContainer(t, name)
	if _, err := docker(t, "", "run", "-d", "--name", name,
		"-e", "POSTGRES_PASSWORD=harness", pgHarnessImage); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if out, err := docker(t, "", "exec", name, "psql", "-U", "postgres", "-tAc", "SELECT 1"); err == nil && strings.TrimSpace(out) == "1" {
			return
		}
		if time.Now().After(deadline) {
			logs, _ := docker(t, "", "logs", "--tail", "40", name)
			t.Fatalf("%s did not become ready:\n%s", name, logs)
		}
		time.Sleep(time.Second)
	}
}

func pgCreateDB(t *testing.T, name, db string) {
	t.Helper()
	if _, err := docker(t, "", "exec", name, "psql", "-U", "postgres", "-c", "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create db %s: %v", db, err)
	}
}

func pgApply(t *testing.T, name, db, sql string) {
	t.Helper()
	if out, err := docker(t, sql, "exec", "-i", name, "psql", "-U", "postgres", "-d", db, "-v", "ON_ERROR_STOP=1", "-q"); err != nil {
		t.Fatalf("apply SQL to %s/%s: %v\noutput: %s", name, db, err, out)
	}
}

func pgIntrospect(t *testing.T, name, db string) string {
	t.Helper()
	out, err := docker(t, "", "exec", name, "psql", "-U", "postgres", "-d", db, "-tAc", pgIntrospectQuery)
	if err != nil {
		t.Fatalf("introspect %s/%s: %v", name, db, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// pgBaselineFiles lists the FrameWorks Postgres baseline schema files: schema/*.sql
// minus periscope.sql (ClickHouse) and minus any baseline that does CREATE DATABASE
// (chatwoot/listmonk are external apps owning their own top-level DB, not the
// FW schema-in-shared-DB model, and have no FW migrations to fold).
func pgBaselineFiles(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(dbsql.Content, "schema")
	if err != nil {
		t.Fatalf("read schema dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") || e.Name() == "periscope.sql" {
			continue
		}
		sql, rerr := dbsql.Content.ReadFile("schema/" + e.Name())
		if rerr != nil {
			t.Fatalf("read schema/%s: %v", e.Name(), rerr)
		}
		if strings.Contains(strings.ToUpper(string(sql)), "CREATE DATABASE") {
			continue
		}
		files = append(files, "schema/"+e.Name())
	}
	sort.Strings(files)
	return files
}

// TestPostgresBaselineEqualsReplay proves the Postgres baseline schema files equal
// the baseline + every POST-FLOOR migration replayed on top. Pre-floor migrations
// are folded into the baseline and NOT replayed — see the ClickHouse harness for
// why. This is a forward drift-guard, NOT a proof that the fold is complete (that is
// a live-prod diff); it also smoke-tests that every baseline applies cleanly on a
// real engine.
func TestPostgresBaselineEqualsReplay(t *testing.T) {
	requireDocker(t)

	baselines := pgBaselineFiles(t)
	known, err := knownMigrationDatabases()
	if err != nil {
		t.Fatalf("known migration databases: %v", err)
	}
	migs, err := discoverMigrationsInFS(dbsql.Content, "migrations", known)
	if err != nil {
		t.Fatalf("discover postgres migrations: %v", err)
	}

	const name = "fw-sv-pg"
	pgStart(t, name)
	defer rmContainer(t, name)

	applyBaselines := func(db string) {
		for _, f := range baselines {
			sql, rerr := dbsql.Content.ReadFile(f)
			if rerr != nil {
				t.Fatalf("read %s: %v", f, rerr)
			}
			pgApply(t, name, db, string(sql))
		}
	}

	// A: baselines only.
	pgCreateDB(t, name, "sv_a")
	applyBaselines("sv_a")
	baseline := pgIntrospect(t, name, "sv_a")

	// B: baselines + every post-floor migration in discovery order.
	pgCreateDB(t, name, "sv_b")
	applyBaselines("sv_b")
	replayedCount := 0
	for _, m := range migs {
		if belowBaselineFloor(m) {
			continue
		}
		replayedCount++
		pgApply(t, name, "sv_b", m.content)
	}
	replayed := pgIntrospect(t, name, "sv_b")

	t.Logf("postgres: %d baseline files, %d/%d migrations post-floor (floor=%s)",
		len(baselines), replayedCount, len(migs), schemaMigrationBaselineFloor)

	if baseline != replayed {
		// Line-level diff for readability.
		bset := map[string]bool{}
		for _, l := range strings.Split(baseline, "\n") {
			bset[l] = true
		}
		rset := map[string]bool{}
		for _, l := range strings.Split(replayed, "\n") {
			rset[l] = true
		}
		var diffs []string
		for _, l := range strings.Split(baseline, "\n") {
			if !rset[l] {
				diffs = append(diffs, "  only in baseline: "+l)
			}
		}
		for _, l := range strings.Split(replayed, "\n") {
			if !bset[l] {
				diffs = append(diffs, "  only in replay:   "+l)
			}
		}
		t.Fatalf("postgres: baseline != baseline+migrations:\n%s", strings.Join(diffs, "\n"))
	}
}
