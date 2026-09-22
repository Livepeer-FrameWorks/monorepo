package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/bootstrap"
	"frameworks/api_billing/internal/database/purserdb"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"

	"gopkg.in/yaml.v3"
)

// runBootstrapCommand handles `purser bootstrap …` invocations. main()
// dispatches here when argv[1] == "bootstrap"; the remaining argv is passed
// in as args. Returns an exit code.
//
// Subcommand surface (per docs/architecture/bootstrap-desired-state.md):
//
//	purser bootstrap          --file ...           # apply
//	purser bootstrap          --file ... --check   # parse + schema validate, no DB
//	purser bootstrap          --file ... --dry-run # full reconcile in a tx that rolls back
//	purser bootstrap validate                      # cross-service invariant check (no file)
//
// --dry-run runs the same code path as apply against an outer transaction the
// dispatcher rolls back. Reconcilers themselves never call BeginTx/Commit.
//
// No-arg invocation of the binary itself still starts the gRPC server — the
// dispatcher in main() only branches when "bootstrap" is the first arg.
func runBootstrapCommand(args []string) int {
	if len(args) > 0 && args[0] == "validate" {
		return runBootstrapValidate(args[1:])
	}
	return runBootstrapApply(args)
}

// errBootstrapDryRun makes the retrying transaction helper roll back a
// successful dry-run reconcile.
var errBootstrapDryRun = errors.New("purser bootstrap dry-run")

