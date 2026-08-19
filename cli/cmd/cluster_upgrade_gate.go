package cmd

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/preflight"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"

	"github.com/spf13/cobra"
)

// serviceIsDatabaseBacked reports whether the topology declares a primary database dependency for a deploy — the
// authority for "this service must have a catalog-declared database owner". Keyed by deploy name, which equals the
// canonical service id for every database-backed service (aliases like foghorn-eu deploy as foghorn).
func serviceIsDatabaseBacked(deployName string) bool {
	for _, dep := range topology.InfraDependencies(deployName) {
		if dep.Kind == topology.InfraDatabase {
			return true
		}
	}
	return false
}

// serviceDependsOnClickHouse reports whether the topology declares a ClickHouse dependency for a deploy (Periscope), so
// the pre-deploy gate knows to verify ClickHouse migrations for it in addition to Postgres.
func serviceDependsOnClickHouse(deployName string) bool {
	for _, dep := range topology.InfraDependencies(deployName) {
		if dep.Kind == topology.InfraClickHouse {
			return true
		}
	}
	return false
}

// checkPostgresMigrationGate verifies, for a database-backed service, that every Postgres EXPAND migration up to the
// target and every HIGHEST-PRIOR postdeploy migration is already applied — checked against the actual _migrations
// ledger (the authority), never a detected running version. MissingMigrationsForDatabases is CUMULATIVE, so checking
// the highest prior release once covers all prior postdeploy; the target's own postdeploy is excluded (it runs after
// deploy).
func checkPostgresMigrationGate(ctx context.Context, rc *resolvedCluster, sshPool *ssh.Pool, manifest *inventory.Manifest, dbName, serviceName, target string) error {
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return fmt.Errorf("[gate] %s is database-backed but postgres is not enabled in the manifest; cannot verify its migrations before deploy", serviceName)
	}
	dbHost, ok := resolvePGHost(manifest, pg)
	if !ok {
		return fmt.Errorf("[gate] cannot resolve postgres host from manifest")
	}
	password, _ := resolveYugabytePassword(pg, sharedEnvForGate(rc)) //nolint:errcheck // missing yugabyte password is reported by ReadMigrationLedger
	databases := schemaDatabasesFromConfigs([]inventory.DatabaseConfig{{Name: dbName}})
	if pg.IsYugabyte() {
		databases = yugabyteSchemaDatabases([]inventory.DatabaseConfig{{Name: dbName}}, manifest)
	}
	missingExpand, err := provisioner.MissingMigrationsForDatabases(ctx, sshPool, dbHost, pg, password, databases, "expand", target)
	if err != nil {
		return fmt.Errorf("[gate] check postgres expand migrations: %w", err)
	}
	missingPriorPostdeploy, priorVersion, err := highestPriorPostdeploy(ctx, target, func(v string) ([]provisioner.MigrationKey, error) {
		return provisioner.MissingMigrationsForDatabases(ctx, sshPool, dbHost, pg, password, databases, "postdeploy", v)
	})
	if err != nil {
		return fmt.Errorf("[gate] check postgres prior postdeploy: %w", err)
	}
	if len(missingExpand) > 0 || len(missingPriorPostdeploy) > 0 {
		return fmt.Errorf("%s", formatMigrationRemediation(serviceName, target, priorVersion, missingExpand, missingPriorPostdeploy))
	}
	return nil
}

