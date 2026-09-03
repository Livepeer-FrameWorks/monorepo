//go:build schema_verify

package provisioner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
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
		"--tserver_flags=yb_enable_read_committed_isolation=false"); err != nil {
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
	if out, err := docker(t, sql, "exec", "-i", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", db, "-v", "ON_ERROR_STOP=1", "-q"); err != nil {
		t.Fatalf("apply SQL to %s/%s: %v\n%s", name, db, err, out)
	}
}

func ybIntrospect(t *testing.T, name, db string) string {
	t.Helper()
	out, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", db, "-tAc", pgIntrospectQuery)
	if err != nil {
		t.Fatalf("introspect Yugabyte schema %s: %v\n%s", db, err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func ybCreateDatabase(t *testing.T, name, databaseName string) {
	t.Helper()
	if out, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE "+databaseName); err != nil {
		t.Fatalf("create Yugabyte database %s: %v\n%s", databaseName, err, out)
	}
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
	for _, service := range services {
		t.Run(service, func(t *testing.T) {
			upgradeDatabase := service + "_upgrade"
			currentDatabase := service + "_current"
			ybCreateDatabase(t, name, upgradeDatabase)
			t.Cleanup(func() { ybDropDatabase(t, name, upgradeDatabase) })
			ybCreateDatabase(t, name, currentDatabase)
			t.Cleanup(func() { ybDropDatabase(t, name, currentDatabase) })
			ybApply(t, name, upgradeDatabase, repositoryFileAtTag(t, fromTag, "pkg/database/sql/schema/"+service+".sql"))

			applied := 0
			for _, migration := range postTag {
				if migration.Database == service {
					ybApply(t, name, upgradeDatabase, migration.content)
					applied++
				}
			}
			currentBaseline, readErr := dbsql.Content.ReadFile("schema/" + service + ".sql")
			if readErr != nil {
				t.Fatalf("read current %s baseline: %v", service, readErr)
			}
			ybApply(t, name, currentDatabase, string(currentBaseline))
			ybRequireAllIndexesValid(t, name, upgradeDatabase)
			ybRequireAllIndexesValid(t, name, currentDatabase)
			requirePGSchemasEqual(t, "yugabyte "+service+" tagged upgrade vs current baseline",
				ybIntrospect(t, name, currentDatabase), ybIntrospect(t, name, upgradeDatabase))
			t.Logf("yugabyte: upgraded %s %s baseline with %d migration(s)", service, fromTag, applied)
		})
	}
}

func TestYugabyteTaggedMigrationPaths(t *testing.T) {
	requireDocker(t)
	ybVerifyTaggedMigrationPaths(t)
}

func TestYugabyteCurrentBaselinesAndCapabilities(t *testing.T) {
	requireDocker(t)
	name := fmt.Sprintf("fw-sv-yb-%d", time.Now().UnixNano())
	name = ybStart(t, name)

	services, _ := yugabyteServiceDatabases(t)
	selected := make(map[string]struct{}, len(services))
	for _, service := range services {
		selected[service] = struct{}{}
		if out, err := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE "+service); err != nil {
			t.Fatalf("create Yugabyte database %s: %v\n%s", service, err, out)
		}
		path := "schema/" + service + ".sql"
		schemaSQL, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		ybApply(t, name, service, string(schemaSQL))
		ybRequireAllIndexesValid(t, name, service)
		runtimeRole := service + "_runtime"
		if out, createErr := docker(t, "", "exec", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", "CREATE ROLE "+runtimeRole+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION"); createErr != nil {
			t.Fatalf("create Yugabyte runtime role %s: %v\n%s", runtimeRole, createErr, out)
		}
		ybApply(t, name, service, fmt.Sprintf(`
GRANT CONNECT ON DATABASE %[1]s TO %[2]s;
GRANT USAGE ON SCHEMA %[1]s TO %[2]s;
REVOKE CREATE ON SCHEMA %[1]s FROM %[2]s;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %[1]s TO %[2]s;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %[1]s TO %[2]s;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA %[1]s TO %[2]s;
ALTER DEFAULT PRIVILEGES FOR ROLE yugabyte IN SCHEMA %[1]s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %[2]s;
ALTER DEFAULT PRIVILEGES FOR ROLE yugabyte IN SCHEMA %[1]s GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %[2]s;
ALTER DEFAULT PRIVILEGES FOR ROLE yugabyte IN SCHEMA %[1]s GRANT EXECUTE ON FUNCTIONS TO %[2]s;
`, service, runtimeRole))
	}
	databaseByService := map[string]string{
		"commodore": "commodore", "foghorn": "foghorn", "navigator": "navigator",
		"periscope-metering": "periscope", "purser": "purser", "quartermaster": "quartermaster", "skipper": "skipper",
	}
	for _, binary := range pkgdatabase.CapabilityServices() {
		databaseName := databaseByService[binary]
		if _, ok := selected[databaseName]; !ok {
			continue
		}
		for _, capability := range pkgdatabase.CapabilitiesFor(binary, pkgdatabase.EnginePostgres) {
			if databaseName == "" {
				t.Fatalf("PostgreSQL capability service %q has no Yugabyte database", binary)
			}
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