func runBootstrapApply(args []string) int {
	fs := flag.NewFlagSet("purser bootstrap", flag.ContinueOnError)
	file := fs.String("file", "", "path to the rendered bootstrap desired-state YAML")
	check := fs.Bool("check", false, "parse + schema-validate only; no DB connection")
	dryRun := fs.Bool("dry-run", false, "run the full reconcile inside a transaction that rolls back; reports planned changes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(os.Stderr, "purser bootstrap: --file required")
		return 2
	}

	logger := logging.NewLoggerWithService("purser-bootstrap")

	desired, loadErr := loadDesiredState(*file)
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "purser bootstrap: %v\n", loadErr)
		return 1
	}

	if *check {
		embedded, embedErr := bootstrap.EmbeddedTiers()
		if embedErr != nil {
			fmt.Fprintf(os.Stderr, "purser bootstrap --check: load embedded catalog: %v\n", embedErr)
			return 1
		}
		if checkErr := bootstrap.Check(desired.Purser, embedded); checkErr != nil {
			fmt.Fprintf(os.Stderr, "purser bootstrap --check: %v\n", checkErr)
			return 1
		}
		fmt.Fprintf(os.Stdout, "purser bootstrap --check: %s OK (parse + tier refs + pricing models)\n", *file)
		return 0
	}

	config.LoadEnv(logger)
	cfg, err := config.Load[appconfig.PurserBootstrap](config.Options{Service: "purser", Logger: logger})
	if err != nil {
		fmt.Fprintf(os.Stderr, "purser bootstrap: %v\n", err)
		return 1
	}
	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "purser"
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	embedded, err := bootstrap.EmbeddedTiers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "purser bootstrap: load embedded catalog: %v\n", err)
		return 1
	}

	var qm *grpcQMClient
	if len(desired.Purser.CustomerBilling) > 0 {
		if cfg.ServiceToken == "" {
			fmt.Fprintln(os.Stderr, "purser bootstrap: SERVICE_TOKEN is required when the desired state declares customer_billing")
			return 1
		}
		qm, err = newGRPCQMClient(qmclient.GRPCConfig{
			GRPCAddr:      cfg.QuartermasterGRPCAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: cfg.AllowInsecure,
			CACertFile:    cfg.CAPath,
			ServerName:    cfg.QuartermasterGRPCTLSServerName,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "purser bootstrap: QM client: %v\n", err)
			return 1
		}
		defer qm.Close()
	}

	ctx := context.Background()
	var qmIface bootstrap.QMBootstrapClient
	if qm != nil {
		qmIface = qm
	}
	// Reconcile only writes through tx and reads Quartermaster, so a replayed
	// attempt is safe; its output is printed once, after the final attempt.
	var (
		out          *bootstrap.Sections
		reconcileErr error
		reconciled   bool
	)
	txErr := database.WithRetryablePostgresTxWithHook(ctx, db, nil, func(error, int) {
		out, reconcileErr, reconciled = nil, nil, false
	}, func(tx *sql.Tx) error {
		out, reconcileErr = bootstrap.Reconcile(ctx, tx, desired.Purser, embedded, qmIface)
		if reconcileErr != nil {
			return reconcileErr
		}
		reconciled = true
		if *dryRun {
			return errBootstrapDryRun
		}
		return nil
	})
	if out != nil {
		printPurserSection("billing_tier_catalog", out.BillingTierCatalog)
		printPurserSection("cluster_pricing", out.ClusterPricing)
		if len(desired.Purser.CustomerBilling) > 0 {
			printPurserSection("customer_billing", out.CustomerBilling)
		}
	}
	switch {
	case reconcileErr != nil:
		fmt.Fprintf(os.Stderr, "purser bootstrap: %v\n", reconcileErr)
		return 1
	case errors.Is(txErr, errBootstrapDryRun):
		fmt.Fprintln(os.Stdout, "purser bootstrap [dry-run] rolled back; no changes persisted")
		for _, op := range out.PostCommit {
			fmt.Fprintf(os.Stdout, "purser bootstrap [dry-run] would: %s tenant=%s cluster=%s\n", op.Kind, op.Alias, op.ClusterID)
		}
		return 0
	case txErr != nil && !reconciled:
		fmt.Fprintf(os.Stderr, "purser bootstrap: begin tx: %v\n", txErr)
		return 1
	case txErr != nil:
		fmt.Fprintf(os.Stderr, "purser bootstrap: commit: %v\n", txErr)
		return 1
	}
	if len(out.PostCommit) > 0 {
		// Cross-service entitlement runs after the local Purser tx is durable.
		// Per-op failures are reported and the command exits non-zero so the
		// operator can retry — every QM handler this dispatches to is
		// idempotent.
		if qm == nil {
			return 0
		}
		failures := 0
		for _, op := range out.PostCommit {
			if applyErr := applyPostCommitOp(ctx, db, qm.client, op); applyErr != nil {
				failures++
				fmt.Fprintf(os.Stderr, "purser bootstrap post-commit %s tenant=%s cluster=%s: %v\n",
					op.Kind, op.Alias, op.ClusterID, applyErr)
			}
		}
		if failures > 0 {
			fmt.Fprintf(os.Stderr, "purser bootstrap: %d post-commit op(s) failed (subscription rows committed; re-run to retry)\n", failures)
			return 1
		}
	}
	return 0
}

func applyPostCommitOp(ctx context.Context, db *sql.DB, client *qmclient.GRPCClient, op bootstrap.PostCommitOp) error {
	switch op.Kind {
	case bootstrap.PostCommitGrantClusterAccess:
		// Bootstrap CLI runs admin/system-tenant grants; per-tenant runtime
		// caps come from Purser tier entitlements during admission, not from
		// tenant_cluster_access. Passing nil keeps that access row empty unless
		// an operator later adds a tenant/cluster-specific override.
		return client.BootstrapClusterAccess(ctx, op.TenantID, op.ClusterID, nil, nil)
	case bootstrap.PostCommitSetPrimaryCluster:
		clusterID := op.ClusterID
		return client.UpdateTenantCluster(ctx, &quartermasterpb.UpdateTenantClusterRequest{
			TenantId: op.TenantID, PrimaryClusterId: &clusterID,
		})
	case bootstrap.PostCommitSetDeploymentTier:
		observedAt := time.Now().UTC()
		dnsEntitlements, err := purserdb.New(db).LoadEffectiveDNSEntitlements(ctx, op.TenantID)
		if err != nil {
			return fmt.Errorf("load effective DNS entitlements: %w", err)
		}
		_, err = client.ApplyTenantBillingEntitlements(ctx, &quartermasterpb.ApplyTenantBillingEntitlementsRequest{
			TenantId:               op.TenantID,
			DeploymentTier:         dnsEntitlements.TierName,
			CustomSubdomainEnabled: dnsEntitlements.CustomSubdomainEnabled,
			CustomDomainEnabled:    dnsEntitlements.CustomDomainEnabled,
			ObservedAt:             timestamppb.New(observedAt),
		})
		return err
	default:
		return fmt.Errorf("unknown post-commit op %q", op.Kind)
	}
}

