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
// unreadable marks a gap whose state could not be read: it still refuses, but it does not show the release unfinished.
type priorReleaseGap struct {
	release    string
	detail     string
	unreadable bool
}

// enforcePriorReleasesComplete refuses a move to target while an earlier release is unfinished on the cluster: a
// postdeploy migration of an earlier release missing from a PostgreSQL/YugabyteDB or ClickHouse ledger, or a required
// data migration of an earlier release that is not completed. `release apply` runs it before its first mutation and
// under --dry-run. The per-service pre-deploy gate repeats the ledger checks for each service it deploys.
//
// Only service databases that already carry a baseline marker or a ledger are read: a database the release creates is
// born from the current baseline and has no earlier release to finish.
//
// An interrupt (ctx ending) is reported as such, never as an unfinished release: the reads it cuts short fail as
// transport errors.
func enforcePriorReleasesComplete(ctx context.Context, out io.Writer, rc *resolvedCluster, sshPool *ssh.Pool, target string) error {
	if hasUnscopedDatabaseDeployments(rc.Manifest) {
		first := true
		return forEachDatabaseDeployment(rc.Manifest, func(view *inventory.Manifest) error {
			if !first {
				view.Infrastructure.ClickHouse = nil
			}
			first = false
			return enforcePriorReleasesComplete(ctx, out, rc.withDatabaseManifest(view), sshPool, target)
		})
	}

	manifest := rc.Manifest
	var gaps []priorReleaseGap
	fmt.Fprintf(out, "[preflight] checking that every release before %s is complete\n", target)

	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		gap, err := priorPostgresGap(ctx, out, rc, sshPool, pg, target)
		if interrupted := preflightInterrupted(ctx); interrupted != nil {
			return interrupted
		}
		if err != nil {
			return fmt.Errorf("[preflight] check earlier releases (postgres): %w", err)
		}
		if gap != nil {
			gaps = append(gaps, *gap)
		}
	}
	if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled && len(ch.Databases) > 0 {
		gap, err := priorClickHouseGap(ctx, out, rc, sshPool, ch, target)
		if interrupted := preflightInterrupted(ctx); interrupted != nil {
			return interrupted
		}
		if err != nil {
			return fmt.Errorf("[preflight] check earlier releases (clickhouse): %w", err)
		}
		if gap != nil {
			gaps = append(gaps, *gap)
		}
	}
	dataGaps, err := priorDataMigrationGaps(ctx, out, sshPool, manifest, target)
	if interrupted := preflightInterrupted(ctx); interrupted != nil {
		return interrupted
	}
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

// preflightInterrupted reports an interrupt of the read-only preflight once ctx has ended, and nil otherwise.
func preflightInterrupted(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("[preflight] interrupted (%w); nothing was changed", err)
	}
	return nil
}

func priorPostgresGap(ctx context.Context, out io.Writer, rc *resolvedCluster, sshPool *ssh.Pool, pg *inventory.PostgresConfig, target string) (*priorReleaseGap, error) {
	manifest := rc.Manifest
	candidates, err := manifestServiceDatabases(manifest, pg.Databases)
	if err != nil {
		return nil, fmt.Errorf("collect service databases: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	fmt.Fprintln(out, "[preflight] selecting the postgres admin host")
	host, err := postgresAdminHost(ctx, manifest, pg, sshPool)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "[preflight] reading service database state on %s (%d database(s))\n", hostLabel(host), len(candidates))
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
		fmt.Fprintf(out, "[preflight] reading postgres postdeploy ledger through %s on %s\n", v, hostLabel(host))
		return missingPostgresMigrationsFn(ctx, sshPool, host, pg, password, ledgered, "postdeploy", v)
	})
	if err != nil || len(missing) == 0 {
		return nil, err
	}
	return &priorReleaseGap{release: release, detail: "missing postdeploy migrations: " + migrationKeyList(missing)}, nil
}

func priorClickHouseGap(ctx context.Context, out io.Writer, rc *resolvedCluster, sshPool *ssh.Pool, ch *inventory.ClickHouseConfig, target string) (*priorReleaseGap, error) {
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
		fmt.Fprintf(out, "[preflight] reading clickhouse postdeploy ledger through %s on %s\n", v, hostLabel(host))
		return missingClickHouseMigrationsFn(ctx, sshPool, host, ch.EffectivePort(), env["CLICKHOUSE_PASSWORD"], ch.Databases, "postdeploy", v)
	})
	if err != nil || len(missing) == 0 {
		return nil, err
	}
	return &priorReleaseGap{release: release, detail: "missing clickhouse postdeploy migrations: " + migrationKeyList(missing)}, nil
}

