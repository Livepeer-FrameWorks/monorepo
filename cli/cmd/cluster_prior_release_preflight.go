package cmd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/preflight"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"

	"github.com/spf13/cobra"
)

// Ledger seams shared by the prior-release preflight, the pre-deploy gate, and the below-floor guard. Production
// reads the migration ledgers, baseline markers, and service data-migration state over SSH; tests substitute
// scripted ledgers and states.
var (
	missingPostgresMigrationsFn   = provisioner.MissingMigrationsForDatabases
	missingClickHouseMigrationsFn = provisioner.MissingClickHouseMigrationsForDatabases
	postgresBelowFloorGapFn       = provisioner.PostgresBelowFloorGap
	clickHouseBelowFloorGapFn     = provisioner.ClickHouseBelowFloorGap
	priorDataMigrationSourceFn    = func(pool *ssh.Pool, manifest *inventory.Manifest) datamigrate.StateSource {
		return preflight.SSHStateSource(pool, manifestHostFor(manifest), manifestRuntimeFor(manifest))
	}
)

// priorReleaseGap is what one engine or the data-migration state reports as unfinished for an earlier release.
// unreadable marks a gap whose state could not be read: it still refuses, but it does not show the release unfinished.
// needsRelease marks a gap only applying the earlier release closes: a missing postdeploy migration, or a required data
// migration the installed binary cannot run.
type priorReleaseGap struct {
	release      string
	detail       string
	unreadable   bool
	needsRelease bool
}

// priorDataMigration is a required data migration of an earlier release whose state was read as pending or failed and
// whose service binary registers it. `release apply` runs it itself, behind the backup gate, before its first other
// mutation.
type priorDataMigration struct {
	release string
	service string
	id      string
	status  datamigrate.Status
}

func (m priorDataMigration) key() string { return m.service + "." + m.id }