// checkClickHouseMigrationGate is the ClickHouse counterpart: it verifies expand + highest-prior postdeploy for the
// analytics databases against the coordinator's ledger. Symmetric with checkPostgresMigrationGate so a ClickHouse-
// dependent service (Periscope) is gated on the same completeness the Postgres branch enforces.
func checkClickHouseMigrationGate(ctx context.Context, rc *resolvedCluster, sshPool *ssh.Pool, manifest *inventory.Manifest, serviceName, target string) error {
	ch := manifest.Infrastructure.ClickHouse
	if ch == nil || !ch.Enabled || len(ch.Databases) == 0 {
		return fmt.Errorf("[gate] %s depends on ClickHouse but ClickHouse is not enabled/configured in the manifest; cannot verify its migrations before deploy", serviceName)
	}
	host, ok := manifest.GetHost(ch.CoordinatorHost())
	if !ok {
		return fmt.Errorf("[gate] cannot resolve clickhouse coordinator host %q", ch.CoordinatorHost())
	}
	host.Name = firstNonEmpty(host.Name, ch.CoordinatorHost())
	env, err := rc.SharedEnv()
	if err != nil {
		return fmt.Errorf("[gate] load manifest env_files: %w", err)
	}
	password := env["CLICKHOUSE_PASSWORD"]
	port := ch.EffectivePort()
	missingExpand, err := provisioner.MissingClickHouseMigrationsForDatabases(ctx, sshPool, host, port, password, ch.Databases, "expand", target)
	if err != nil {
		return fmt.Errorf("[gate] check clickhouse expand migrations: %w", err)
	}
	missingPriorPostdeploy, priorVersion, err := highestPriorPostdeploy(ctx, target, func(v string) ([]provisioner.MigrationKey, error) {
		return provisioner.MissingClickHouseMigrationsForDatabases(ctx, sshPool, host, port, password, ch.Databases, "postdeploy", v)
	})
	if err != nil {
		return fmt.Errorf("[gate] check clickhouse prior postdeploy: %w", err)
	}
	if len(missingExpand) > 0 || len(missingPriorPostdeploy) > 0 {
		return fmt.Errorf("%s", formatMigrationRemediation(serviceName, target, priorVersion, missingExpand, missingPriorPostdeploy))
	}
	return nil
}

// highestPriorPostdeploy runs `check` against the HIGHEST prior release below the target's base (postdeploy is
// cumulative, so the highest prior subsumes all earlier ones) and returns the missing keys plus that prior version.
// When there is no prior release, it returns nil with an empty version.
func highestPriorPostdeploy(_ context.Context, target string, check func(priorVersion string) ([]provisioner.MigrationKey, error)) ([]provisioner.MigrationKey, string, error) {
	prior := releases.ReleasesBelow(target)
	if len(prior) == 0 {
		return nil, "", nil
	}
	priorVersion := prior[len(prior)-1].Version // ReleasesBelow is ascending
	missing, err := check(priorVersion)
	if err != nil {
		return nil, priorVersion, err
	}
	return missing, priorVersion, nil
}

// loadReleaseCatalog is the seam the migration and data-migration gates read the
// embedded catalog through. Production binds it to releases.CatalogOrError, so a
// corrupt catalog surfaces as an error and every gate fails closed. Tests
// override it to exercise that fail-closed wiring without reaching into the
// releases package (which parses once into an immutable value and exposes no
// mutation seam of its own). Not for reassignment by production code.
var loadReleaseCatalog = releases.CatalogOrError

// Engine-gate seams: production binds them to the real SSH-backed checks. Tests override them to observe the gate's
// engine ROUTING (e.g. that the ClickHouse branch runs when Postgres is disabled) without a live cluster.
var (
	checkPostgresMigrationGateFn   = checkPostgresMigrationGate
	checkClickHouseMigrationGateFn = checkClickHouseMigrationGate
	runBelowFloorGuardFn           = runBelowFloorGuard
)

