//go:build schema_verify

package provisioner

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

func ybStart(t *testing.T, name string) {
	t.Helper()
	rmContainer(t, name)
	image := infrastructureContractImage(t, "yugabyte")
	if _, err := docker(t, "", "run", "-d", "--name", name, image,
		"bin/yugabyted", "start", "--background=false", "--advertise_address=127.0.0.1"); err != nil {
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
	if len(queries) != 44 {
		t.Fatalf("found %d generated Navigator queries, want 44", len(queries))
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

func TestYugabyteCurrentBaselinesAndCapabilities(t *testing.T) {
	requireDocker(t)
	const name = "fw-sv-yb"
	ybStart(t, name)

	services := []string{"quartermaster", "purser", "foghorn", "commodore", "periscope", "navigator", "skipper"}
	for _, service := range services {
		if out, err := docker(t, "", "exec", name, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", "yugabyte", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE "+service); err != nil {
			t.Fatalf("create Yugabyte database %s: %v\n%s", service, err, out)
		}
		path := "schema/" + service + ".sql"
		schemaSQL, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		ybApply(t, name, service, string(schemaSQL))
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
}
