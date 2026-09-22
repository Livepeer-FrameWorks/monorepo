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
	"sort"
	"time"

	"frameworks/api_control/internal/appconfig"
	"frameworks/api_control/internal/bootstrap"
	commodoregrpc "frameworks/api_control/internal/grpc"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"

	"gopkg.in/yaml.v3"
)

// newSourceURIEncrypter constructs the same rotating keyring the runtime
// Commodore service uses for pull-input source URIs.
func newSourceURIEncrypter(cfg *appconfig.CommodoreBootstrap) (fieldcrypt.FieldCipher, error) {
	if cfg.JWTSecret == "" || cfg.FieldEncryptionKey == "" {
		return nil, errors.New("JWT_SECRET and FIELD_ENCRYPTION_KEY are required to encrypt pull-stream source URIs")
	}
	previous, err := fieldcrypt.ParseFieldKeySet(cfg.FieldEncryptionPreviousKeys)
	if err != nil {
		return nil, err
	}
	explicitLegacy, err := fieldcrypt.ParseLegacyFieldSecrets(cfg.FieldEncryptionLegacySecrets)
	if err != nil {
		return nil, err
	}
	legacy := make([][]byte, 0, len(previous)+len(explicitLegacy)+1)
	legacy = append(legacy, []byte(cfg.JWTSecret))
	legacy = append(legacy, explicitLegacy...)
	keyIDs := make([]string, 0, len(previous))
	for keyID := range previous {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	for _, keyID := range keyIDs {
		legacy = append(legacy, previous[keyID])
	}
	return fieldcrypt.NewFieldKeyring(
		cfg.FieldEncryptionKeyID,
		[]byte(cfg.FieldEncryptionKey), previous, legacy, "pull-source-uri",
	)
}

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
		requirements, reqErr := bootstrap.NodeSourceLocationRequirements(desired.Commodore)
		if reqErr != nil {
			fmt.Fprintf(os.Stderr, "commodore bootstrap --check: %v\n", reqErr)
			return 1
		}
		for _, requirement := range requirements {
			fmt.Fprintf(os.Stdout, "commodore bootstrap --check: requires node placement: %s\n", requirement)
		}
		fmt.Fprintf(os.Stdout, "commodore bootstrap --check: %s OK (parse + intra-file references)\n", *file)
		return 0
	}

	config.LoadEnv(logger)
	cfg, err := config.Load[appconfig.CommodoreBootstrap](config.Options{Service: "commodore", Logger: logger})
	if err != nil {
		fmt.Fprintf(os.Stderr, "commodore bootstrap: %v\n", err)
		return 1
	}
	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "commodore"
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	resolver, err := newGRPCResolver(logger, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "commodore bootstrap: tenant resolver: %v\n", err)
		return 1
	}
	defer resolver.Close()

	ctx := context.Background()
	if nodeErr := requireSourceLocationNodePlacement(ctx, desired.Commodore, db, resolver, cfg, logger); nodeErr != nil {
		fmt.Fprintf(os.Stderr, "commodore bootstrap: %v\n", nodeErr)
		return 1
	}

	// The reconcile body replays on retryable transaction failures, so its
	// report lines are buffered per attempt and printed once the outcome is known.
	var (
		out     []bootstrapReportLine
		bodyRan bool
		bodyErr error
	)
	report := func(stderr bool, format string, args ...any) {
		out = append(out, bootstrapReportLine{stderr: stderr, text: fmt.Sprintf(format, args...)})
	}
	reconcile := func(tx *sql.Tx) error {
		out, bodyRan, bodyErr = out[:0], true, nil
		fail := func(err error, format string, args ...any) error {
			report(true, format, args...)
			bodyErr = err
			return err
		}

		res, warnings, err := bootstrap.ReconcileAccounts(ctx, tx, desired.Accounts, resolver, *resetCreds)
		for _, w := range warnings {
			report(true, "commodore bootstrap: warning: %s\n", w)
		}
		if err != nil {
			return fail(err, "commodore bootstrap: %v\n", err)
		}
		report(false, "commodore bootstrap accounts: created=%d updated=%d noop=%d\n",
			len(res.Created), len(res.Updated), len(res.Noop))

		if streams := desired.Commodore.PullStreams; len(streams) > 0 {
			encrypter, encrypterErr := newSourceURIEncrypter(cfg)
			if encrypterErr != nil {
				return fail(encrypterErr, "commodore bootstrap: source URI encrypter: %v\n", encrypterErr)
			}
			clusterResolver := &grpcClusterResolver{client: resolver.client}
			psRes, reconcileErr := bootstrap.ReconcilePullStreams(ctx, tx, streams, resolver, clusterResolver, encrypter)
			if reconcileErr != nil {
				return fail(reconcileErr, "commodore bootstrap: %v\n", reconcileErr)
			}
			report(false, "commodore bootstrap pull_streams: created=%d updated=%d noop=%d\n",
				len(psRes.Created), len(psRes.Updated), len(psRes.Noop))
		}

		// Mist-native streams: always reconcile, even when the desired list is
		// empty. ReconcileMistNativeStreams handles upserts; an empty list means
		// "every previously-bootstrapped mist_native stream under the operator
		// tenant must be deleted". Without this call the declarative remove path
		// would be broken — pulling a stream out of bootstrap.yaml has to stop
		// the stream, not leave it running.
		mnStreams := desired.Commodore.MistNativeStreams
		var mnRes bootstrap.Result
		var mnErr error
		if len(mnStreams) > 0 {
			mnRes, mnErr = bootstrap.ReconcileMistNativeStreams(ctx, tx, mnStreams, resolver)
		} else {
			// No desired rows declared. Scope the prune to the operator/system
			// tenant — any other tenant's mist_native streams stay untouched
			// (the operator-tenant scope matches the render-time exec gate).
			mnRes, mnErr = bootstrap.PruneAllMistNativeStreams(ctx, tx, resolver, []string{bootstrap.SystemTenantAlias})
		}
		if mnErr != nil {
			return fail(mnErr, "commodore bootstrap: %v\n", mnErr)
		}
		report(false, "commodore bootstrap mist_native_streams: created=%d updated=%d noop=%d deleted=%d\n",
			len(mnRes.Created), len(mnRes.Updated), len(mnRes.Noop), len(mnRes.Deleted))

		// Source locations become the declared streams' own ingest placement rules
		// once every declared stream row exists in this transaction.
		locationRes, err := bootstrap.ReconcileStreamSourceLocations(ctx, tx, desired.Commodore, resolver)
		if err != nil {
			return fail(err, "commodore bootstrap: %v\n", err)
		}
		report(false, "commodore bootstrap stream source_locations: updated=%d noop=%d\n",
			len(locationRes.Updated), len(locationRes.Noop))
		return nil
	}

	// A replay discards the aborted attempt's report, so a later begin failure is not mistaken for a failed commit
	// of that attempt's work.
	resetAttempt := func(error, int) { out, bodyRan, bodyErr = out[:0], false, nil }
	var txErr error
	if *dryRun {
		txErr = database.WithRetryablePostgresRollbackTxWithHook(ctx, db, nil, resetAttempt, reconcile)
	} else {
		txErr = database.WithRetryablePostgresTxWithHook(ctx, db, nil, resetAttempt, reconcile)
	}
	bodyFailed := bodyErr != nil && errors.Is(txErr, bodyErr)
	finishFailed := txErr != nil && !bodyFailed && bodyRan && bodyErr == nil
	// The report describes work that was applied (or, for --dry-run, rolled back on purpose); an attempt whose commit
	// failed applied nothing, so only its failure is printed.
	if txErr == nil || bodyFailed {
		for _, line := range out {
			if line.stderr {
				fmt.Fprint(os.Stderr, line.text)
			} else {
				fmt.Fprint(os.Stdout, line.text)
			}
		}
	}
	switch {
	case bodyFailed:
		return 1
	case finishFailed && *dryRun:
		fmt.Fprintf(os.Stderr, "commodore bootstrap [dry-run] rollback: %v\n", txErr)
		return 1
	case finishFailed:
		fmt.Fprintf(os.Stderr, "commodore bootstrap: commit: %v\n", txErr)
		return 1
	case txErr != nil:
		fmt.Fprintf(os.Stderr, "commodore bootstrap: begin tx: %v\n", txErr)
		return 1
	}
	if *dryRun {
		fmt.Fprintln(os.Stdout, "commodore bootstrap [dry-run] rolled back; no changes persisted")
	}
	return 0
}

