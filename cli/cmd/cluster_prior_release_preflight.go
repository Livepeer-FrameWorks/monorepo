package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/preflight"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

// Prior-release preflight seams. Production reads the migration ledgers and service data-migration state over SSH;
// tests substitute scripted ledgers and states.
var (
	missingPostgresMigrationsFn   = provisioner.MissingMigrationsForDatabases
	missingClickHouseMigrationsFn = provisioner.MissingClickHouseMigrationsForDatabases
	priorDataMigrationSourceFn    = func(pool *ssh.Pool, manifest *inventory.Manifest) datamigrate.StateSource {
		return preflight.SSHStateSource(pool, manifestHostFor(manifest), manifestRuntimeFor(manifest))
	}
)

// priorReleaseGap is what one engine or the data-migration state reports as unfinished for an earlier release.
type priorReleaseGap struct {
	release string
	detail  string
}

// enforcePriorReleasesComplete refuses a move to target while an earlier release is unfinished on the cluster: a
// postdeploy migration of an earlier release missing from a PostgreSQL/YugabyteDB or ClickHouse ledger, or a required
// data migration of an earlier release that is not completed. `release apply` runs it before its first mutation and
// under --dry-run. The per-service pre-deploy gate repeats the ledger checks for each service it deploys.
//
// Only service databases that already carry a baseline marker or a ledger are read: a database the release creates is
// born from the current baseline and has no earlier release to finish.
func enforcePriorReleasesComplete(ctx context.Context, out io.Writer, rc *resolvedCluster, sshPool *ssh.Pool, target string) error {
	manifest := rc.Manifest
	var gaps []priorReleaseGap

	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		gap, err := priorPostgresGap(ctx, rc, sshPool, pg, target)
		if err != nil {
			return fmt.Errorf("[preflight] check earlier releases (postgres): %w", err)
		}
		if gap != nil {
			gaps = append(gaps, *gap)
		}
	}
	if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled && len(ch.Databases) > 0 {
		gap, err := priorClickHouseGap(ctx, rc, sshPool, ch, target)
		if err != nil {
			return fmt.Errorf("[preflight] check earlier releases (clickhouse): %w", err)
		}
		if gap != nil {
			gaps = append(gaps, *gap)
		}
	}
	dataGaps, err := priorDataMigrationGaps(ctx, sshPool, manifest, target)
	if err != nil {
		return fmt.Errorf("[preflight] check earlier releases (data migrations): %w", err)
	}
	gaps = append(gaps, dataGaps...)

	if len(gaps) > 0 {
		return priorReleaseRefusal(target, gaps)
	}
	fmt.Fprintf(out, "[preflight] every release before %s is complete (postdeploy migrations and required data migrations).\n", target)
	return nil
}

