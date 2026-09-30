//go:build schema_verify

package provisioner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/cli/internal/releases"
	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/lib/pq"
)

func yugabyteServiceDatabases(t *testing.T) ([]string, bool) {
	t.Helper()
	baselines := pgBaselineFiles(t)
	available := make(map[string]struct{}, len(baselines))
	for _, baseline := range baselines {
		available[strings.TrimSuffix(filepath.Base(baseline), ".sql")] = struct{}{}
	}
	selection := strings.TrimSpace(os.Getenv("FRAMEWORKS_YUGABYTE_DATABASES"))
	if selection == "" {
		services := make([]string, 0, len(available))
		for service := range available {
			services = append(services, service)
		}
		sort.Strings(services)
		return services, false
	}
	selected := make(map[string]struct{})
	for _, service := range strings.FieldsFunc(selection, func(r rune) bool { return r == ',' || r == ' ' }) {
		if _, ok := available[service]; !ok {
			t.Fatalf("FRAMEWORKS_YUGABYTE_DATABASES selects unknown database %q", service)
		}
		selected[service] = struct{}{}
	}
	if len(selected) == 0 {
		t.Fatal("FRAMEWORKS_YUGABYTE_DATABASES must select at least one database")
	}
	services := make([]string, 0, len(selected))
	for service := range selected {
		services = append(services, service)
	}
	sort.Strings(services)
	return services, true
}

func TestYugabyteDatabaseSelection(t *testing.T) {
	t.Setenv("FRAMEWORKS_YUGABYTE_DATABASES", "purser, navigator purser")
	services, constrained := yugabyteServiceDatabases(t)
	if !constrained {
		t.Fatal("explicit Yugabyte database selection was not constrained")
	}
	want := []string{"navigator", "purser"}
	if len(services) != len(want) {
		t.Fatalf("selected databases = %v, want %v", services, want)
	}
	for i := range want {
		if services[i] != want[i] {
			t.Fatalf("selected databases = %v, want %v", services, want)
		}
	}
}

func ybStart(t *testing.T, name string) string {
	t.Helper()
	if shared := strings.TrimSpace(os.Getenv("FRAMEWORKS_YUGABYTE_TEST_CONTAINER")); shared != "" {
		return shared
	}
	rmContainer(t, name)
	image := infrastructureContractImage(t, "yugabyte")
	if _, err := docker(t, "", "run", "-d", "--name", name, image,
		"bin/yugabyted", "start", "--background=false", "--ui=false", "--advertise_address=127.0.0.1",
		"--tserver_flags=yb_enable_read_committed_isolation=true"); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() { rmContainer(t, name) })
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if out, err := docker(t, "", "exec", name, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", "yugabyte", "-tAc", "SELECT 1"); err == nil && strings.TrimSpace(out) == "1" {
			return name
		}
		if time.Now().After(deadline) {
			logs, _ := docker(t, "", "logs", "--tail", "80", name)
			t.Fatalf("%s did not become ready:\n%s", name, logs)
		}
		time.Sleep(time.Second)
	}
}