// requireSourceLocationNodePlacement runs Commodore's node selector gate for
// declared source locations that name nodes. It runs before the bootstrap
// transaction opens, so a refusal writes nothing in apply or --dry-run, and the
// capability refresh it may trigger never waits on bootstrap's row locks.
func requireSourceLocationNodePlacement(ctx context.Context, section bootstrap.CommodoreSection, db *sql.DB, resolver *grpcTenantResolver, cfg *appconfig.CommodoreBootstrap, logger logging.Logger) error {
	requirements, err := bootstrap.NodeSourceLocationRequirements(section)
	if err != nil || len(requirements) == 0 {
		return err
	}
	purserClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:           cfg.PurserGRPCAddr,
		Timeout:            10 * time.Second,
		Logger:             logger,
		ServiceToken:       cfg.ServiceToken,
		PreferServiceToken: true,
		AllowInsecure:      cfg.AllowInsecure,
		CACertFile:         cfg.CAPath,
		ServerName:         cfg.PurserGRPCTLSServerName,
	})
	if err != nil {
		return fmt.Errorf("node placement readiness: dial purser: %w", err)
	}
	defer func() { _ = purserClient.Close() }()
	foghornPool := foghornclient.NewPool(foghornclient.PoolConfig{
		ServiceToken:  cfg.ServiceToken,
		Timeout:       10 * time.Second,
		Logger:        logger,
		MaxIdleTime:   time.Minute,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.FoghornGRPCTLSServerName,
		AllowInsecure: cfg.AllowInsecure,
	})
	defer func() { _ = foghornPool.Close() }()
	checker := commodoregrpc.NewPlacementNodeChecker(commodoregrpc.PlacementNodeCheckerConfig{
		DB:                  db,
		Logger:              logger,
		QuartermasterClient: resolver.client,
		PurserClient:        purserClient,
		FoghornPool:         foghornPool,
		SystemTenantID:      cfg.SystemTenantUUID(),
	})
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return bootstrap.RequireSourceLocationNodePlacement(checkCtx, section, resolver, checker)
}