// runUpgradePreDeployGate is the pre-deploy gate every cluster upgrade goes
// through:
//
//	(a) every expand migration up to targetPlatformVersion must be applied
//	(b) every required postdeploy migration in the catalog path EXCLUDING
//	    targetPlatformVersion must be applied — target postdeploy waits
//	    until after deploy + data migration
//	(c) prior-version required data migrations must be completed
//	(d) the target must be a declared release and this CLI must meet its
//	    min_cli_version (tooling floor)
//
// DB ownership is explicit catalog metadata. Generated DATABASE_* env vars are
// intentionally ignored because they are injected more broadly than ownership.
// validateStorageBackendAgreement is a PRE-DEPLOY MANIFEST-CONSISTENCY check: within the SAME manifest, a Foghorn
// deployment's planned S3 descriptor env (STORAGE_S3_BUCKET/ENDPOINT/REGION/PREFIX) must EXACTLY match the FULL S3
// descriptor tuple on the cluster(s) it belongs to — the values reconciled into the Quartermaster cluster row that
// Chandler serves from. Quartermaster owns the whole tuple INCLUDING s3_prefix, so prefix is compared here too: a
// Foghorn writing under one prefix while the cluster row (hence Chandler) addresses another would split the keyspace.
// This catches an INTERNALLY INCONSISTENT manifest (env and cluster config disagree) before it is applied. It does NOT
// read live state; the live-state guarantees are separate and stronger: Quartermaster refuses to repoint an established
// cluster row (upsertCluster), and Foghorn cross-checks its env against the live QM row on first boot and against its
// committed cell_storage_identity thereafter. Credentials are env-only and never compared. Bucket/endpoint/prefix
// compare RAW (untrimmed), matching Foghorn's byte-for-byte identity check; region compares on its effective value
// (empty→us-east-1).
func validateStorageBackendAgreement(manifest *inventory.Manifest, serviceID, deployName, effectiveCluster string, env map[string]string) error {
	if deployName != "foghorn" || manifest == nil {
		return nil
	}
	svc, ok := manifest.Services[serviceID]
	if !ok {
		return nil
	}
	// Every cluster the service is assigned to PLUS the effective (host-resolved) cluster — a service with no explicit
	// Cluster/Clusters is still deployed into its host's cluster, whose descriptor must agree.
	clusters := append([]string{}, svc.Clusters...)
	if svc.Cluster != "" {
		clusters = append(clusters, svc.Cluster)
	}
	if effectiveCluster != "" {
		clusters = append(clusters, effectiveCluster)
	}

	// Compare RAW values, NOT trimmed: Foghorn's immutable cell identity compares the descriptor byte-for-byte against
	// the live Quartermaster row, so a leading/trailing-whitespace difference genuinely addresses a different backend
	// and must fail here too — trimming would let it pass preflight and then fail at Foghorn startup after a partial
	// rollout. TrimSpace is used ONLY to decide whether S3 is configured at all.
	envBucket := env["STORAGE_S3_BUCKET"]
	if envBucket == "" {
		// The env declares NO S3. This is valid ONLY for a genuinely storage-less cell: if ANY assigned cluster declares
		// an s3_bucket, the cell would deploy storage-disabled against a cluster whose Chandler + first-boot establishment
		// expect a backend, and durable writes would silently fail after an apparently-successful deploy. Fail closed.
		seen := map[string]bool{}
		for _, cid := range clusters {
			if cid == "" || seen[cid] {
				continue
			}
			seen[cid] = true
			cc, ok := manifest.Clusters[cid]
			if !ok {
				continue
			}
			if strings.TrimSpace(cc.S3Bucket) != "" {
				return fmt.Errorf("storage backend missing: Foghorn %q has no STORAGE_S3_BUCKET but cluster %q declares s3_bucket=%q — an S3-declaring cluster requires the FULL descriptor (bucket/endpoint/region/prefix) in the Foghorn env; set STORAGE_S3_* to match, or clear the cluster descriptor for a genuinely storage-less cell", serviceID, cid, cc.S3Bucket)
			}
		}
		return nil // genuinely storage-less: env empty AND no assigned cluster declares an S3 backend
	}
	envEndpoint := env["STORAGE_S3_ENDPOINT"]
	envRegion := env["STORAGE_S3_REGION"]
	envPrefix := env["STORAGE_S3_PREFIX"]
	// Reject a blank-but-PRESENT (whitespace-only) descriptor field. Runtime GetEnv treats such a value as CONFIGURED
	// (nonempty), so a whitespace bucket/endpoint/region/prefix would pass a trimmed/effective compare here yet fail
	// Foghorn's byte-for-byte immutable identity after a partial deploy. A field that is genuinely unset ("") is
	// omitted (bucket → disabled above; region → us-east-1; prefix → empty), which is valid.
	for name, v := range map[string]string{
		"STORAGE_S3_BUCKET":   envBucket,
		"STORAGE_S3_ENDPOINT": envEndpoint,
		"STORAGE_S3_REGION":   envRegion,
		"STORAGE_S3_PREFIX":   envPrefix,
	} {
		if v != "" && strings.TrimSpace(v) == "" {
			return fmt.Errorf("storage backend misconfigured: Foghorn %q %s is whitespace-only (%q) — runtime treats a blank-but-present value as configured, so it would fail Foghorn's immutable identity after deploy; unset it or give it a real value", serviceID, name, v)
		}
	}

	seen := map[string]bool{}
	for _, cid := range clusters {
		if cid == "" || seen[cid] {
			continue
		}
		seen[cid] = true
		cc, ok := manifest.Clusters[cid]
		if !ok {
			continue
		}
		// An S3-enabled Foghorn MUST have a cluster S3 descriptor: it is what Quartermaster persists and Chandler
		// serves from, and it is the authority a first boot establishes against. An absent descriptor leaves Chandler
		// with no backend and lets Foghorn establish an unproven identity, so refuse it before deploy. Credentials stay
		// env-only.
		if cc.S3Bucket == "" {
			return fmt.Errorf("storage backend descriptor absent: Foghorn %q has STORAGE_S3_BUCKET=%q but cluster %q declares no s3_bucket — the cluster S3 descriptor (bucket/endpoint/region) is required so Chandler and first-boot establishment have an authoritative backend; populate it on the cluster before deploying", serviceID, envBucket, cid)
		}
		if cc.S3Bucket != envBucket {
			return fmt.Errorf("storage backend disagreement: Foghorn %q env STORAGE_S3_BUCKET=%q but cluster %q s3_bucket=%q — the cell's S3 backend is immutable and must match the cluster Chandler serves from; reconcile before deploying", serviceID, envBucket, cid, cc.S3Bucket)
		}
		if cc.S3Endpoint != envEndpoint {
			return fmt.Errorf("storage backend disagreement: Foghorn %q env STORAGE_S3_ENDPOINT=%q but cluster %q s3_endpoint=%q; reconcile before deploying", serviceID, envEndpoint, cid, cc.S3Endpoint)
		}
		// Region compares on the EFFECTIVE value: an exactly-empty region defaults to us-east-1 on both Foghorn and the
		// cluster, so an unset cluster region does not false-mismatch a Foghorn env that defaulted to us-east-1. A
		// whitespace-only cluster region is NOT normalized away (see effectiveS3Region) — it is a real byte-for-byte
		// difference Foghorn's first-boot identity would reject, so it must fail here too rather than at runtime.
		if effectiveS3Region(cc.S3Region) != effectiveS3Region(envRegion) {
			return fmt.Errorf("storage backend disagreement: Foghorn %q env STORAGE_S3_REGION=%q but cluster %q s3_region=%q; reconcile before deploying", serviceID, envRegion, cid, cc.S3Region)
		}
		// Prefix is part of the immutable descriptor Quartermaster owns and Chandler serves from — compared exactly so a
		// Foghorn writing under one keyspace prefix cannot deploy against a cluster row addressing another. An absent
		// (nil) cluster prefix is rejected upstream by manifest validation when a bucket is declared; treat nil as "".
		ccPrefix := ""
		if cc.S3Prefix != nil {
			ccPrefix = *cc.S3Prefix
		}
		if ccPrefix != envPrefix {
			return fmt.Errorf("storage backend disagreement: Foghorn %q env STORAGE_S3_PREFIX=%q but cluster %q s3_prefix=%q; reconcile before deploying", serviceID, envPrefix, cid, ccPrefix)
		}
	}
	return nil
}

