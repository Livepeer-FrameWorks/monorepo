package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"frameworks/api_tenants/internal/bootstrap"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"

	"gopkg.in/yaml.v3"
)

// runBootstrapCommand handles `quartermaster bootstrap …` invocations. main()
// dispatches here when argv[1] == "bootstrap"; the remaining argv (after the
// bootstrap word) is passed in as args. Returns an exit code.
//
// Subcommand surface (per docs/architecture/bootstrap-desired-state.md):
//
//	quartermaster bootstrap --file ...                 # apply
//	quartermaster bootstrap --file ... --check         # parse + schema validate, no DB
//	quartermaster bootstrap --file ... --dry-run       # full reconcile in a tx that rolls back
//
// --dry-run runs the same code path as apply against an outer transaction the
// dispatcher rolls back at the end. The reconcilers themselves never call
// BeginTx/Commit — that is the cobra layer's job.
//
// No-arg invocation of the binary itself still starts the server — this only
// fires when the operator (or Ansible) explicitly says `bootstrap`.
func runBootstrapCommand(args []string) int {
	fs := flag.NewFlagSet("quartermaster bootstrap", flag.ContinueOnError)
	file := fs.String("file", "", "path to the rendered bootstrap desired-state YAML")
	check := fs.Bool("check", false, "parse + schema-validate only; no DB connection")
	dryRun := fs.Bool("dry-run", false, "run the full reconcile inside a transaction that rolls back; reports planned changes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(os.Stderr, "quartermaster bootstrap: --file required")
		return 2
	}

	logger := logging.NewLoggerWithService("quartermaster-bootstrap")

	desired, loadErr := loadBootstrapDesiredState(*file)
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "quartermaster bootstrap: %v\n", loadErr)
		return 1
	}

	if *check {
		if checkErr := bootstrap.Check(desired.Quartermaster); checkErr != nil {
			fmt.Fprintf(os.Stderr, "quartermaster bootstrap --check: %v\n", checkErr)
			return 1
		}
		fmt.Fprintf(os.Stdout, "quartermaster bootstrap --check: %s OK (parse + intra-file references)\n", *file)
		return 0
	}

	config.LoadEnv(logger)
	dbURL := config.RequireEnv("DATABASE_URL")
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "quartermaster bootstrap: begin tx: %v\n", err)
		return 1
	}
	out, err := bootstrap.Reconcile(ctx, tx, desired.Quartermaster)
	if out != nil {
		printSection("tenants", out.Tenants)
		printSection("clusters", out.Clusters)
		printSection("nodes", out.Nodes)
		printSection("ingress", out.Ingress)
		printSection("service_registry", out.ServiceRegistry)
		printSection("system_tenant_cluster_access", out.SystemTenantAccess)
	}
	if err != nil {
		_ = tx.Rollback() //nolint:errcheck // already in error path
		fmt.Fprintf(os.Stderr, "quartermaster bootstrap: %v\n", err)
		return 1
	}
	if *dryRun {
		if err := tx.Rollback(); err != nil {
			fmt.Fprintf(os.Stderr, "quartermaster bootstrap [dry-run] rollback: %v\n", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "quartermaster bootstrap [dry-run] rolled back; no changes persisted")
		return 0
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "quartermaster bootstrap: commit: %v\n", err)
		return 1
	}
	return 0
}

func printSection(name string, r bootstrap.Result) {
	fmt.Fprintf(os.Stdout, "quartermaster bootstrap %s: created=%d updated=%d noop=%d\n",
		name, len(r.Created), len(r.Updated), len(r.Noop))
}

// loadBootstrapDesiredState reads + decodes the rendered bootstrap YAML for
// Quartermaster. Two-pass decode: lenient on unknown top-level sections (e.g.
// purser:, accounts:), strict (KnownFields(true)) inside the quartermaster:
// section so typos in QM-owned fields fail parse.
func loadBootstrapDesiredState(path string) (*bootstrap.DesiredState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := &bootstrap.DesiredState{}
	qmNode, ok := top["quartermaster"]
	if !ok {
		return out, nil
	}
	var buf bytes.Buffer
	if err := yaml.NewEncoder(&buf).Encode(&qmNode); err != nil {
		return nil, fmt.Errorf("re-encode quartermaster section: %w", err)
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(&out.Quartermaster); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse quartermaster section in %s: %w", path, err)
	}
	return out, nil
}