func priorPostgresGap(ctx context.Context, rc *resolvedCluster, sshPool *ssh.Pool, pg *inventory.PostgresConfig, target string) (*priorReleaseGap, error) {
	manifest := rc.Manifest
	candidates, err := manifestServiceDatabases(manifest, pg.Databases)
	if err != nil {
		return nil, fmt.Errorf("collect service databases: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	host, err := postgresAdminHost(ctx, manifest, pg, sshPool)
	if err != nil {
		return nil, err
	}
	states, err := readServiceDatabaseStatesFn(ctx, sshPool, host, pg, candidates)
	if err != nil {
		return nil, fmt.Errorf("probe service databases: %w", err)
	}
	var ledgered []provisioner.SchemaDatabase
	for _, database := range candidates {
		if states[database.Name].Initialized {
			ledgered = append(ledgered, database)
		}
	}
	if len(ledgered) == 0 {
		return nil, nil
	}
	var sharedEnv map[string]string
	if pg.IsYugabyte() && pg.Password == "" {
		env, envErr := rc.SharedEnv()
		if envErr != nil {
			return nil, fmt.Errorf("load manifest env_files: %w", envErr)
		}
		sharedEnv = env
	}
	password, err := resolveYugabytePassword(pg, sharedEnv)
	if err != nil {
		return nil, err
	}
	release, missing, err := firstIncompletePriorRelease(target, releases.ReleasesBelow, func(v string) ([]provisioner.MigrationKey, error) {
		return missingPostgresMigrationsFn(ctx, sshPool, host, pg, password, ledgered, "postdeploy", v)
	})
	if err != nil || len(missing) == 0 {
		return nil, err
	}
	return &priorReleaseGap{release: release, detail: "missing postdeploy migrations: " + migrationKeyList(missing)}, nil
}

func priorClickHouseGap(ctx context.Context, rc *resolvedCluster, sshPool *ssh.Pool, ch *inventory.ClickHouseConfig, target string) (*priorReleaseGap, error) {
	host, ok := rc.Manifest.GetHost(ch.CoordinatorHost())
	if !ok {
		return nil, fmt.Errorf("cannot resolve clickhouse coordinator host %q", ch.CoordinatorHost())
	}
	host.Name = firstNonEmpty(host.Name, ch.CoordinatorHost())
	env, err := rc.SharedEnv()
	if err != nil {
		return nil, fmt.Errorf("load manifest env_files: %w", err)
	}
	release, missing, err := firstIncompletePriorRelease(target, releases.ReleasesBelow, func(v string) ([]provisioner.MigrationKey, error) {
		return missingClickHouseMigrationsFn(ctx, sshPool, host, ch.EffectivePort(), env["CLICKHOUSE_PASSWORD"], ch.Databases, "postdeploy", v)
	})
	if err != nil || len(missing) == 0 {
		return nil, err
	}
	return &priorReleaseGap{release: release, detail: "missing clickhouse postdeploy migrations: " + migrationKeyList(missing)}, nil
}

func priorDataMigrationGaps(ctx context.Context, sshPool *ssh.Pool, manifest *inventory.Manifest, target string) ([]priorReleaseGap, error) {
	catalog, err := loadReleaseCatalog()
	if err != nil {
		return nil, fmt.Errorf("embedded release catalog failed to load: %w", err)
	}
	reqs := preflight.CatalogRequirements(catalog, target)
	if len(reqs) == 0 {
		return nil, nil
	}
	blockers, err := datamigrate.PreDeployBlockers(ctx, priorDataMigrationSourceFn(sshPool, manifest), reqs, target, releases.CompareSemver, releases.BaseVersion)
	if err != nil {
		return nil, err
	}
	gaps := make([]priorReleaseGap, 0, len(blockers))
	for _, blocker := range blockers {
		r := blocker.Requirement
		gaps = append(gaps, priorReleaseGap{
			release: releases.BaseVersion(r.IntroducedIn),
			detail:  fmt.Sprintf("required data migration %s/%s is %s (run: frameworks cluster data-migrate run %s.%s)", r.Service, r.ID, blocker.Reason, r.Service, r.ID),
		})
	}
	return gaps, nil
}

// priorReleaseRefusal names the lowest unfinished release, because it is the one to apply next, and lists everything
// unfinished so one refusal covers every engine.
func priorReleaseRefusal(target string, gaps []priorReleaseGap) error {
	sort.SliceStable(gaps, func(i, j int) bool { return releases.CompareSemver(gaps[i].release, gaps[j].release) < 0 })
	first := gaps[0].release
	var b strings.Builder
	fmt.Fprintf(&b, "[preflight] refusing to apply %s: the cluster has not completed release %s. Nothing was changed.\n", target, first)
	for _, gap := range gaps {
		fmt.Fprintf(&b, "  - %s: %s\n", gap.release, gap.detail)
	}
	fmt.Fprintf(&b, "Apply %s first (`frameworks cluster release apply --version %s`), including its postdeploy migrations and required data migrations, then retry %s. Skipping an unfinished release is not supported.", first, first, target)
	return fmt.Errorf("%s", b.String())
}

func migrationKeyList(keys []provisioner.MigrationKey) string {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, key.String())
	}
	return strings.Join(names, ", ")
}