// effectiveS3Region mirrors Foghorn/Chandler's omitted-region default EXACTLY: they default only an exactly-empty
// region to us-east-1 and otherwise use the value byte-for-byte. The pre-deploy compare must match that contract, so a
// whitespace-only region is NOT treated as omitted here — trimming it would let an empty Foghorn env (effective
// us-east-1) agree with a whitespace-only cluster region at preflight and then fail Foghorn's exact first-boot identity
// check after deploy. (A whitespace-only region on the Foghorn ENV side is rejected outright by the blank-but-present
// guard in validateStorageBackendAgreement.)
func effectiveS3Region(region string) string {
	if region == "" {
		return "us-east-1"
	}
	return region
}

func runUpgradePreDeployGate(
	ctx context.Context,
	cmd *cobra.Command,
	rc *resolvedCluster,
	sshPool *ssh.Pool,
	manifest *inventory.Manifest,
	targetPlatformVersion string,
	serviceName string,
	deployName string,
	skipMigrationCheck bool,
	skipDataMigrationCheck bool,
) error {
	// Fail closed on a corrupt embedded catalog BEFORE any ownership lookup. ServiceDatabase() returns "" for both a
	// parse failure and a genuinely undeclared service, and the "" branch below skips the whole gate — so a corrupt
	// catalog would silently disable it here, before enforceCatalogPath is ever reached. Check the load error first.
	if _, err := loadReleaseCatalog(); err != nil {
		return fmt.Errorf("[gate] embedded release catalog failed to load: %w; refusing (cannot verify migration preconditions)", err)
	}

	// Which engines hold this service's schema is decided by TOPOLOGY (the authority), so the Postgres and ClickHouse
	// gates are INDEPENDENT topology-driven branches — a ClickHouse-only service (Postgres disabled) is not skipped
	// because a Postgres branch bailed. Database ownership for the Postgres branch is keyed by DEPLOY name (an aliased
	// foghorn-us owns the `foghorn` database; looking up the alias would find no owner and skip the gate).
	pgBacked := serviceIsDatabaseBacked(deployName)
	chBacked := serviceDependsOnClickHouse(deployName)
	dbName, declared := releases.ServiceDatabaseLookup(deployName)
	if pgBacked && !declared {
		// Undeclared ownership must FAIL CLOSED for a service the topology says needs a primary database — an omitted
		// catalog entry would otherwise skip its schema gate and let it deploy ahead of its own migrations.
		return fmt.Errorf("[gate] %s (deploy %q) is database-backed per topology but the release catalog declares no database ownership for it; refusing — declare it in the catalog's service_databases before upgrading", serviceName, deployName)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\n[gate] Pre-deploy migration gate (target %s)\n", targetPlatformVersion)

	// Catalog hard-skip refusals — a release precondition (target is a declared release, this CLI meets its min_cli
	// floor) that applies to EVERY service, DB-backed or not.
	if catalogErr := enforceCatalogPath(cmd, targetPlatformVersion); catalogErr != nil {
		return catalogErr
	}

	// SCHEMA checks are ENGINE-SPECIFIC: they run only for a service whose topology declares that engine. A DB-less
	// service has no schema to verify here (but is still subject to the catalog-wide DATA-migration check below).
	if skipMigrationCheck {
		fmt.Fprintln(cmd.OutOrStderr(), "[gate] WARNING: --skip-migration-check active; pre-deploy schema gate bypassed.")
	} else if pgBacked || chBacked {
		// PostgreSQL branch: expand + highest-prior postdeploy for this service's owned database.
		if pgBacked {
			if err := checkPostgresMigrationGateFn(ctx, rc, sshPool, manifest, dbName, serviceName, targetPlatformVersion); err != nil {
				return err
			}
		}
		// ClickHouse branch (INDEPENDENT): expand + highest-prior postdeploy for the analytics databases.
		if chBacked {
			if err := checkClickHouseMigrationGateFn(ctx, rc, sshPool, manifest, serviceName, targetPlatformVersion); err != nil {
				return err
			}
		}
		// Baseline-floor guard (Postgres AND ClickHouse). The expand/postdeploy checks use the same builder that EXCLUDES
		// migrations folded below the declared baseline floor, so a cluster with an incomplete folded ledger would pass
		// them yet be missing schema. The floor guard fails closed on such a cluster before any binary mutates.
		if err := runBelowFloorGuardFn(ctx, rc, sshPool); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "[gate] %s (deploy %q) has no primary-database or ClickHouse dependency; no engine schema checks.\n", serviceName, deployName)
	}

	// (c) prior required data migrations — checked for EVERY service, NOT only DB-backed ones. A catalog data migration's
	// required_before_version gates deploying any newer binary that depends on the migrated data, independent of whether
	// the upgrading service owns a schema; coupling this to schema ownership would let a DB-less consumer deploy ahead of
	// an incomplete data migration.
	if !skipDataMigrationCheck {
		// Fail closed on a corrupt catalog (already guaranteed non-error by the check at the top of this function, but
		// read through the seam so this gate never silently treats corruption as "no requirements").
		catalog, err := loadReleaseCatalog()
		if err != nil {
			return fmt.Errorf("[gate] embedded release catalog failed to load: %w; refusing (cannot verify prior data migrations)", err)
		}
		if len(catalog) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "[gate] release catalog is empty; no prior data migrations to evaluate.")
		} else {
			reqs := preflight.CatalogRequirements(catalog, targetPlatformVersion)
			if len(reqs) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "[gate] no required data migrations declared up to %s.\n", targetPlatformVersion)
			} else {
				src := preflight.SSHStateSource(sshPool, manifestHostFor(manifest), manifestRuntimeFor(manifest))
				blockers, err := datamigrate.PreDeployBlockers(ctx, src, reqs, targetPlatformVersion, releases.CompareSemver, releases.BaseVersion)
				if err != nil {
					return fmt.Errorf("[gate] check prior data migrations: %w", err)
				}
				if len(blockers) > 0 {
					return fmt.Errorf("[gate] prior data migrations required before deploying %s %s:\n%s\n\nrun: frameworks cluster data-migrate run <id>",
						serviceName, targetPlatformVersion, formatBlockers(blockers))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[gate] %d prior required data migration(s) completed.\n", len(reqs))
			}
		}
	} else {
		fmt.Fprintln(cmd.OutOrStderr(), "[gate] WARNING: --skip-data-migration-check active; prior data migration gate bypassed.")
	}

	fmt.Fprintln(cmd.OutOrStdout(), "[gate] OK — proceeding with deploy.")
	return nil
}

