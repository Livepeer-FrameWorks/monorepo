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
		"bin/yugabyted", "start", "--background=false", "--advertise_address=127.0.0.1",
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

func ybDropDatabase(t *testing.T, name, databaseName string) {
	t.Helper()
	if out, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", "DROP DATABASE "+databaseName); err != nil {
		t.Errorf("drop Yugabyte database %s: %v\n%s", databaseName, err, out)
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
	for _, service := range services {
		for _, shape := range []string{"declared", "distributed"} {
			t.Run(service+"/"+shape, func(t *testing.T) {
				upgradeDatabase := service + "_upgrade_" + shape
				currentDatabase := service + "_current_" + shape
				layout := ybDatabaseLayout(t, service)
				create := func(database string) {
					if shape == "declared" {
						ybCreateDatabase(t, name, database, layout)
					} else {
						ybCreateDatabase(t, name, database, &DatabaseLayout{Database: service, Layout: DatabaseLayoutDistributed})
					}
					t.Cleanup(func() { ybDropDatabase(t, name, database) })
				}
				create(upgradeDatabase)
				create(currentDatabase)
				ybApply(t, name, upgradeDatabase, ybLayoutSQL(t, layout, baselineAtTagOrRelease(t, fromTag, "schema/"+service+".sql", postTag)))

				applied := 0
				for _, migration := range postTag {
					if migration.Database == service {
						ybApply(t, name, upgradeDatabase, ybLayoutSQL(t, layout, migration.content))
						applied++
					}
				}
				currentBaseline, readErr := dbsql.Content.ReadFile("schema/" + service + ".sql")
				if readErr != nil {
					t.Fatalf("read current %s baseline: %v", service, readErr)
				}
				ybApply(t, name, currentDatabase, ybLayoutSQL(t, layout, string(currentBaseline)))
				ybRequireAllIndexesValid(t, name, upgradeDatabase)
				ybRequireAllIndexesValid(t, name, currentDatabase)
				if shape == "declared" {
					// Contract migrations drop relations whose tablets are removed asynchronously, so the upgraded database
					// is checked for placement only.
					ybRequireDeclaredPlacement(t, name, upgradeDatabase, layout, false)
					ybRequireDeclaredPlacement(t, name, currentDatabase, layout, true)
				}
				requirePGSchemasEqual(t, "yugabyte "+service+" "+shape+" tagged upgrade vs current baseline",
					ybIntrospect(t, name, currentDatabase), ybIntrospect(t, name, upgradeDatabase))
				t.Logf("yugabyte: upgraded %s %s baseline with %d migration(s) (%s)", service, fromTag, applied, shape)
			})
		}
	}
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
	selected := make(map[string]struct{}, len(services))
	splitProven := false
	for _, service := range services {
		selected[service] = struct{}{}
		layout := ybDatabaseLayout(t, service)
		ybCreateDatabase(t, name, service, layout)
		path := "schema/" + service + ".sql"
		schemaSQL, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		ybApply(t, name, service, ybLayoutSQL(t, layout, string(schemaSQL)))
		ybRequireAllIndexesValid(t, name, service)
		ybRequireDeclaredPlacement(t, name, service, layout, true)
		if layout.Colocated() && !splitProven {
			ybRequireDistributedTableSplits(t, name, service)
			splitProven = true
		}
		runtimeRole := service + "_runtime"
		if out, createErr := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", "CREATE ROLE "+runtimeRole+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION"); createErr != nil {
			t.Fatalf("create Yugabyte runtime role %s: %v\n%s", runtimeRole, createErr, out)
		}
		ybGrantRuntimeRole(t, name, service, service, runtimeRole)
	}
	for _, binary := range pkgdatabase.CapabilityServices() {
		databaseName, ownsDatabase := releases.ServiceDatabaseLookup(binary)
		if !ownsDatabase || strings.TrimSpace(databaseName) == "" {
			t.Fatalf("PostgreSQL capability service %q has no catalogued Yugabyte database", binary)
		}
		if _, ok := selected[databaseName]; !ok {
			continue
		}
		for _, capability := range pkgdatabase.CapabilitiesFor(binary, pkgdatabase.EnginePostgres) {
			ybApply(t, name, databaseName, fmt.Sprintf("SET ROLE %s_runtime; %s; RESET ROLE;", databaseName, capability.Probe))
		}
	}
	if _, ok := selected["purser"]; ok {
		if out, ddlErr := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", "purser", "-v", "ON_ERROR_STOP=1", "-c", "SET ROLE purser_runtime; CREATE TABLE purser.runtime_role_must_not_create (id integer)"); ddlErr == nil {
			t.Fatalf("Yugabyte runtime role unexpectedly created a table: %s", out)
		}
		purserSeed, err := dbsql.Content.ReadFile(demoSeeds["purser"])
		if err != nil {
			t.Fatalf("read Purser demo seed: %v", err)
		}
		ybApply(t, name, "purser", string(purserSeed))
		ybApply(t, name, "purser", string(purserSeed))
	}
	// These statements represent concrete runtime assumptions not proven by
	// merely accepting DDL: JSONB null normalization, conflict inference,
	// transactional advisory locks, and work-queue row locking.
	if _, ok := selected["purser"]; ok {
		ybApply(t, name, "purser", `
BEGIN;
SELECT pg_advisory_xact_lock(8675309);
SELECT COALESCE(NULL::jsonb, '{}'::jsonb);
SELECT id FROM purser.stripe_meter_events_outbox
FOR UPDATE SKIP LOCKED;
ROLLBACK;
`)
	}
	if _, ok := selected["commodore"]; ok {
		ybApply(t, name, "commodore", `
BEGIN;
SELECT pg_advisory_xact_lock(hashtext('tenant-contract'), hashtext('stream-contract'));
ROLLBACK;
`)
	}
}
