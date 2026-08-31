//go:build schema_verify

package provisioner

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

func yugabyteServiceDatabases(t *testing.T) []string {
	t.Helper()
	baselines := pgBaselineFiles(t)
	services := make([]string, 0, len(baselines))
	for _, baseline := range baselines {
		services = append(services, strings.TrimSuffix(filepath.Base(baseline), ".sql"))
	}
	return services
}

func ybStart(t *testing.T, name string) {
	t.Helper()
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
			return
		}
		if time.Now().After(deadline) {
			logs, _ := docker(t, "", "logs", "--tail", "80", name)
			t.Fatalf("%s did not become ready:\n%s", name, logs)
		}
		time.Sleep(time.Second)
	}
}

func ybApply(t *testing.T, name, db, sql string) {
	t.Helper()
	if out, err := docker(t, sql, "exec", "-i", name, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", db, "-v", "ON_ERROR_STOP=1", "-q"); err != nil {
		t.Fatalf("apply SQL to %s/%s: %v\n%s", name, db, err, out)
	}
}

func ybIntrospect(t *testing.T, name, db string) string {
	t.Helper()
	out, err := docker(t, "", "exec", name, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", db, "-tAc", pgIntrospectQuery)
	if err != nil {
		t.Fatalf("introspect Yugabyte schema %s: %v\n%s", db, err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func ybCreateDatabase(t *testing.T, name, databaseName string) {
	t.Helper()
	if out, err := docker(t, "", "exec", name, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE "+databaseName); err != nil {
		t.Fatalf("create Yugabyte database %s: %v\n%s", databaseName, err, out)
	}
}

func ybRequireAllIndexesValid(t *testing.T, name, databaseName string) {
	t.Helper()
	output, err := docker(t, "", "exec", name, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", databaseName, "-tAc", `
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

type yugabyteGeneratedQuery struct {
	file string
	name string
	sql  string
}

func generatedQueriesInDirectory(t *testing.T, directory string) []yugabyteGeneratedQuery {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(directory, "*.sql.go"))
	if err != nil {
		t.Fatal(err)
	}
	queries := make([]yugabyteGeneratedQuery, 0, len(paths))
	for _, path := range paths {
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for valueIndex, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					querySQL, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr != nil {
						t.Fatalf("unquote constant in %s: %v", path, unquoteErr)
					}
					if !strings.HasPrefix(querySQL, "-- name:") {
						continue
					}
					name := "unknown"
					if valueIndex < len(value.Names) {
						name = value.Names[valueIndex].Name
					}
					queries = append(queries, yugabyteGeneratedQuery{
						file: filepath.Base(path),
						name: name,
						sql:  querySQL,
					})
				}
			}
		}
	}
	return queries
}

func generatedServiceQueries(t *testing.T, relativeDirectory string) []yugabyteGeneratedQuery {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Yugabyte contract test path")
	}
	return generatedQueriesInDirectory(t, filepath.Join(filepath.Dir(currentFile), relativeDirectory))
}

func ybPreparePurserCatalog(t *testing.T, name string) {
	t.Helper()
	queries := generatedServiceQueries(t, "../../../api_billing/internal/database/purserdb")
	if len(queries) < 100 {
		t.Fatalf("found only %d generated Purser queries; Yugabyte catalog discovery is incomplete", len(queries))
	}
	var statements strings.Builder
	for index, query := range queries {
		preparedName := fmt.Sprintf("purser_contract_%d", index)
		fmt.Fprintf(&statements, "\\echo preparing %s from %s\n", query.name, query.file)
		fmt.Fprintf(&statements, "PREPARE %s AS %s;\n", preparedName, query.sql)
		fmt.Fprintf(&statements, "DEALLOCATE %s;\n", preparedName)
	}
	ybApply(t, name, "purser", statements.String())
}

func ybPrepareNavigatorCatalog(t *testing.T, name string) {
	t.Helper()
	queries := generatedServiceQueries(t, "../../../api_dns/internal/database/navigatordb")
	if len(queries) != 55 {
		t.Fatalf("found %d generated Navigator queries, want 55", len(queries))
	}
	var statements strings.Builder
	for index, query := range queries {
		preparedName := fmt.Sprintf("navigator_contract_%d", index)
		fmt.Fprintf(&statements, "\\echo preparing %s from %s\n", query.name, query.file)
		fmt.Fprintf(&statements, "PREPARE %s AS %s;\n", preparedName, query.sql)
		fmt.Fprintf(&statements, "DEALLOCATE %s;\n", preparedName)
	}
	ybApply(t, name, "navigator", statements.String())
}

func ybPrepareSkipperCatalog(t *testing.T, name string) {
	t.Helper()
	queries := generatedServiceQueries(t, "../../../api_consultant/internal/database/skipperdb")
	if len(queries) != 62 {
		t.Fatalf("found %d generated Skipper queries, want 62", len(queries))
	}
	var statements strings.Builder
	for index, query := range queries {
		preparedName := fmt.Sprintf("skipper_contract_%d", index)
		fmt.Fprintf(&statements, "\\echo preparing %s from %s\n", query.name, query.file)
		fmt.Fprintf(&statements, "PREPARE %s AS %s;\n", preparedName, query.sql)
		fmt.Fprintf(&statements, "DEALLOCATE %s;\n", preparedName)
	}
	ybApply(t, name, "skipper", statements.String())
}

func ybPrepareMeteringCatalog(t *testing.T, name string) {
	t.Helper()
	queries := generatedServiceQueries(t, "../../../api_analytics_query/internal/database/meteringdb")
	if len(queries) != 11 {
		t.Fatalf("found %d generated Periscope Metering queries, want 11", len(queries))
	}
	var statements strings.Builder
	for index, query := range queries {
		preparedName := fmt.Sprintf("metering_contract_%d", index)
		fmt.Fprintf(&statements, "\\echo preparing %s from %s\n", query.name, query.file)
		fmt.Fprintf(&statements, "PREPARE %s AS %s;\n", preparedName, query.sql)
		fmt.Fprintf(&statements, "DEALLOCATE %s;\n", preparedName)
	}
	ybApply(t, name, "periscope", statements.String())
}

func ybPrepareCommodoreCatalog(t *testing.T, name string) {
	t.Helper()
	queries := generatedServiceQueries(t, "../../../api_control/internal/database/commodoredb")
	if len(queries) != 305 {
		t.Fatalf("found %d generated Commodore queries, want 305", len(queries))
	}
	var statements strings.Builder
	for index, query := range queries {
		preparedName := fmt.Sprintf("commodore_contract_%d", index)
		fmt.Fprintf(&statements, "\\echo preparing %s from %s\n", query.name, query.file)
		fmt.Fprintf(&statements, "PREPARE %s AS %s;\n", preparedName, query.sql)
		fmt.Fprintf(&statements, "DEALLOCATE %s;\n", preparedName)
	}
	ybApply(t, name, "commodore", statements.String())
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
	serviceSet := make(map[string]struct{})
	for _, service := range yugabyteServiceDatabases(t) {
		serviceSet[service] = struct{}{}
	}
	for _, migration := range postTag {
		serviceSet[migration.Database] = struct{}{}
	}
	services := make([]string, 0, len(serviceSet))
	for service := range serviceSet {
		services = append(services, service)
	}
	sort.Strings(services)
	for _, service := range services {
		t.Run(service, func(t *testing.T) {
			name := fmt.Sprintf("fw-sv-yb-upgrade-%s-%d", service, time.Now().UnixNano())
			ybStart(t, name)
			upgradeDatabase := service + "_upgrade"
			currentDatabase := service + "_current"
			ybCreateDatabase(t, name, upgradeDatabase)
			ybCreateDatabase(t, name, currentDatabase)
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

func ybPrepareQuartermasterCatalog(t *testing.T, name string) {
	t.Helper()
	queries := generatedServiceQueries(t, "../../../api_tenants/internal/database/quartermasterdb")
	if len(queries) != 163 {
		t.Fatalf("found %d generated Quartermaster queries, want 163", len(queries))
	}
	var statements strings.Builder
	for index, query := range queries {
		preparedName := fmt.Sprintf("quartermaster_contract_%d", index)
		fmt.Fprintf(&statements, "\\echo preparing %s from %s\n", query.name, query.file)
		fmt.Fprintf(&statements, "PREPARE %s AS %s;\n", preparedName, query.sql)
		fmt.Fprintf(&statements, "DEALLOCATE %s;\n", preparedName)
	}
	ybApply(t, name, "quartermaster", statements.String())
}

func TestYugabyteTaggedMigrationPaths(t *testing.T) {
	requireDocker(t)
	ybVerifyTaggedMigrationPaths(t)
}

func TestYugabyteCurrentBaselinesAndCapabilities(t *testing.T) {
	requireDocker(t)
	name := fmt.Sprintf("fw-sv-yb-%d", time.Now().UnixNano())
	ybStart(t, name)

	for _, service := range yugabyteServiceDatabases(t) {
		if out, err := docker(t, "", "exec", name, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE "+service); err != nil {
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
		if out, createErr := docker(t, "", "exec", name, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", "CREATE ROLE "+runtimeRole+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION"); createErr != nil {
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
		for _, capability := range pkgdatabase.CapabilitiesFor(binary, pkgdatabase.EnginePostgres) {
			if databaseName == "" {
				t.Fatalf("PostgreSQL capability service %q has no Yugabyte database", binary)
			}
			ybApply(t, name, databaseName, fmt.Sprintf("SET ROLE %s_runtime; %s; RESET ROLE;", databaseName, capability.Probe))
		}
	}
	if out, ddlErr := docker(t, "", "exec", name, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", "purser", "-v", "ON_ERROR_STOP=1", "-c", "SET ROLE purser_runtime; CREATE TABLE purser.runtime_role_must_not_create (id integer)"); ddlErr == nil {
		t.Fatalf("Yugabyte runtime role unexpectedly created a table: %s", out)
	}
	purserSeed, err := dbsql.Content.ReadFile(demoSeeds["purser"])
	if err != nil {
		t.Fatalf("read Purser demo seed: %v", err)
	}
	ybApply(t, name, "purser", string(purserSeed))
	ybApply(t, name, "purser", string(purserSeed))
	ybPreparePurserCatalog(t, name)
	ybPrepareNavigatorCatalog(t, name)
	ybPrepareSkipperCatalog(t, name)
	ybPrepareMeteringCatalog(t, name)
	ybPrepareCommodoreCatalog(t, name)
	ybPrepareQuartermasterCatalog(t, name)

	// These statements represent concrete runtime assumptions not proven by
	// merely accepting DDL: JSONB null normalization, conflict inference,
	// transactional advisory locks, and work-queue row locking.
	ybApply(t, name, "purser", `
BEGIN;
SELECT pg_advisory_xact_lock(8675309);
SELECT COALESCE(NULL::jsonb, '{}'::jsonb);
SELECT id FROM purser.stripe_meter_events_outbox
FOR UPDATE SKIP LOCKED;
ROLLBACK;
`)
	ybApply(t, name, "commodore", `
BEGIN;
SELECT pg_advisory_xact_lock(hashtext('tenant-contract'), hashtext('stream-contract'));
ROLLBACK;
`)
}