// enforceCatalogPath validates the catalog PRECONDITIONS that apply to EVERY service being upgraded (not only
// DB-backed ones — it runs unconditionally in the pre-deploy gate): the target must be a declared release and this CLI
// must meet its min_cli_version tooling floor. It deliberately does NOT depend on the running service's version —
// migration completeness is proven separately against the actual _migrations ledger (the prior-postdeploy scan checks
// the highest prior release's CUMULATIVE postdeploy set against the ledger, so a skewed/unreadable replica version
// cannot block a ledger-proven rollout). It is NOT a version compatibility floor. Empty catalog ⇒ no-op.
func enforceCatalogPath(cmd *cobra.Command, target string) error {
	// A corrupt embedded catalog must FAIL CLOSED (it would otherwise silently disable this gate and the data-migration
	// gate), not be read as "nothing to enforce". Reading through the seam surfaces the parse error instead of an
	// ambiguous empty slice. This runs before the expand/postdeploy scan, so it gates the whole check.
	catalog, err := loadReleaseCatalog()
	if err != nil {
		return fmt.Errorf("[gate] embedded release catalog failed to load: %w; refusing (cannot verify migration preconditions)", err)
	}
	if len(catalog) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "[gate] release catalog is empty; no catalog preconditions to enforce.")
		return nil
	}
	tgt := releases.Lookup(target)
	if tgt == nil {
		return fmt.Errorf("[gate] target release %s is not declared in the embedded release catalog; update the CLI/catalog before upgrading a DB-backed service", target)
	}
	// Same fail-closed floor as the fetched-manifest gate (checkCLIVersionFloor): a non-concrete local build (dev/
	// unknown) is REFUSED here too rather than silently exempted, so a locally built CLI cannot ignore a release's
	// required tooling floor on the upgrade path. --unsafe-ignore-cli-version-floor is the only escape hatch.
	return checkCLIVersionFloor(cmd.ErrOrStderr(), tgt.MinCLIVersion, unsafeCLIFloor(cmd))
}

