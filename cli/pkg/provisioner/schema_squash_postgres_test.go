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

// pgIntrospectQuery dumps every deploy-relevant logical object as sorted text.
// Definitions that can contain newlines are hashed so one object remains one
// comparison row. Migration bookkeeping and baseline provenance are excluded
// because they intentionally differ by install path.
const pgIntrospectQuery = `
SELECT 'col|' || table_schema || '|' || table_name || '|' || column_name || '|' ||
       data_type || '|' || is_nullable || '|' || coalesce(column_default, '')
  FROM information_schema.columns
 WHERE table_schema NOT IN ('pg_catalog','information_schema')
   AND table_name NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'idx|' || schemaname || '|' || indexname || '|' || indexdef
  FROM pg_indexes
 WHERE schemaname NOT IN ('pg_catalog','information_schema')
   AND tablename NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'con|' || n.nspname || '|' || t.relname || '|' || c.contype::text || '|' || c.conname || '|' || pg_get_constraintdef(c.oid)
  FROM pg_constraint c
  JOIN pg_class t ON t.oid = c.conrelid
  JOIN pg_namespace n ON n.oid = t.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND t.relname NOT IN ('_migrations', '_schema_baseline')
   -- Exclude Postgres's synthesized NOT NULL check constraints: their names embed
   -- table OIDs (e.g. 21489_21843_3_not_null) so they differ between the two DBs
   -- as pure noise. NOT NULL is already compared via is_nullable in the column rows.
   AND NOT (c.contype = 'c' AND c.conname LIKE '%_not_null')
UNION ALL
SELECT 'trg|' || n.nspname || '|' || t.relname || '|' || tg.tgname || '|' || pg_get_triggerdef(tg.oid)
  FROM pg_trigger tg
  JOIN pg_class t ON t.oid = tg.tgrelid
  JOIN pg_namespace n ON n.oid = t.relnamespace
 WHERE NOT tg.tgisinternal
   AND n.nspname NOT IN ('pg_catalog','information_schema')
   AND t.relname NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'routine|' || n.nspname || '|' || p.prokind::text || '|' || p.proname || '|' ||
       pg_get_function_identity_arguments(p.oid) || '|' || md5(pg_get_functiondef(p.oid)) || '|' || pg_get_userbyid(p.proowner)
  FROM pg_proc p
  JOIN pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND p.prokind IN ('f', 'p')
UNION ALL
SELECT 'view|' || n.nspname || '|' || c.relname || '|' || c.relkind::text || '|' || md5(pg_get_viewdef(c.oid, true)) || '|' || pg_get_userbyid(c.relowner)
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND c.relkind IN ('v', 'm')
UNION ALL
SELECT 'seq|' || sequence_schema || '|' || sequence_name || '|' || data_type || '|' ||
       start_value || '|' || minimum_value || '|' || maximum_value || '|' || increment || '|' || cycle_option
  FROM information_schema.sequences
 WHERE sequence_schema NOT IN ('pg_catalog','information_schema')
UNION ALL
SELECT 'ext|' || e.extname || '|' || e.extversion || '|' || n.nspname
  FROM pg_extension e
  JOIN pg_namespace n ON n.oid = e.extnamespace
 WHERE e.extname <> 'plpgsql'
UNION ALL
SELECT 'enum|' || n.nspname || '|' || t.typname || '|' || string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder) || '|' || pg_get_userbyid(t.typowner)
  FROM pg_type t
  JOIN pg_namespace n ON n.oid = t.typnamespace
  JOIN pg_enum e ON e.enumtypid = t.oid
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
 GROUP BY n.nspname, t.typname, t.typowner
UNION ALL
SELECT 'domain|' || n.nspname || '|' || t.typname || '|' || format_type(t.typbasetype, t.typtypmod) || '|' ||
       t.typnotnull::text || '|' || coalesce(t.typdefault, '') || '|' || pg_get_userbyid(t.typowner)
  FROM pg_type t
  JOIN pg_namespace n ON n.oid = t.typnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND t.typtype = 'd'
UNION ALL
SELECT 'composite|' || n.nspname || '|' || c.relname || '|' || a.attnum::text || '|' || a.attname || '|' ||
       format_type(a.atttypid, a.atttypmod) || '|' || a.attnotnull::text || '|' || pg_get_userbyid(c.relowner)
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_attribute a ON a.attrelid = c.oid
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND c.relkind = 'c'
   AND a.attnum > 0
   AND NOT a.attisdropped
UNION ALL
SELECT 'schema-owner|' || n.nspname || '|' || pg_get_userbyid(n.nspowner)
  FROM pg_namespace n
 WHERE n.nspname NOT IN ('pg_catalog','information_schema','public')
UNION ALL
SELECT 'relation-owner|' || n.nspname || '|' || c.relname || '|' || c.relkind::text || '|' || pg_get_userbyid(c.relowner)
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND c.relkind IN ('r','p','v','m','S','f')
   AND c.relname NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'table-grant|' || table_schema || '|' || table_name || '|' || grantor || '|' || grantee || '|' || privilege_type || '|' || is_grantable
  FROM information_schema.role_table_grants
 WHERE table_schema NOT IN ('pg_catalog','information_schema')
   AND table_name NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'routine-grant|' || routine_schema || '|' || routine_name || '|' || grantor || '|' || grantee || '|' || privilege_type || '|' || is_grantable
  FROM information_schema.role_routine_grants
 WHERE routine_schema NOT IN ('pg_catalog','information_schema')
UNION ALL
SELECT 'usage-grant|' || object_type || '|' || object_schema || '|' || object_name || '|' || grantor || '|' || grantee || '|' || privilege_type || '|' || is_grantable
  FROM information_schema.role_usage_grants
 WHERE object_schema NOT IN ('pg_catalog','information_schema')
UNION ALL
SELECT 'default-acl|' || coalesce(n.nspname, '') || '|' || pg_get_userbyid(d.defaclrole) || '|' || d.defaclobjtype::text || '|' || d.defaclacl::text
  FROM pg_default_acl d
  LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace
UNION ALL
SELECT 'rls-table|' || n.nspname || '|' || c.relname || '|' || c.relrowsecurity::text || '|' || c.relforcerowsecurity::text
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND c.relkind IN ('r','p')
   AND c.relname NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'rls-policy|' || schemaname || '|' || tablename || '|' || policyname || '|' || permissive || '|' ||
       array_to_string(roles, ',') || '|' || cmd || '|' || coalesce(qual, '') || '|' || coalesce(with_check, '')
  FROM pg_policies
 ORDER BY 1`