func priorDataMigrationGaps(ctx context.Context, out io.Writer, sshPool *ssh.Pool, manifest *inventory.Manifest, target string) ([]priorReleaseGap, error) {
	catalog, err := loadReleaseCatalog()
	if err != nil {
		return nil, fmt.Errorf("embedded release catalog failed to load: %w", err)
	}
	reqs := preflight.CatalogRequirements(catalog, target)
	if len(reqs) == 0 {
		return nil, nil
	}
	src := progressDataMigrationSource(out, manifestHostFor(manifest), priorDataMigrationSourceFn(sshPool, manifest))
	blockers, err := datamigrate.PreDeployBlockers(ctx, src, reqs, target, releases.CompareSemver, releases.BaseVersion)
	if err != nil {
		return nil, err
	}
	gaps := make([]priorReleaseGap, 0, len(blockers))
	for _, blocker := range blockers {
		r := blocker.Requirement
		if blocker.Unreadable() {
			gaps = append(gaps, priorReleaseGap{
				release:    releases.BaseVersion(r.IntroducedIn),
				detail:     fmt.Sprintf("required data migration %s/%s: %s", r.Service, r.ID, blocker.Reason),
				unreadable: true,
			})
			continue
		}
		gaps = append(gaps, priorReleaseGap{
			release: releases.BaseVersion(r.IntroducedIn),
			detail:  fmt.Sprintf("required data migration %s/%s is %s (run: frameworks cluster data-migrate run %s.%s)", r.Service, r.ID, blocker.Reason, r.Service, r.ID),
		})
	}
	return gaps, nil
}

// progressDataMigrationSource announces each data-migration state read before it runs, because each one is several
// sequential SSH calls, and names the manifest host on a result whose source did not.
func progressDataMigrationSource(out io.Writer, hostFor preflight.HostResolver, src datamigrate.StateSource) datamigrate.StateSource {
	return func(ctx context.Context, service, id string) datamigrate.LiveStatus {
		host, ok := hostFor(service)
		where := "no host in the manifest"
		if ok {
			where = hostLabel(host)
		}
		fmt.Fprintf(out, "[preflight] reading data-migration state: %s/%s on %s\n", service, id, where)
		live := src(ctx, service, id)
		if live.Host == "" && ok {
			live.Host = hostLabel(host)
		}
		return live
	}
}

// hostLabel names a host by its manifest name, or its address when it has none.
func hostLabel(host inventory.Host) string {
	return firstNonEmpty(host.Name, host.ExternalIP)
}

// priorReleaseRefusal names the lowest unfinished release, because it is the one to apply next, and lists everything
// unfinished so one refusal covers every engine. Gaps whose state could not be read are listed separately: they refuse
// without showing any release unfinished, so they never name a release to apply.
func priorReleaseRefusal(target string, gaps []priorReleaseGap) error {
	sort.SliceStable(gaps, func(i, j int) bool { return releases.CompareSemver(gaps[i].release, gaps[j].release) < 0 })
	var unfinished, unreadable []priorReleaseGap
	for _, gap := range gaps {
		if gap.unreadable {
			unreadable = append(unreadable, gap)
		} else {
			unfinished = append(unfinished, gap)
		}
	}
	var b strings.Builder
	if len(unfinished) > 0 {
		first := unfinished[0].release
		fmt.Fprintf(&b, "[preflight] refusing to apply %s: the cluster has not completed release %s. Nothing was changed.\n", target, first)
		for _, gap := range unfinished {
			fmt.Fprintf(&b, "  - %s: %s\n", gap.release, gap.detail)
		}
		fmt.Fprintf(&b, "Apply %s first (`frameworks cluster release apply --version %s`), including its postdeploy migrations and required data migrations, then retry %s. Skipping an unfinished release is not supported.", first, first, target)
		if len(unreadable) > 0 {
			b.WriteString("\nThe state of these required data migrations could not be read either:\n")
			writeUnreadableGaps(&b, unreadable)
		}
		return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
	}
	fmt.Fprintf(&b, "[preflight] refusing to apply %s: the state of %d required data migration(s) could not be read, so the earlier releases cannot be verified complete. Nothing was changed.\n", target, len(unreadable))
	writeUnreadableGaps(&b, unreadable)
	fmt.Fprintf(&b, "Resolve the read error above (host reachability, service install), then retry %s.", target)
	return fmt.Errorf("%s", b.String())
}

func writeUnreadableGaps(b *strings.Builder, gaps []priorReleaseGap) {
	for _, gap := range gaps {
		fmt.Fprintf(b, "  - %s (introduced in %s)\n", gap.detail, gap.release)
	}
}

func migrationKeyList(keys []provisioner.MigrationKey) string {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, key.String())
	}
	return strings.Join(names, ", ")
}