// ybEnsureLoginRole creates a login role with no privileges beyond LOGIN unless it exists, and fails when an existing
// role of that name, created by a contract that ran earlier on the same engine, holds more: superuser, CREATEDB,
// CREATEROLE, REPLICATION, BYPASSRLS, or membership in another role.
func ybEnsureLoginRole(t *testing.T, name, role string) {
	t.Helper()
	statement := fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN CREATE ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS; END IF;
END $$;`, relayoutLiteral(role), relayoutIdentifier(role))
	if out, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", statement); err != nil {
		t.Fatalf("create Yugabyte role %s: %v\n%s", role, err, out)
	}
	attributes := ybQuery(t, name, "yugabyte", fmt.Sprintf(`SELECT concat_ws(',', rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls,
       (SELECT count(*) FROM pg_auth_members m WHERE m.member = r.oid))
FROM pg_roles r WHERE rolname = %s`, relayoutLiteral(role)))
	if attributes != "t,f,f,f,f,f,0" {
		t.Fatalf("Yugabyte role %s has login,superuser,createdb,createrole,replication,bypassrls,memberships = %s, want t,f,f,f,f,f,0", role, attributes)
	}
}

func ybSQLHost(name string) string {
	if strings.TrimSpace(os.Getenv("FRAMEWORKS_YUGABYTE_TEST_CONTAINER")) != "" {
		return name
	}
	return "127.0.0.1"
}

func ybApply(t *testing.T, name, db, sql string) {
	t.Helper()
	if out, err := dockerWithTimeout(t, 10*time.Minute, sql, "exec", "-i", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", db, "-v", "ON_ERROR_STOP=1", "-q"); err != nil {
		t.Fatalf("apply SQL to %s/%s: %v\n%s", name, db, err, out)
	}
}

// ybPlacementIntrospectQuery adds physical placement to schema introspection, so an upgraded database and a fresh
// baseline must also agree on database colocation and on the placement of every table and secondary index.
var ybPlacementIntrospectQuery = `SELECT 'yb-database-colocated|' || yb_is_database_colocated()::text
UNION ALL
SELECT 'yb-placement|' || relation || '|' || kind::text || '|' || colocated::text
FROM (` + YugabyteRelationPlacementQuery + `) AS placement(relation, kind, indexed_table, colocated, num_tablets)`

func ybIntrospect(t *testing.T, name, db string) string {
	t.Helper()
	out, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", db, "-tAc", pgIntrospectQuery)
	if err != nil {
		t.Fatalf("introspect Yugabyte schema %s: %v\n%s", db, err, out)
	}
	placement, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", db, "-tAc", ybPlacementIntrospectQuery)
	if err != nil {
		t.Fatalf("introspect Yugabyte placement %s: %v\n%s", db, err, placement)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	lines = append(lines, strings.Split(strings.TrimSpace(placement), "\n")...)
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// ybGrantRuntimeRole gives a runtime role the connect, schema usage, DML, sequence, and function access provisioning
// grants, including default privileges for objects the superuser creates later.
func ybGrantRuntimeRole(t *testing.T, name, database, schema, runtimeRole string) {
	t.Helper()
	ybApply(t, name, database, ybRuntimeGrantSQL(database, schema, runtimeRole))
}

func ybRuntimeGrantSQL(database, schema, runtimeRole string) string {
	return fmt.Sprintf(`
GRANT CONNECT ON DATABASE %[1]s TO %[3]s;
GRANT USAGE ON SCHEMA %[2]s TO %[3]s;
REVOKE CREATE ON SCHEMA %[2]s FROM %[3]s;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %[2]s TO %[3]s;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %[2]s TO %[3]s;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA %[2]s TO %[3]s;
ALTER DEFAULT PRIVILEGES FOR ROLE yugabyte IN SCHEMA %[2]s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %[3]s;
ALTER DEFAULT PRIVILEGES FOR ROLE yugabyte IN SCHEMA %[2]s GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %[3]s;
ALTER DEFAULT PRIVILEGES FOR ROLE yugabyte IN SCHEMA %[2]s GRANT EXECUTE ON FUNCTIONS TO %[3]s;
`, database, schema, runtimeRole)
}

func ybQuery(t *testing.T, name, database, query string) string {
	t.Helper()
	out, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", database, "-v", "ON_ERROR_STOP=1", "-tAc", query)
	if err != nil {
		t.Fatalf("query %s: %v\n%s\n%s", database, err, query, out)
	}
	return strings.TrimSpace(out)
}

// ybDatabaseLayout returns the embedded layout for a baseline database, or nil for databases the platform does not own.
func ybDatabaseLayout(t *testing.T, database string) *DatabaseLayout {
	t.Helper()
	layout, err := YugabyteLayoutForDatabase(database)
	if err != nil {
		t.Fatalf("load %s layout: %v", database, err)
	}
	return layout
}

func ybLayoutSQL(t *testing.T, layout *DatabaseLayout, sql string) string {
	t.Helper()
	rewritten, err := RewriteDDLForLayout(layout, sql)
	if err != nil {
		t.Fatalf("apply YugabyteDB layout: %v", err)
	}
	return rewritten
}

func ybCreateDatabase(t *testing.T, name, databaseName string, layout *DatabaseLayout) {
	t.Helper()
	statement := "CREATE DATABASE " + databaseName
	if layout.Colocated() {
		statement += " WITH COLOCATION = true"
	}
	if out, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", statement); err != nil {
		t.Fatalf("create Yugabyte database %s: %v\n%s", databaseName, err, out)
	}
}

const ybParentTabletCountQuery = `SELECT count(DISTINCT tablet_id) FROM yb_local_tablets
WHERE namespace_name = current_database() AND table_name LIKE '%.colocation.parent.tablename'`

// ybRequireDeclaredPlacement proves a database matches its layout on the engine: database colocation, every table and
// secondary index, and the resulting tablet count. The fixture is a single node, so local tablets are all tablets.
func ybRequireDeclaredPlacement(t *testing.T, name, database string, layout *DatabaseLayout, checkTablets bool) {
	t.Helper()
	colocated := ybQuery(t, name, database, "SELECT yb_is_database_colocated()") == "t"
	relations, err := ParseYugabyteRelationPlacements(ybQuery(t, name, database, YugabyteRelationPlacementQuery))
	if err != nil {
		t.Fatalf("parse %s placement: %v", database, err)
	}
	if drift := YugabytePlacementDrift(layout, colocated, relations); len(drift) > 0 {
		t.Fatalf("%s placement differs from its layout:\n%s", database, strings.Join(drift, "\n"))
	}
	colocatedRelations, distributedTablets := 0, 0
	for _, relation := range relations {
		if relation.Kind == "r" || relation.Kind == "p" {
			// Migration and data-migration ledgers are created at runtime and deliberately stay outside the layout.
			runtimeTable := isRelayoutRuntimeTable(relation.Table)
			if _, classified := layout.Placement(relation.Table); layout != nil && !classified && !runtimeTable {
				t.Fatalf("%s creates %s, which its layout does not classify", database, relation.Table)
			}
		}
		if relation.Colocated {
			colocatedRelations++
		} else {
			distributedTablets += relation.NumTablets
		}
	}
	if !checkTablets {
		return
	}
	wantParents := 0
	if colocatedRelations > 0 {
		wantParents = 1
	}
	parents, _ := strconv.Atoi(ybQuery(t, name, database, ybParentTabletCountQuery))
	total, _ := strconv.Atoi(ybQuery(t, name, database, "SELECT count(DISTINCT tablet_id) FROM yb_local_tablets WHERE namespace_name = current_database()"))
	if parents != wantParents || total != wantParents+distributedTablets {
		t.Fatalf("%s has %d parent and %d total tablets, want %d parent and %d total (%d colocated relations share the parent)",
			database, parents, total, wantParents, wantParents+distributedTablets, colocatedRelations)
	}
	t.Logf("yugabyte: %s layout %s holds %d relations in %d tablet(s)", database, layout.Layout, len(relations), total)
}

// ybRequireDistributedTableSplits proves that inside a colocated database a table created with the colocation opt-out
// splits into more tablets while the colocation parent stays a single tablet.
func ybRequireDistributedTableSplits(t *testing.T, name, database string) {
	t.Helper()
	ybApply(t, name, database, `
CREATE TABLE public.layout_split_probe (id bigint PRIMARY KEY, payload text) WITH (COLOCATION = false);
INSERT INTO public.layout_split_probe SELECT g, repeat('x', 200) FROM generate_series(1, 50000) g;`)
	parentsBefore := ybQuery(t, name, database, ybParentTabletCountQuery)
	probeFilter := "namespace_name = current_database() AND ysql_schema_name = 'public' AND table_name = 'layout_split_probe'"
	tableID := ybQuery(t, name, database, "SELECT DISTINCT table_id FROM yb_local_tablets WHERE "+probeFilter)
	tabletID := ybQuery(t, name, database, "SELECT tablet_id FROM yb_local_tablets WHERE "+probeFilter)
	if len(strings.Fields(tabletID)) != 1 {
		t.Fatalf("split probe in %s must start with one tablet, got %q", database, tabletID)
	}
	master := ybQuery(t, name, database, "SELECT host FROM yb_servers() LIMIT 1") + ":7100"
	for _, args := range [][]string{{"flush_table", "tableid." + tableID, "60"}, {"split_tablet", tabletID}} {
		command := append([]string{"exec", name, "bin/yb-admin", "--master_addresses", master}, args...)
		if out, err := docker(t, "", command...); err != nil {
			t.Fatalf("yb-admin %s: %v\n%s", args[0], err, out)
		}
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		properties := ybQuery(t, name, database, "SELECT num_tablets FROM yb_table_properties('public.layout_split_probe'::regclass)")
		// The split parent stays listed until it is cleaned up; the doctor's serving-tablet query counts only the
		// children. yb_table_properties can lag the completed split, so require the two distinct replacement IDs.
		serving := strings.Fields(ybQuery(t, name, database, "SELECT tablet_id FROM ("+YugabyteServingTabletsQuery+") s WHERE s.tablet_id IN (SELECT tablet_id FROM yb_local_tablets WHERE "+probeFilter+")"))
		listed, _ := strconv.Atoi(ybQuery(t, name, database, "SELECT count(*) FROM yb_local_tablets WHERE "+probeFilter))
		if len(serving) == 2 && serving[0] != serving[1] && serving[0] != tabletID && serving[1] != tabletID {
			t.Logf("yugabyte: split probe in %s has two serving children %v replacing %s, %d listed, properties=%s", database, serving, tabletID, listed, properties)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("distributed probe table in %s did not converge within two minutes: properties=%s serving=%v original=%s listed=%d; local tablets:\n%s",
				database, properties, serving, tabletID, listed, ybQuery(t, name, database, "SELECT tablet_id || '|' || state FROM yb_local_tablets WHERE "+probeFilter))
		}
		time.Sleep(time.Second)
	}
	if parentsAfter := ybQuery(t, name, database, ybParentTabletCountQuery); parentsAfter != parentsBefore {
		t.Fatalf("splitting a distributed table changed %s colocation parent tablets from %s to %s", database, parentsBefore, parentsAfter)
	}
	if data := ybQuery(t, name, database, "SELECT count(*), min(id), max(id), count(*) FILTER (WHERE payload <> repeat('x', 200)) FROM public.layout_split_probe"); data != "50000|1|50000|0" {
		t.Fatalf("split probe in %s lost or changed data: %s", database, data)
	}
	ybApply(t, name, database, "DROP TABLE public.layout_split_probe;")
}

func TestYugabyteDistributedOptOutSplits(t *testing.T) {
	requireDocker(t)
	name := ybStart(t, fmt.Sprintf("fw-sv-yb-split-%d", time.Now().UnixNano()))
	const database = "layout_split_contract"
	ybApply(t, name, "yugabyte", "CREATE DATABASE "+database+" WITH COLOCATION = true;")
	t.Cleanup(func() { ybDropDatabase(t, name, database) })
	ybRequireDistributedTableSplits(t, name, database)
}

// ybDropDatabase drops a test database. A session still attached to it (Yugabyte's own
// backends, such as an index backfill a test started) is logged and terminated first,
// so the drop does not depend on when that session would end by itself.
func ybDropDatabase(t *testing.T, name, databaseName string) {
	t.Helper()
	ysql := func(sql string) (string, error) {
		return docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-tA", "-c", sql)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, err := ysql("DROP DATABASE " + databaseName)
		if err == nil {
			return
		}
		if !strings.Contains(err.Error()+out, "is being accessed by other users") || time.Now().After(deadline) {
			t.Errorf("drop Yugabyte database %s: %v\n%s", databaseName, err, out)
			return
		}
		attached, _ := ysql(fmt.Sprintf(`SELECT pid, COALESCE(application_name, ''), COALESCE(backend_type, ''), COALESCE(state, ''), left(COALESCE(query, ''), 120)
			FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, databaseName))
		t.Logf("sessions attached to %s at drop: %s", databaseName, strings.TrimSpace(attached))
		if _, termErr := ysql(fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, databaseName)); termErr != nil {
			t.Logf("terminate sessions attached to %s: %v", databaseName, termErr)
		}
		time.Sleep(time.Second)
	}
}