type bootstrapReportLine struct {
	stderr bool
	text   string
}

// grpcTenantResolver dials Quartermaster's TenantService and resolves
// bootstrap aliases through ResolveTenantAliases — the cross-service handoff
// for alias→UUID lookup. Implements bootstrap.TenantResolver.
type grpcTenantResolver struct {
	client *qmclient.GRPCClient
}

func newGRPCResolver(logger logging.Logger, cfg *appconfig.CommodoreBootstrap) (*grpcTenantResolver, error) {
	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      cfg.QuartermasterGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.QuartermasterGRPCTLSServerName,
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

// grpcClusterResolver lists media-capable clusters from Quartermaster's
// operator-only fleet catalog. This is intentionally broader than runtime
// tenant entitlement: bootstrap pull streams are platform-authored desired
// state for the system tenant, while Foghorn still checks signed tenant and
// cluster authority before placement. Implements bootstrap.ClusterCapabilityResolver.
type grpcClusterResolver struct {
	client *qmclient.GRPCClient
}

func (r *grpcClusterResolver) MediaClusterCapabilities(ctx context.Context) ([]pullsource.ClusterCapability, error) {
	if r.client == nil {
		return nil, fmt.Errorf("quartermaster client unavailable")
	}
	var (
		out   []pullsource.ClusterCapability
		after *string
	)
	for {
		resp, err := r.client.ListClusters(ctx, &commonpb.CursorPaginationRequest{
			First: 500,
			After: after,
		})
		if err != nil {
			return nil, fmt.Errorf("ListClusters: %w", err)
		}
		for _, c := range resp.GetClusters() {
			// "edge" type is the media-capable role in this codebase. Central
			// clusters host control plane only.
			if !models.ClusterTypeCanBePreferred(c.GetClusterType()) {
				continue
			}
			out = append(out, pullsource.ClusterCapability{
				ID:                      c.GetClusterId(),
				AllowPrivatePullSources: c.GetAllowPrivatePullSources(),
			})
		}
		page := resp.GetPagination()
		if page == nil || !page.GetHasNextPage() {
			break
		}
		next := page.GetEndCursor()
		if next == "" {
			return nil, fmt.Errorf("ListClusters: pagination cursor missing")
		}
		after = &next
	}
	return out, nil
}

// loadDesiredState reads + decodes the rendered bootstrap YAML for Commodore.
// Two-pass decode: lenient on unknown top-level sections (quartermaster:, purser:),
// strict (KnownFields(true)) inside accounts/commodore entries so typos fail parse.
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

	if accNode, ok := top["accounts"]; ok {
		var buf bytes.Buffer
		if err := yaml.NewEncoder(&buf).Encode(&accNode); err != nil {
			return nil, fmt.Errorf("re-encode accounts section: %w", err)
		}
		dec := yaml.NewDecoder(&buf)
		dec.KnownFields(true)
		if err := dec.Decode(&out.Accounts); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse accounts section in %s: %w", path, err)
		}
	}

	if cmdNode, ok := top["commodore"]; ok {
		var buf bytes.Buffer
		if err := yaml.NewEncoder(&buf).Encode(&cmdNode); err != nil {
			return nil, fmt.Errorf("re-encode commodore section: %w", err)
		}
		dec := yaml.NewDecoder(&buf)
		dec.KnownFields(true)
		if err := dec.Decode(&out.Commodore); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse commodore section in %s: %w", path, err)
		}
	}

	return out, nil
}