// resolvePGHost returns the host used by upgrade-gate SQL checks. Runtime
// services and `cluster migrate` have separate HA-aware Yugabyte selection.
func resolvePGHost(manifest *inventory.Manifest, pg *inventory.PostgresConfig) (inventory.Host, bool) {
	if pg.IsYugabyte() && len(pg.Nodes) > 0 {
		return manifest.GetHost(pg.Nodes[0].Host)
	}
	return manifest.GetHost(pg.Host)
}

// sharedEnvForGate returns the shared env map if it's already loaded; the
// gate cannot tolerate a SOPS failure mid-flight — runUpgrade has already
// required a successful load before reaching the gate.
func sharedEnvForGate(rc *resolvedCluster) map[string]string {
	env, _ := rc.SharedEnv() //nolint:errcheck // already validated upstream in runUpgrade
	return env
}

// manifestHostFor returns a HostResolver for the cluster CLI: services
// listed in manifest.Services / Interfaces resolve to their declared host.
func manifestHostFor(manifest *inventory.Manifest) preflight.HostResolver {
	return func(service string) (inventory.Host, bool) {
		if svc, ok := manifest.Services[service]; ok && svc.Enabled {
			return firstServiceHost(manifest, svc)
		}
		if svc, ok := manifest.Interfaces[service]; ok && svc.Enabled {
			return firstServiceHost(manifest, svc)
		}
		if svc, ok := manifest.Observability[service]; ok && svc.Enabled {
			return firstServiceHost(manifest, svc)
		}
		return inventory.Host{}, false
	}
}

