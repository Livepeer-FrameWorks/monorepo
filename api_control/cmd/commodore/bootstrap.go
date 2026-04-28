package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"frameworks/api_control/internal/bootstrap"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"

	"gopkg.in/yaml.v3"
)

// runBootstrapCommand handles `commodore bootstrap …` invocations. main()
// dispatches here when argv[1] == "bootstrap"; the remaining argv is passed
// in as args. Returns an exit code.
//
// Subcommand surface (per docs/architecture/bootstrap-desired-state.md):
//
//	commodore bootstrap --file ...                          # apply
//	commodore bootstrap --file ... --check                  # parse + schema validate
//	commodore bootstrap --file ... --dry-run                # full reconcile in a tx that rolls back
//	commodore bootstrap --file ... --reset-credentials      # rehash passwords for users
//	                                                          marked reset_credentials=true
//
// Without --reset-credentials, existing passwords are NEVER rewritten — even if
// the rendered file says reset_credentials=true. This protects against a stale
// rendered artifact silently rotating live credentials.
//
// --dry-run runs the same code path as apply against an outer transaction the
// dispatcher rolls back. Reconcilers themselves never call BeginTx/Commit.
func runBootstrapCommand(args []string) int {
	fs := flag.NewFlagSet("commodore bootstrap", flag.ContinueOnError)
	file := fs.String("file", "", "path to the rendered bootstrap desired-state YAML")
	check := fs.Bool("check", false, "parse + schema-validate only; no DB connection")
	dryRun := fs.Bool("dry-run", false, "run the full reconcile inside a transaction that rolls back; reports planned changes")
	resetCreds := fs.Bool("reset-credentials", false, "rehash passwords for users that set reset_credentials=true (otherwise: warn-and-skip the password rewrite, profile changes still applied)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(os.Stderr, "commodore bootstrap: --file required")
		return 2
	}

	logger := logging.NewLoggerWithService("commodore-bootstrap")

	desired, loadErr := loadDesiredState(*file)
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "commodore bootstrap: %v\n", loadErr)
		return 1
	}

	if *check {
		if checkErr := bootstrap.Check(*desired); checkErr != nil {
			fmt.Fprintf(os.Stderr, "commodore bootstrap --check: %v\n", checkErr)
			return 1
		}
		fmt.Fprintf(os.Stdout, "commodore bootstrap --check: %s OK (parse + intra-file references)\n", *file)
		return 0
	}

	config.LoadEnv(logger)
	dbURL := config.RequireEnv("DATABASE_URL")
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	resolver, err := newGRPCResolver(logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "commodore bootstrap: tenant resolver: %v\n", err)
		return 1
	}
	defer resolver.Close()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "commodore bootstrap: begin tx: %v\n", err)
		return 1
	}

	res, warnings, err := bootstrap.ReconcileAccounts(ctx, tx, desired.Accounts, resolver, *resetCreds)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "commodore bootstrap: warning: %s\n", w)
	}
	if err != nil {
		_ = tx.Rollback() //nolint:errcheck // already in error path
		fmt.Fprintf(os.Stderr, "commodore bootstrap: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "commodore bootstrap accounts: created=%d updated=%d noop=%d\n",
		len(res.Created), len(res.Updated), len(res.Noop))
	if *dryRun {
		if err := tx.Rollback(); err != nil {
			fmt.Fprintf(os.Stderr, "commodore bootstrap [dry-run] rollback: %v\n", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "commodore bootstrap [dry-run] rolled back; no changes persisted")
		return 0
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "commodore bootstrap: commit: %v\n", err)
		return 1
	}
	return 0
}

// grpcTenantResolver dials Quartermaster's TenantService and resolves
// bootstrap aliases through ResolveTenantAliases — the cross-service handoff
// for alias→UUID lookup. Implements bootstrap.TenantResolver.
type grpcTenantResolver struct {
	client *qmclient.GRPCClient
}

func newGRPCResolver(logger logging.Logger) (*grpcTenantResolver, error) {
	addr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      addr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
	})
	if err != nil {
		return nil, fmt.Errorf("dial quartermaster: %w", err)
	}
	return &grpcTenantResolver{client: client}, nil
}

func (r *grpcTenantResolver) Resolve(ctx context.Context, alias string) (string, error) {
	resp, err := r.client.ResolveTenantAliases(ctx, []string{alias})
	if err != nil {
		return "", fmt.Errorf("ResolveTenantAliases(%q): %w", alias, err)
	}
	if len(resp.GetUnknown()) > 0 {
		return "", fmt.Errorf("alias %q not in quartermaster.bootstrap_tenant_aliases (run quartermaster bootstrap first)", alias)
	}
	id, ok := resp.GetMapping()[alias]
	if !ok {
		return "", fmt.Errorf("alias %q: empty mapping in ResolveTenantAliases response", alias)
	}
	return id, nil
}

func (r *grpcTenantResolver) Close() {
	if r.client != nil {
		_ = r.client.Close()
	}
}

// loadDesiredState reads + decodes the rendered bootstrap YAML for Commodore.
// Two-pass decode: lenient on unknown top-level sections (quartermaster:, purser:),
// strict (KnownFields(true)) inside accounts entries so typos fail parse.
func loadDesiredState(path string) (*bootstrap.DesiredState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := &bootstrap.DesiredState{}
	accNode, ok := top["accounts"]
	if !ok {
		return out, nil
	}
	var buf bytes.Buffer
	if err := yaml.NewEncoder(&buf).Encode(&accNode); err != nil {
		return nil, fmt.Errorf("re-encode accounts section: %w", err)
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(&out.Accounts); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse accounts section in %s: %w", path, err)
	}
	return out, nil
}