func printPurserSection(name string, r bootstrap.Result) {
	fmt.Fprintf(os.Stdout, "purser bootstrap %s: created=%d updated=%d noop=%d\n",
		name, len(r.Created), len(r.Updated), len(r.Noop))
}

// runBootstrapValidate runs the cross-service invariant check: every QM
// platform-official cluster has a matching purser.cluster_pricing row. It
// queries Quartermaster (gRPC) and Purser's own DB; the rendered file is not
// an input. Run after `purser bootstrap` apply has committed.
func runBootstrapValidate(args []string) int {
	fs := flag.NewFlagSet("purser bootstrap validate", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	logger := logging.NewLoggerWithService("purser-bootstrap-validate")
	config.LoadEnv(logger)
	cfg, err := config.Load[appconfig.PurserBootstrapValidate](config.Options{Service: "purser", Logger: logger})
	if err != nil {
		fmt.Fprintf(os.Stderr, "purser bootstrap validate: %v\n", err)
		return 1
	}

	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "purser"
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	missing, err := bootstrap.ValidatePlatformOfficialPricingCoverage(ctx, db, qmclient.GRPCConfig{
		GRPCAddr:      cfg.QuartermasterGRPCAddr,
		ServiceToken:  cfg.ServiceToken,
		Logger:        logger,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.QuartermasterGRPCTLSServerName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "purser bootstrap validate: %v\n", err)
		return 1
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "purser bootstrap validate: %d platform-official cluster(s) without pricing rows: %v\n", len(missing), missing)
		return 1
	}
	fmt.Fprintln(os.Stdout, "purser bootstrap validate: every platform-official cluster has a pricing row")
	return 0
}

// grpcQMClient is the cobra-side implementation of bootstrap.QMBootstrapClient.
// It bundles the Quartermaster gRPC connection used by both alias resolution
// and platform-official cluster lookup, plus the post-commit grant calls. The
// bootstrap reconcile package itself stays free of any gRPC types.
type grpcQMClient struct {
	client *qmclient.GRPCClient
}

func newGRPCQMClient(cfg qmclient.GRPCConfig) (*grpcQMClient, error) {
	client, err := qmclient.NewGRPCClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("dial quartermaster: %w", err)
	}
	return &grpcQMClient{client: client}, nil
}

func (q *grpcQMClient) Resolve(ctx context.Context, alias string) (string, error) {
	resp, err := q.client.ResolveTenantAliases(ctx, []string{alias})
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

func (q *grpcQMClient) PlatformOfficialClusterIDs(ctx context.Context) ([]string, error) {
	resp, err := q.client.ListOfficialClusters(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListOfficialClusters: %w", err)
	}
	out := make([]string, 0, len(resp.GetClusters()))
	for _, c := range resp.GetClusters() {
		out = append(out, c.GetClusterId())
	}
	return out, nil
}

func (q *grpcQMClient) Close() {
	if q.client != nil {
		_ = q.client.Close()
	}
}

// loadDesiredState reads + decodes the rendered bootstrap YAML for Purser. The
// decoder is lenient about unknown top-level keys (other services' sections) but
// strict on Purser's own section — typos like `cluster_priicng` fail parse.
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
	pNode, ok := top["purser"]
	if !ok {
		return out, nil
	}
	var purserBytes bytes.Buffer
	if err := yaml.NewEncoder(&purserBytes).Encode(&pNode); err != nil {
		return nil, fmt.Errorf("re-encode purser section: %w", err)
	}
	dec := yaml.NewDecoder(&purserBytes)
	dec.KnownFields(true)
	if err := dec.Decode(&out.Purser); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse purser section in %s: %w", path, err)
	}
	return out, nil
}