// enforcePriorReleasesComplete refuses a move to target while an earlier release is unfinished on the cluster in a way
// `release apply` cannot finish itself: a postdeploy migration of an earlier release missing from a
// PostgreSQL/YugabyteDB or ClickHouse ledger, or a required data migration of an earlier release that is running,
// paused, unreadable or unknown to its installed binary. It returns the earlier releases' required data migrations that
// are pending or failed, which `release apply` runs before this release changes anything. It runs before the first
// mutation and under --dry-run. The per-service pre-deploy gate repeats the ledger checks for each service it deploys.
//
// Only service databases that already carry a baseline marker or a ledger are read: a database the release creates is
// born from the current baseline and has no earlier release to finish.
//
// An interrupt (ctx ending) is reported as such, never as an unfinished release: the reads it cuts short fail as
// transport errors.
func enforcePriorReleasesComplete(ctx context.Context, out io.Writer, rc *resolvedCluster, sshPool *ssh.Pool, target string) ([]priorDataMigration, error) {
	if hasUnscopedDatabaseDeployments(rc.Manifest) {
		first := true
		var runs []priorDataMigration
		err := forEachDatabaseDeployment(rc.Manifest, func(view *inventory.Manifest) error {
			if !first {
				view.Infrastructure.ClickHouse = nil
			}
			first = false
			part, err := enforcePriorReleasesComplete(ctx, out, rc.withDatabaseManifest(view), sshPool, target)
			for _, run := range part {
				if !slices.ContainsFunc(runs, func(have priorDataMigration) bool { return have.key() == run.key() }) {
					runs = append(runs, run)
				}
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		return runs, nil
	}

	manifest := rc.Manifest
	var gaps []priorReleaseGap
	fmt.Fprintf(out, "[preflight] checking that every release before %s is complete\n", target)

	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		gap, err := priorPostgresGap(ctx, out, rc, sshPool, pg, target)
		if interrupted := preflightInterrupted(ctx); interrupted != nil {
			return nil, interrupted
		}
		if err != nil {
			return nil, fmt.Errorf("[preflight] check earlier releases (postgres): %w", err)
		}
		if gap != nil {
			gaps = append(gaps, *gap)
		}
	}
	if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled && len(ch.Databases) > 0 {
		gap, err := priorClickHouseGap(ctx, out, rc, sshPool, ch, target)
		if interrupted := preflightInterrupted(ctx); interrupted != nil {
			return nil, interrupted
		}
		if err != nil {
			return nil, fmt.Errorf("[preflight] check earlier releases (clickhouse): %w", err)
		}
		if gap != nil {
			gaps = append(gaps, *gap)
		}
	}
	dataGaps, runs, err := priorDataMigrationGaps(ctx, out, sshPool, manifest, target)
	if interrupted := preflightInterrupted(ctx); interrupted != nil {
		return nil, interrupted
	}
	if err != nil {
		return nil, fmt.Errorf("[preflight] check earlier releases (data migrations): %w", err)
	}
	gaps = append(gaps, dataGaps...)

	if len(gaps) > 0 {
		return nil, priorReleaseRefusal(target, gaps, runs)
	}
	if len(runs) > 0 {
		fmt.Fprintf(out, "[preflight] every release before %s is complete except %d required data migration(s) release apply runs first: %s\n", target, len(runs), priorDataMigrationList(runs))
		return runs, nil
	}
	fmt.Fprintf(out, "[preflight] every release before %s is complete (postdeploy migrations and required data migrations).\n", target)
	return nil, nil
}

func priorDataMigrationList(runs []priorDataMigration) string {
	names := make([]string, 0, len(runs))
	for _, run := range runs {
		names = append(names, fmt.Sprintf("%s (%s, %s)", run.key(), run.release, run.status))
	}
	return strings.Join(names, ", ")
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
	return &priorReleaseGap{release: release, detail: "missing postdeploy migrations: " + migrationKeyList(missing), needsRelease: true}, nil
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
	return &priorReleaseGap{release: release, detail: "missing clickhouse postdeploy migrations: " + migrationKeyList(missing), needsRelease: true}, nil
}

// priorDataMigrationGaps reads the state of every earlier release's required data migration. A pending or failed one
// its binary registers is returned to run; every other unfinished state is a gap.
func priorDataMigrationGaps(ctx context.Context, out io.Writer, sshPool *ssh.Pool, manifest *inventory.Manifest, target string) ([]priorReleaseGap, []priorDataMigration, error) {
	catalog, err := loadReleaseCatalog()
	if err != nil {
		return nil, nil, fmt.Errorf("embedded release catalog failed to load: %w", err)
	}
	reqs := preflight.CatalogRequirements(catalog, target)
	if len(reqs) == 0 {
		return nil, nil, nil
	}
	src := progressDataMigrationSource(out, manifestHostFor(manifest), priorDataMigrationSourceFn(sshPool, manifest))
	blockers, err := datamigrate.PreDeployBlockers(ctx, src, reqs, target, releases.CompareSemver, releases.BaseVersion)
	if err != nil {
		return nil, nil, err
	}
	var gaps []priorReleaseGap
	var runs []priorDataMigration
	for _, blocker := range blockers {
		r := blocker.Requirement
		release := releases.BaseVersion(r.IntroducedIn)
		live := blocker.Live
		switch {
		case blocker.Unreadable():
			gaps = append(gaps, priorReleaseGap{
				release:    release,
				detail:     fmt.Sprintf("required data migration %s/%s: %s", r.Service, r.ID, blocker.Reason),
				unreadable: true,
			})
		case live.NotAdopted || live.NotRegistered:
			gaps = append(gaps, priorReleaseGap{
				release:      release,
				detail:       fmt.Sprintf("required data migration %s/%s cannot run on the installed %s binary: %s", r.Service, r.ID, r.Service, blocker.Reason),
				needsRelease: true,
			})
		case live.Status == datamigrate.StatusPending || live.Status == datamigrate.StatusFailed:
			runs = append(runs, priorDataMigration{release: release, service: r.Service, id: r.ID, status: live.Status})
		case live.Status == datamigrate.StatusPaused:
			gaps = append(gaps, priorReleaseGap{
				release: release,
				detail:  fmt.Sprintf("required data migration %s/%s is paused (resume it with: frameworks cluster data-migrate resume %s.%s)", r.Service, r.ID, r.Service, r.ID),
			})
		default:
			gaps = append(gaps, priorReleaseGap{
				release: release,
				detail:  fmt.Sprintf("required data migration %s/%s is %s (wait for it to finish; check: frameworks cluster data-migrate status %s.%s)", r.Service, r.ID, blocker.Reason, r.Service, r.ID),
			})
		}
	}
	return gaps, runs, nil
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

// priorReleaseRefusal lists everything unfinished so one refusal covers every engine. When a gap needs an earlier
// release applied, it names the lowest such release, because it is the one to apply next; a running or paused data
// migration names its own remedy. Gaps whose state could not be read are listed separately: they refuse without
// showing any release unfinished, so they never name a release to apply. runs are the pending data migrations
// `release apply` would run itself once the gaps are closed.
func priorReleaseRefusal(target string, gaps []priorReleaseGap, runs []priorDataMigration) error {
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
		if i := slices.IndexFunc(unfinished, func(gap priorReleaseGap) bool { return gap.needsRelease }); i >= 0 {
			release := unfinished[i].release
			fmt.Fprintf(&b, "Apply %s first (`frameworks cluster release apply --version %s`), including its postdeploy migrations, then retry %s. Skipping an unfinished release is not supported.", release, release, target)
		} else {
			fmt.Fprintf(&b, "Resolve the data migrations above, then retry %s.", target)
		}
		if len(unreadable) > 0 {
			b.WriteString("\nThe state of these required data migrations could not be read either:\n")
			writeUnreadableGaps(&b, unreadable)
		}
		writeDeferredRuns(&b, runs)
		return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
	}
	fmt.Fprintf(&b, "[preflight] refusing to apply %s: the state of %d required data migration(s) could not be read, so the earlier releases cannot be verified complete. Nothing was changed.\n", target, len(unreadable))
	writeUnreadableGaps(&b, unreadable)
	fmt.Fprintf(&b, "Resolve the read error above (host reachability, service install), then retry %s.", target)
	writeDeferredRuns(&b, runs)
	return fmt.Errorf("%s", b.String())
}

func writeDeferredRuns(b *strings.Builder, runs []priorDataMigration) {
	if len(runs) > 0 {
		fmt.Fprintf(b, "\nOnce these are resolved, release apply runs these required data migrations itself: %s", priorDataMigrationList(runs))
	}
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

// requirePriorDataMigrationBackups applies the backup gate of every earlier-release data migration `release apply`
// will run, before it changes anything: an irreversible one needs --backup on `release apply`, exactly as on
// `data-migrate run`. The gate also runs under --dry-run, so the dry run refuses where the real run would.
func requirePriorDataMigrationBackups(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, runs []priorDataMigration) error {
	for _, run := range runs {
		if err := requireDataMigrationBackup(ctx, cmd, rc, sshPool, run.service, run.id, false); err != nil {
			return fmt.Errorf("[preflight] release apply runs the required data migration %s of release %s first, and its backup gate refused; pass --backup to release apply. Nothing was changed: %w", run.key(), run.release, err)
		}
	}
	return nil
}

// runPriorDataMigrations runs each earlier-release data migration through its installed service binary, in catalog
// order, and confirms its state reads completed afterwards. The first failure stops the release and names the
// migration. Under --dry-run each runs in a read-only transaction and is recorded so the later dry-run gates treat it
// as completed.
func runPriorDataMigrations(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, runs []priorDataMigration, dryRun bool) error {
	out := cmd.OutOrStdout()
	src := priorDataMigrationSourceFn(sshPool, rc.Manifest)
	for _, run := range runs {
		args := []string{"data-migrations", "run", run.id, "--batch-size", strconv.Itoa(defaultDataMigrationBatchSize)}
		if dryRun {
			args = append(args, "--dry-run")
		}
		fmt.Fprintf(out, "  %s (release %s, %s)\n", run.key(), run.release, run.status)
		if err := runDataMigrateRemoteFn(cmd, rc, run.service, args); err != nil {
			return fmt.Errorf("required data migration %s of release %s failed: %w; nothing of this release was applied after it. Inspect it with `frameworks cluster data-migrate status %s`, fix the cause, then rerun release apply", run.key(), run.release, err, run.key())
		}
		if dryRun {
			if rc.dryRunDataMigrations == nil {
				rc.dryRunDataMigrations = map[string]struct{}{}
			}
			rc.dryRunDataMigrations[run.key()] = struct{}{}
			continue
		}
		live := src(ctx, run.service, run.id)
		if live.FetchError != nil {
			return fmt.Errorf("required data migration %s of release %s ran, but its state could not be read afterwards: %w", run.key(), run.release, live.FetchError)
		}
		if live.Status != datamigrate.StatusCompleted {
			return fmt.Errorf("required data migration %s of release %s ran, but its state is %q, not completed; inspect it with `frameworks cluster data-migrate status %s`", run.key(), run.release, live.Status, run.key())
		}
	}
	return nil
}

// withoutDryRunDataMigrations drops the blockers a dry run of `release apply` already ran read-only: the real apply
// runs them before any later gate reads their state.
func withoutDryRunDataMigrations(blockers []datamigrate.Blocker, rc *resolvedCluster) []datamigrate.Blocker {
	if rc == nil || len(rc.dryRunDataMigrations) == 0 {
		return blockers
	}
	ran := rc.dryRunDataMigrations
	kept := blockers[:0:0]
	for _, blocker := range blockers {
		if _, ok := ran[blocker.Requirement.Service+"."+blocker.Requirement.ID]; !ok || blocker.Unreadable() {
			kept = append(kept, blocker)
		}
	}
	return kept
}