func pgStart(t *testing.T, name string) {
	t.Helper()
	rmContainer(t, name)
	pgHarnessImage := infrastructureHarnessImage(t, "postgresql")
	if _, err := docker(t, "", "run", "-d", "--name", name,
		"-e", "POSTGRES_PASSWORD=harness", pgHarnessImage); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	// Register cleanup NOW — the moment the container exists — not after readiness. A readiness Fatalf below would
	// otherwise leak the container + its anonymous data volume, because the caller only installs its defer AFTER
	// this function returns. (A caller that also defers rmContainer double-removes harmlessly: rm -fv on a gone
	// container is a no-op.)
	t.Cleanup(func() { rmContainer(t, name) })
	deadline := time.Now().Add(90 * time.Second)
	for {
		// The official image first starts a temporary socket-only postmaster to run initialization, then deliberately
		// stops it before starting the final server. A plain SELECT 1 can hit that temporary process and let the test
		// race its shutdown. The final Docker postmaster listens on configured addresses; the temporary one forces
		// listen_addresses='' and therefore cannot satisfy this probe.
		if out, err := docker(t, "", "exec", name, "psql", "-U", "postgres", "-tAc",
			"SELECT CASE WHEN current_setting('listen_addresses') <> '' THEN 1 ELSE 0 END"); err == nil && strings.TrimSpace(out) == "1" {
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

func requirePGSchemasEqual(t *testing.T, label, expected, actual string) {
	t.Helper()
	if expected == actual {
		return
	}

	expectedSet := map[string]bool{}
	for _, line := range strings.Split(expected, "\n") {
		expectedSet[line] = true
	}
	actualSet := map[string]bool{}
	for _, line := range strings.Split(actual, "\n") {
		actualSet[line] = true
	}
	var diffs []string
	for _, line := range strings.Split(expected, "\n") {
		if !actualSet[line] {
			diffs = append(diffs, "  only in expected: "+line)
		}
	}
	for _, line := range strings.Split(actual, "\n") {
		if !expectedSet[line] {
			diffs = append(diffs, "  only in actual:   "+line)
		}
	}
	t.Fatalf("%s schemas differ:\n%s", label, strings.Join(diffs, "\n"))
}

func TestPostgresIntrospectionCoversDeployRelevantObjects(t *testing.T) {
	requireDocker(t)
	const name = "fw-sv-pg-introspect"
	pgStart(t, name)

	const common = `
CREATE EXTENSION pgcrypto;
CREATE SCHEMA contract;
CREATE TYPE contract.state AS ENUM ('open', 'closed');
CREATE DOMAIN contract.positive_int AS integer CHECK (VALUE > 0);
CREATE TYPE contract.coordinate AS (x integer, y integer);
CREATE TABLE contract.items (
    id bigint PRIMARY KEY,
    tenant_id uuid NOT NULL,
    state contract.state NOT NULL,
    weight contract.positive_int NOT NULL
);
CREATE SEQUENCE contract.external_ids START 10 INCREMENT 5;
CREATE MATERIALIZED VIEW contract.item_counts AS SELECT state, count(*) AS total FROM contract.items GROUP BY state;
CREATE FUNCTION contract.item_count() RETURNS bigint LANGUAGE sql AS $$ SELECT count(*) FROM contract.items $$;
CREATE PROCEDURE contract.clear_items() LANGUAGE sql AS $$ DELETE FROM contract.items $$;
ALTER TABLE contract.items ENABLE ROW LEVEL SECURITY;
GRANT SELECT ON contract.items TO PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA contract GRANT SELECT ON TABLES TO PUBLIC;
`

	pgCreateDB(t, name, "objects_a")
	pgApply(t, name, "objects_a", common+`
CREATE VIEW contract.visible_items AS SELECT id, tenant_id FROM contract.items WHERE state = 'open';
CREATE POLICY tenant_items ON contract.items USING (tenant_id = current_setting('app.tenant_id')::uuid);
`)
	a := pgIntrospect(t, name, "objects_a")
	for _, prefix := range []string{
		"view|contract|visible_items|v|",
		"view|contract|item_counts|m|",
		"seq|contract|external_ids|",
		"ext|pgcrypto|",
		"enum|contract|state|open,closed|",
		"domain|contract|positive_int|integer|",
		"composite|contract|coordinate|",
		"routine|contract|f|item_count|",
		"routine|contract|p|clear_items|",
		"schema-owner|contract|",
		"relation-owner|contract|items|",
		"table-grant|contract|items|",
		"default-acl|contract|",
		"rls-table|contract|items|true|false",
		"rls-policy|contract|items|tenant_items|",
	} {
		if !strings.Contains(a, prefix) {
			t.Errorf("schema introspection omitted %q", prefix)
		}
	}

	pgCreateDB(t, name, "objects_b")
	pgApply(t, name, "objects_b", common+`
CREATE VIEW contract.visible_items AS SELECT id, tenant_id FROM contract.items WHERE state = 'closed';
CREATE POLICY tenant_items ON contract.items USING (tenant_id IS NOT NULL);
`)
	b := pgIntrospect(t, name, "objects_b")
	if a == b {
		t.Fatal("schema introspection did not detect changed view and RLS policy definitions")
	}
}

// pgBaselineFiles lists the FrameWorks Postgres baseline schema files: schema/*.sql
// minus any baseline that does CREATE DATABASE (chatwoot/listmonk are external
// apps owning their own top-level DB, not the
// FW schema-in-shared-DB model, and have no FW migrations to fold).
func pgBaselineFiles(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(dbsql.Content, "schema")
	if err != nil {
		t.Fatalf("read schema dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
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

	requirePGSchemasEqual(t, "postgres baseline vs baseline+migrations", baseline, replayed)
}

// TestPostgresTaggedBaselineUpgradeEqualsCurrent proves the actual release
// lifecycle: a database initialized by the latest shipped tag, plus every
// migration developed after that tag, converges to the baseline a fresh install
// receives from the current commit.
func TestPostgresTaggedBaselineUpgradeEqualsCurrent(t *testing.T) {
	requireDocker(t)
	fromTag := schemaVerifyFromTag(t)
	baselines := pgBaselineFiles(t)
	known, err := knownMigrationDatabases()
	if err != nil {
		t.Fatalf("known migration databases: %v", err)
	}
	allMigrations, err := discoverMigrationsInFS(dbsql.Content, "migrations", known)
	if err != nil {
		t.Fatalf("discover postgres migrations: %v", err)
	}
	migrations := migrationsAfterVersion(allMigrations, fromTag)

	const name = "fw-sv-pg-tag"
	pgStart(t, name)

	pgCreateDB(t, name, "sv_current")
	for _, file := range baselines {
		sql, readErr := dbsql.Content.ReadFile(file)
		if readErr != nil {
			t.Fatalf("read %s: %v", file, readErr)
		}
		pgApply(t, name, "sv_current", string(sql))
	}
	current := pgIntrospect(t, name, "sv_current")

	pgCreateDB(t, name, "sv_upgraded")
	for _, file := range baselines {
		taggedSQL := repositoryFileAtTag(t, fromTag, "pkg/database/sql/"+file)
		pgApply(t, name, "sv_upgraded", taggedSQL)
	}
	for _, migration := range migrations {
		pgApply(t, name, "sv_upgraded", migration.content)
	}
	upgraded := pgIntrospect(t, name, "sv_upgraded")

	t.Logf("postgres: upgraded %s baseline with %d migration(s) to current", fromTag, len(migrations))
	requirePGSchemasEqual(t, "postgres tagged baseline upgrade vs current baseline", current, upgraded)
}