func manifestRuntimeFor(manifest *inventory.Manifest) preflight.RuntimeResolver {
	return func(service string) string {
		if svc, ok := manifest.Services[service]; ok && svc.Enabled {
			return serviceRuntimeName(service, svc)
		}
		if svc, ok := manifest.Interfaces[service]; ok && svc.Enabled {
			return serviceRuntimeName(service, svc)
		}
		if svc, ok := manifest.Observability[service]; ok && svc.Enabled {
			return serviceRuntimeName(service, svc)
		}
		return service
	}
}

func firstServiceHost(manifest *inventory.Manifest, svc inventory.ServiceConfig) (inventory.Host, bool) {
	if svc.Host != "" {
		return manifest.GetHost(svc.Host)
	}
	if len(svc.Hosts) > 0 {
		return manifest.GetHost(svc.Hosts[0])
	}
	return inventory.Host{}, false
}

func serviceRuntimeName(service string, svc inventory.ServiceConfig) string {
	if strings.TrimSpace(svc.Deploy) != "" {
		return strings.TrimSpace(svc.Deploy)
	}
	return service
}

// formatMigrationRemediation builds the pre-deploy refusal message, pointing the operator at the RIGHT phase(s):
// expand migrations are applied with `--phase expand --to-version <target>`, while missing PRIOR postdeploy migrations
// are applied with `--phase postdeploy --to-version <highest prior release>` — the two are separate commands, and the
// postdeploy version is the prior release (not the target, whose own postdeploy runs after this deploy). A phase with
// no missing migrations contributes no command line.
func formatMigrationRemediation(serviceName, targetVersion, priorPostdeployVersion string, missingExpand, missingPriorPostdeploy []provisioner.MigrationKey) string {
	msg := fmt.Sprintf("[gate] migrations required before deploying %s %s:\n%s",
		serviceName, targetVersion, formatMissingMigrations(missingExpand, missingPriorPostdeploy))
	if len(missingExpand) > 0 {
		msg += fmt.Sprintf("\n\nrun: frameworks cluster migrate --phase expand --to-version %s", targetVersion)
	}
	if len(missingPriorPostdeploy) > 0 {
		msg += fmt.Sprintf("\nrun: frameworks cluster migrate --phase postdeploy --to-version %s", priorPostdeployVersion)
	}
	return msg
}

func formatMissingMigrations(expand, postdeploy []provisioner.MigrationKey) string {
	var b strings.Builder
	if len(expand) > 0 {
		b.WriteString("  expand:\n")
		for _, m := range expand {
			fmt.Fprintf(&b, "    - %s\n", m.String())
		}
	}
	if len(postdeploy) > 0 {
		b.WriteString("  prior postdeploy:\n")
		for _, m := range postdeploy {
			fmt.Fprintf(&b, "    - %s\n", m.String())
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatBlockers(blockers []datamigrate.Blocker) string {
	var b strings.Builder
	for _, blk := range blockers {
		fmt.Fprintf(&b, "  - %s/%s (introduced %s): %s\n",
			blk.Requirement.Service, blk.Requirement.ID, blk.Requirement.IntroducedIn, blk.Reason)
	}
	return strings.TrimRight(b.String(), "\n")
}