func ybRequireAllIndexesValid(t *testing.T, name, databaseName string) {
	t.Helper()
	output, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", databaseName, "-tAc", `
SELECT count(*)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_index i ON i.indexrelid = c.oid
WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND (NOT i.indisvalid OR NOT i.indisready)`)
	if err != nil {
		t.Fatalf("inspect Yugabyte indexes in %s: %v\n%s", databaseName, err, output)
	}
	if strings.TrimSpace(output) != "0" {
		t.Fatalf("Yugabyte schema %s has %s invalid or unready indexes", databaseName, strings.TrimSpace(output))
	}
}

func ybVerifyTaggedMigrationPaths(t *testing.T) {
	t.Helper()
	fromTag := schemaVerifyFromTag(t)
	known, err := knownMigrationDatabases()
	if err != nil {
		t.Fatalf("known migration databases: %v", err)
	}
	allMigrations, err := discoverMigrationsInFS(dbsql.Content, "migrations", known)
	if err != nil {
		t.Fatalf("discover PostgreSQL migrations: %v", err)
	}
	postTag := migrationsAfterVersion(allMigrations, fromTag)
	selectedServices, constrained := yugabyteServiceDatabases(t)
	serviceSet := make(map[string]struct{}, len(selectedServices))
	for _, service := range selectedServices {
		serviceSet[service] = struct{}{}
	}
	if !constrained {
		for _, migration := range postTag {
			serviceSet[migration.Database] = struct{}{}
		}
	}
	services := make([]string, 0, len(serviceSet))
	for service := range serviceSet {
		services = append(services, service)
	}
	sort.Strings(services)
	name := ybStart(t, fmt.Sprintf("fw-sv-yb-upgrades-%d", time.Now().UnixNano()))
	// Existing clusters keep distributed databases until they are relaid out, and new ones get the declared layout, so
	// the upgrade path is proven in both shapes. Both receive the layout-rewritten SQL the yugabyte role applies.
	//
	// Every database is created before any baseline is applied, and the databases are then built at the same time: on
	// YugabyteDB a CREATE DATABASE fails while another database runs DDL (docs/architecture/database-ha.md).
	type taggedRun struct {
		service, shape, upgradeDatabase string
		layout                          *DatabaseLayout
		current                         ybCurrentBaseline
		upgraded                        string
		applied                         int
	}
	var runs []*taggedRun
	for _, service := range services {
		for _, shape := range []string{"declared", "distributed"} {
			run := &taggedRun{service: service, shape: shape, upgradeDatabase: service + "_upgrade_" + shape, layout: ybDatabaseLayout(t, service)}
			ybCreateDatabase(t, name, run.upgradeDatabase, ybShapeLayout(run.layout, shape))
			t.Cleanup(func() { ybDropDatabase(t, name, run.upgradeDatabase) })
			run.current = ybCreateCurrentBaseline(t, name, service, shape)
			runs = append(runs, run)
		}
	}
	if !t.Run("build", func(t *testing.T) {
		for _, run := range runs {
			t.Run(run.service+"/"+run.shape+"/upgrade", func(t *testing.T) {
				t.Parallel()
				ybApply(t, name, run.upgradeDatabase, ybLayoutSQL(t, run.layout, baselineAtTagOrRelease(t, fromTag, "schema/"+run.service+".sql", postTag)))
				for _, migration := range postTag {
					if migration.Database == run.service {
						ybApply(t, name, run.upgradeDatabase, ybLayoutSQL(t, run.layout, migration.content))
						run.applied++
					}
				}
				ybRequireAllIndexesValid(t, name, run.upgradeDatabase)
				if run.shape == "declared" {
					// Contract migrations drop relations whose tablets are removed asynchronously, so the upgraded
					// database is checked for placement only.
					ybRequireDeclaredPlacement(t, name, run.upgradeDatabase, run.layout, false)
				}
				run.upgraded = ybIntrospect(t, name, run.upgradeDatabase)
			})
			t.Run(run.service+"/"+run.shape+"/current", func(t *testing.T) {
				t.Parallel()
				run.current = ybLoadCurrentBaseline(t, name, run.current)
			})
		}
	}) {
		return
	}
	for _, run := range runs {
		t.Run(run.service+"/"+run.shape, func(t *testing.T) {
			requirePGSchemasEqual(t, "yugabyte "+run.service+" "+run.shape+" tagged upgrade vs current baseline", run.current.introspection, run.upgraded)
			t.Logf("yugabyte: upgraded %s %s baseline with %d migration(s) (%s)", run.service, fromTag, run.applied, run.shape)
		})
	}
}

// ybCurrentBaseline is a database holding a service's current baseline and, once loaded, the schema introspection
// taken right after the baseline was applied.
type ybCurrentBaseline struct {
	service, shape string
	database       string
	introspection  string
}

// ybCurrentBaselines holds the current-baseline databases this test process created, by engine, service, and shape,
// so the tagged upgrade contract and the capability contract read one database instead of loading two.
var ybCurrentBaselines sync.Map

// ybCurrentBaselineState is one created current-baseline database: loaded once its load finished, or the test whose
// load failed.
type ybCurrentBaselineState struct {
	current  ybCurrentBaseline
	failedIn string
}

func ybCurrentBaselineKey(name, service, shape string) string {
	return name + "/" + service + "/" + shape
}

func ybShapeLayout(layout *DatabaseLayout, shape string) *DatabaseLayout {
	if shape == "declared" {
		return layout
	}
	return &DatabaseLayout{Database: layout.Database, Layout: DatabaseLayoutDistributed}
}

// ybCreateCurrentBaseline creates <service>_current_<shape>, in the service's declared layout or distributed, unless
// this process already loaded it on this engine.
func ybCreateCurrentBaseline(t *testing.T, name, service, shape string) ybCurrentBaseline {
	t.Helper()
	if existing, ok := ybCurrentBaselines.Load(ybCurrentBaselineKey(name, service, shape)); ok {
		state := existing.(*ybCurrentBaselineState)
		if state.failedIn != "" {
			t.Fatalf("the %s %s current baseline failed to load in %s; its error is reported there", service, shape, state.failedIn)
		}
		return state.current
	}
	current := ybCurrentBaseline{service: service, shape: shape, database: service + "_current_" + shape}
	ybCreateDatabase(t, name, current.database, ybShapeLayout(ybDatabaseLayout(t, service), shape))
	ybCurrentBaselines.Store(ybCurrentBaselineKey(name, service, shape), &ybCurrentBaselineState{current: current})
	return current
}

// ybLoadCurrentBaseline applies the current baseline with the service's layout, as the yugabyte role applies it, then
// requires valid indexes and, in the declared shape, the layout's placement and tablet count, and records the schema
// introspection before any contract changes the database.
func ybLoadCurrentBaseline(t *testing.T, name string, current ybCurrentBaseline) ybCurrentBaseline {
	t.Helper()
	if current.introspection != "" {
		return current
	}
	loaded := false
	defer func() {
		if !loaded {
			ybCurrentBaselines.Store(ybCurrentBaselineKey(name, current.service, current.shape), &ybCurrentBaselineState{current: current, failedIn: t.Name()})
		}
	}()
	layout := ybDatabaseLayout(t, current.service)
	baseline, err := dbsql.Content.ReadFile("schema/" + current.service + ".sql")
	if err != nil {
		t.Fatalf("read current %s baseline: %v", current.service, err)
	}
	ybApply(t, name, current.database, ybLayoutSQL(t, layout, string(baseline)))
	ybRequireAllIndexesValid(t, name, current.database)
	if current.shape == "declared" {
		ybRequireDeclaredPlacement(t, name, current.database, layout, true)
	}
	current.introspection = ybIntrospect(t, name, current.database)
	ybCurrentBaselines.Store(ybCurrentBaselineKey(name, current.service, current.shape), &ybCurrentBaselineState{current: current})
	loaded = true
	return current
}

func TestYugabyteTaggedMigrationPaths(t *testing.T) {
	requireDocker(t)
	ybVerifyTaggedMigrationPaths(t)
}

// TestYugabyteColocatedDDLAbortsAreRetryable proves the engine-level contract services rely on during online expand
// migrations: DDL on one colocated table aborts an open transaction on a sibling table with an error the shared
// retry classifier replays.
func TestYugabyteColocatedDDLAbortsAreRetryable(t *testing.T) {
	requireDocker(t)
	name := ybStart(t, fmt.Sprintf("fw-sv-yb-abort-%d", time.Now().UnixNano()))
	database := "layout_ddl_abort_probe"
	ybCreateDatabase(t, name, database, &DatabaseLayout{Database: database, Layout: DatabaseLayoutColocated})
	t.Cleanup(func() { ybDropDatabase(t, name, database) })
	ybApply(t, name, database, `
CREATE TABLE public.dml_target (id int PRIMARY KEY, v int);
CREATE TABLE public.ddl_target (id int PRIMARY KEY);
INSERT INTO public.dml_target VALUES (1, 0);`)

	transaction := make(chan error, 1)
	go func() {
		_, err := dockerWithTimeout(t, 2*time.Minute, `\set VERBOSITY verbose
BEGIN;
UPDATE public.dml_target SET v = v + 1 WHERE id = 1;
SELECT pg_sleep(6);
UPDATE public.dml_target SET v = v + 1 WHERE id = 1;
COMMIT;
`, "exec", "-i", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", database, "-v", "ON_ERROR_STOP=1", "-q")
		transaction <- err
	}()
	time.Sleep(2 * time.Second)
	ybApply(t, name, database, "ALTER TABLE public.ddl_target ADD COLUMN added int;")

	err := <-transaction
	var failure *dockerError
	if !errors.As(err, &failure) {
		t.Fatalf("transaction on a sibling colocated table survived concurrent DDL (err=%v); the retry contract no longer applies", err)
	}
	match := regexp.MustCompile(`ERROR:\s+([0-9A-Z]{5}):\s+(.*)`).FindStringSubmatch(failure.stderr)
	if match == nil {
		t.Fatalf("aborted transaction reported no SQLSTATE:\n%s", failure.stderr)
	}
	abort := &pq.Error{Code: pq.ErrorCode(match[1]), Message: match[2]}
	if !pkgdatabase.IsRetryablePostgresError(abort) {
		t.Fatalf("colocated DDL abort SQLSTATE %s (%s) is not classified retryable", match[1], match[2])
	}
	t.Logf("yugabyte: colocated DDL aborted a sibling transaction with retryable SQLSTATE %s", match[1])
}

func TestYugabyteCurrentBaselinesAndCapabilities(t *testing.T) {
	requireDocker(t)
	name := fmt.Sprintf("fw-sv-yb-%d", time.Now().UnixNano())
	name = ybStart(t, name)

	services, _ := yugabyteServiceDatabases(t)
	// selected maps each selected service database to the Yugabyte database holding its current declared baseline.
	selected := make(map[string]string, len(services))
	splitProven := false
	for _, service := range services {
		layout := ybDatabaseLayout(t, service)
		database := ybLoadCurrentBaseline(t, name, ybCreateCurrentBaseline(t, name, service, "declared")).database
		selected[service] = database
		if layout.Colocated() && !splitProven {
			ybRequireDistributedTableSplits(t, name, database)
			splitProven = true
		}
		runtimeRole := service + "_runtime"
		ybEnsureLoginRole(t, name, runtimeRole)
		ybGrantRuntimeRole(t, name, database, service, runtimeRole)
	}
	for _, binary := range pkgdatabase.CapabilityServices() {
		databaseName, ownsDatabase := releases.ServiceDatabaseLookup(binary)
		if !ownsDatabase || strings.TrimSpace(databaseName) == "" {
			t.Fatalf("PostgreSQL capability service %q has no catalogued Yugabyte database", binary)
		}
		database, ok := selected[databaseName]
		if !ok {
			continue
		}
		for _, capability := range pkgdatabase.CapabilitiesFor(binary, pkgdatabase.EnginePostgres) {
			ybApply(t, name, database, fmt.Sprintf("SET ROLE %s_runtime; %s; RESET ROLE;", databaseName, capability.Probe))
		}
	}
	if purser, ok := selected["purser"]; ok {
		if out, ddlErr := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", purser, "-v", "ON_ERROR_STOP=1", "-c", "SET ROLE purser_runtime; CREATE TABLE purser.runtime_role_must_not_create (id integer)"); ddlErr == nil {
			t.Fatalf("Yugabyte runtime role unexpectedly created a table: %s", out)
		}
		purserSeed, err := dbsql.Content.ReadFile(demoSeeds["purser"])
		if err != nil {
			t.Fatalf("read Purser demo seed: %v", err)
		}
		ybApply(t, name, purser, string(purserSeed))
		ybApply(t, name, purser, string(purserSeed))
	}
	// These statements represent concrete runtime assumptions not proven by
	// merely accepting DDL: JSONB null normalization, conflict inference,
	// transactional advisory locks, and work-queue row locking.
	if purser, ok := selected["purser"]; ok {
		ybApply(t, name, purser, `
BEGIN;
SELECT pg_advisory_xact_lock(8675309);
SELECT COALESCE(NULL::jsonb, '{}'::jsonb);
SELECT id FROM purser.stripe_meter_events_outbox
FOR UPDATE SKIP LOCKED;
ROLLBACK;
`)
	}
	if commodore, ok := selected["commodore"]; ok {
		ybApply(t, name, commodore, `
BEGIN;
SELECT pg_advisory_xact_lock(hashtext('tenant-contract'), hashtext('stream-contract'));
ROLLBACK;
`)
	}
}
