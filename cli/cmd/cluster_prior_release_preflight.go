package cmd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/backup"
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
	priorPostgresDigestsFn   = addPostgresDigests
	priorClickHouseDigestsFn = addClickHouseDigests
)

// priorReleaseGap is what one engine or the data-migration state reports as unfinished for an earlier release.
// unreadable marks a gap whose state could not be read: it still refuses, but it does not show the release unfinished.
// needsRelease marks a gap only applying the earlier release closes: a required data migration the installed binary
// cannot run.
type priorReleaseGap struct {
	release      string
	detail       string
	unreadable   bool
	needsRelease bool
}

// priorReleaseWork is what `release apply` finishes of earlier releases before it changes anything of its own: their
// pending required data migrations, then their missing postdeploy migrations.
type priorReleaseWork struct {
	dataMigrations []priorDataMigration
	postdeploy     priorPostdeploy
}

func (w priorReleaseWork) empty() bool {
	return len(w.dataMigrations) == 0 && w.postdeploy.empty()
}

// priorPostdeploy is the postdeploy migrations of earlier releases missing from the ledgers, per engine, and the live
// ledger digest of every database they change, keyed like the backup manifest.
type priorPostdeploy struct {
	postgres   []provisioner.MigrationKey
	clickhouse []provisioner.MigrationKey
	digests    map[string]string
}

func (p priorPostdeploy) empty() bool { return len(p.postgres) == 0 && len(p.clickhouse) == 0 }

// releases lists the earlier releases the missing migrations belong to, lowest first: `release apply` migrates through
// each in turn, so every release's postdeploy gate reads only the data migrations up to it.
func (p priorPostdeploy) releases() []string {
	var versions []string
	for _, key := range slices.Concat(p.postgres, p.clickhouse) {
		if !slices.Contains(versions, key.Version) {
			versions = append(versions, key.Version)
		}
	}
	slices.SortFunc(versions, releases.CompareSemver)
	return versions
}

func (p priorPostdeploy) list() string {
	return migrationKeyList(slices.Concat(p.postgres, p.clickhouse))
}

func (p *priorPostdeploy) merge(other priorPostdeploy) error {
	p.postgres = append(p.postgres, other.postgres...)
	p.clickhouse = append(p.clickhouse, other.clickhouse...)
	for key, digest := range other.digests {
		if _, exists := p.digests[key]; exists {
			return fmt.Errorf("ambiguous backup database %s", key)
		}
		if p.digests == nil {
			p.digests = map[string]string{}
		}
		p.digests[key] = digest
	}
	return nil
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
// `release apply` cannot finish itself: a postdeploy migration of an earlier release whose ledger checksum differs from
// the embedded file, a missing postdeploy migration of an earlier release that was never applied (an expand migration
// of it or an earlier release is missing too), or a required data migration of an earlier release that is running, paused, unreadable or unknown
// to its installed binary. It returns what `release apply` finishes itself before this release changes anything: the
// earlier releases' required data migrations that are pending or failed, and their postdeploy migrations missing from a
// PostgreSQL/YugabyteDB or ClickHouse ledger. It runs before the first mutation and under --dry-run. The per-service
// pre-deploy gate repeats the ledger checks for each service it deploys.
//
// Only service databases that already carry a baseline marker or a ledger are read: a database the release creates is
// born from the current baseline and has no earlier release to finish.
//
// An interrupt (ctx ending) is reported as such, never as an unfinished release: the reads it cuts short fail as
// transport errors.
func enforcePriorReleasesComplete(ctx context.Context, out io.Writer, rc *resolvedCluster, sshPool *ssh.Pool, target string) (priorReleaseWork, error) {
	if hasUnscopedDatabaseDeployments(rc.Manifest) {
		first := true
		var work priorReleaseWork
		err := forEachDatabaseDeployment(rc.Manifest, func(view *inventory.Manifest) error {
			if !first {
				view.Infrastructure.ClickHouse = nil
			}
			first = false
			part, err := enforcePriorReleasesComplete(ctx, out, rc.withDatabaseManifest(view), sshPool, target)
			if err != nil {
				return err
			}
			for _, run := range part.dataMigrations {
				if !slices.ContainsFunc(work.dataMigrations, func(have priorDataMigration) bool { return have.key() == run.key() }) {
					work.dataMigrations = append(work.dataMigrations, run)
				}
			}
			return work.postdeploy.merge(part.postdeploy)
		})
		if err != nil {
			return priorReleaseWork{}, err
		}
		return work, nil
	}

	manifest := rc.Manifest
	var gaps []priorReleaseGap
	var work priorReleaseWork
	fmt.Fprintf(out, "[preflight] checking that every release before %s is complete\n", target)

	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		part, engineGaps, err := priorPostgresPostdeploy(ctx, out, rc, sshPool, pg, target)
		if interrupted := preflightInterrupted(ctx); interrupted != nil {
			return priorReleaseWork{}, interrupted
		}
		if err != nil {
			return priorReleaseWork{}, fmt.Errorf("[preflight] check earlier releases (postgres): %w", err)
		}
		gaps = append(gaps, engineGaps...)
		if err = work.postdeploy.merge(part); err != nil {
			return priorReleaseWork{}, fmt.Errorf("[preflight] check earlier releases (postgres): %w", err)
		}
	}
	if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled && len(ch.Databases) > 0 {
		part, engineGaps, err := priorClickHousePostdeploy(ctx, out, rc, sshPool, ch, target)
		if interrupted := preflightInterrupted(ctx); interrupted != nil {
			return priorReleaseWork{}, interrupted
		}
		if err != nil {
			return priorReleaseWork{}, fmt.Errorf("[preflight] check earlier releases (clickhouse): %w", err)
		}
		gaps = append(gaps, engineGaps...)
		if err = work.postdeploy.merge(part); err != nil {
			return priorReleaseWork{}, fmt.Errorf("[preflight] check earlier releases (clickhouse): %w", err)
		}
	}
	dataGaps, runs, err := priorDataMigrationGaps(ctx, out, sshPool, manifest, target)
	if interrupted := preflightInterrupted(ctx); interrupted != nil {
		return priorReleaseWork{}, interrupted
	}
	if err != nil {
		return priorReleaseWork{}, fmt.Errorf("[preflight] check earlier releases (data migrations): %w", err)
	}
	gaps = append(gaps, dataGaps...)
	work.dataMigrations = runs

	if len(gaps) > 0 {
		return priorReleaseWork{}, priorReleaseRefusal(target, gaps, work)
	}
	if work.empty() {
		fmt.Fprintf(out, "[preflight] every release before %s is complete (postdeploy migrations and required data migrations).\n", target)
		return work, nil
	}
	if len(runs) > 0 {
		fmt.Fprintf(out, "[preflight] release apply first runs %d required data migration(s) of earlier releases: %s\n", len(runs), priorDataMigrationList(runs))
	}
	if !work.postdeploy.empty() {
		fmt.Fprintf(out, "[preflight] release apply first applies the missing postdeploy migrations of %s: %s\n", strings.Join(work.postdeploy.releases(), ", "), work.postdeploy.list())
	}
	return work, nil
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

// highestPriorRelease is the last release below target, or "" when there is none. The postdeploy ledger check is
// cumulative, so reading it through this release finds every earlier release's missing postdeploy migration.
func highestPriorRelease(target string) string {
	prior := releases.ReleasesBelow(target)
	if len(prior) == 0 {
		return ""
	}
	return prior[len(prior)-1].Version
}

// splitPriorPostdeploy separates the missing postdeploy migrations `release apply` can apply from those it refuses.
// A ledgered one whose checksum differs refuses because the migrate role never reapplies a ledgered migration. A
// release's postdeploy SQL also refuses while an expand migration of that release or an earlier one is missing: that
// release was never applied, so its postdeploy SQL would run against the schema and binaries of the release before
// it. That gap names the lowest release with a missing expand migration as the one to apply first. Missing expand
// migrations alone are no gap: this release's expand step applies them.
func splitPriorPostdeploy(engine string, missing, missingExpand []provisioner.MigrationKey) (runnable []provisioner.MigrationKey, gaps []priorReleaseGap) {
	var unapplied []provisioner.MigrationKey
	for _, key := range missingExpand {
		if key.MismatchedChecksum == "" {
			unapplied = append(unapplied, key)
		}
	}
	slices.SortStableFunc(unapplied, func(a, b provisioner.MigrationKey) int { return releases.CompareSemver(a.Version, b.Version) })
	var mismatched, blocked []provisioner.MigrationKey
	for _, key := range missing {
		switch {
		case key.MismatchedChecksum != "":
			mismatched = append(mismatched, key)
		case len(unapplied) > 0 && releases.CompareSemver(key.Version, unapplied[0].Version) >= 0:
			blocked = append(blocked, key)
		default:
			runnable = append(runnable, key)
		}
	}
	if len(blocked) > 0 {
		gaps = append(gaps, priorReleaseGap{
			release:      unapplied[0].Version,
			detail:       fmt.Sprintf("%s postdeploy migrations of a release that was never applied: %s; expand migrations missing: %s", engine, migrationKeyList(blocked), migrationKeyList(unapplied)),
			needsRelease: true,
		})
	}
	if len(mismatched) > 0 {
		gaps = append(gaps, priorReleaseGap{
			release: mismatched[0].Version,
			detail:  fmt.Sprintf("%s postdeploy migrations whose ledger checksum differs from this CLI's file (inspect with `frameworks cluster doctor`): %s", engine, migrationKeyList(mismatched)),
		})
	}
	return runnable, gaps
}

func priorPostgresPostdeploy(ctx context.Context, out io.Writer, rc *resolvedCluster, sshPool *ssh.Pool, pg *inventory.PostgresConfig, target string) (priorPostdeploy, []priorReleaseGap, error) {
	highest := highestPriorRelease(target)
	if highest == "" {
		return priorPostdeploy{}, nil, nil
	}
	manifest := rc.Manifest
	candidates, err := manifestServiceDatabases(manifest, pg.Databases)
	if err != nil {
		return priorPostdeploy{}, nil, fmt.Errorf("collect service databases: %w", err)
	}
	if len(candidates) == 0 {
		return priorPostdeploy{}, nil, nil
	}
	fmt.Fprintln(out, "[preflight] selecting the postgres admin host")
	host, err := postgresAdminHost(ctx, manifest, pg, sshPool)
	if err != nil {
		return priorPostdeploy{}, nil, err
	}
	fmt.Fprintf(out, "[preflight] reading service database state on %s (%d database(s))\n", hostLabel(host), len(candidates))
	states, err := readServiceDatabaseStatesFn(ctx, sshPool, host, pg, candidates)
	if err != nil {
		return priorPostdeploy{}, nil, fmt.Errorf("probe service databases: %w", err)
	}
	var ledgered []provisioner.SchemaDatabase
	for _, database := range candidates {
		if states[database.Name].Initialized {
			ledgered = append(ledgered, database)
		}
	}
	if len(ledgered) == 0 {
		return priorPostdeploy{}, nil, nil
	}
	var sharedEnv map[string]string
	if pg.IsYugabyte() && pg.Password == "" {
		env, envErr := rc.SharedEnv()
		if envErr != nil {
			return priorPostdeploy{}, nil, fmt.Errorf("load manifest env_files: %w", envErr)
		}
		sharedEnv = env
	}
	password, err := resolveYugabytePassword(pg, sharedEnv)
	if err != nil {
		return priorPostdeploy{}, nil, err
	}
	fmt.Fprintf(out, "[preflight] reading postgres postdeploy ledger through %s on %s\n", highest, hostLabel(host))
	missing, err := missingPostgresMigrationsFn(ctx, sshPool, host, pg, password, ledgered, "postdeploy", highest)
	if err != nil {
		return priorPostdeploy{}, nil, err
	}
	if len(missing) == 0 {
		return priorPostdeploy{}, nil, nil
	}
	fmt.Fprintf(out, "[preflight] reading postgres expand ledger through %s on %s\n", highest, hostLabel(host))
	missingExpand, err := missingPostgresMigrationsFn(ctx, sshPool, host, pg, password, ledgered, "expand", highest)
	if err != nil {
		return priorPostdeploy{}, nil, err
	}
	runnable, gaps := splitPriorPostdeploy("postgres", missing, missingExpand)
	if len(runnable) == 0 {
		return priorPostdeploy{}, gaps, nil
	}
	digests := map[string]string{}
	if err = priorPostgresDigestsFn(ctx, sshPool, host, pg, password, databasesOf(runnable), digests); err != nil {
		return priorPostdeploy{}, nil, fmt.Errorf("read the ledgers the missing postdeploy migrations change: %w", err)
	}
	return priorPostdeploy{postgres: runnable, digests: digests}, gaps, nil
}

func priorClickHousePostdeploy(ctx context.Context, out io.Writer, rc *resolvedCluster, sshPool *ssh.Pool, ch *inventory.ClickHouseConfig, target string) (priorPostdeploy, []priorReleaseGap, error) {
	highest := highestPriorRelease(target)
	if highest == "" {
		return priorPostdeploy{}, nil, nil
	}
	host, ok := rc.Manifest.GetHost(ch.CoordinatorHost())
	if !ok {
		return priorPostdeploy{}, nil, fmt.Errorf("cannot resolve clickhouse coordinator host %q", ch.CoordinatorHost())
	}
	host.Name = firstNonEmpty(host.Name, ch.CoordinatorHost())
	env, err := rc.SharedEnv()
	if err != nil {
		return priorPostdeploy{}, nil, fmt.Errorf("load manifest env_files: %w", err)
	}
	password := env["CLICKHOUSE_PASSWORD"]
	fmt.Fprintf(out, "[preflight] reading clickhouse postdeploy ledger through %s on %s\n", highest, hostLabel(host))
	missing, err := missingClickHouseMigrationsFn(ctx, sshPool, host, ch.EffectivePort(), password, ch.Databases, "postdeploy", highest)
	if err != nil {
		return priorPostdeploy{}, nil, err
	}
	if len(missing) == 0 {
		return priorPostdeploy{}, nil, nil
	}
	fmt.Fprintf(out, "[preflight] reading clickhouse expand ledger through %s on %s\n", highest, hostLabel(host))
	missingExpand, err := missingClickHouseMigrationsFn(ctx, sshPool, host, ch.EffectivePort(), password, ch.Databases, "expand", highest)
	if err != nil {
		return priorPostdeploy{}, nil, err
	}
	runnable, gaps := splitPriorPostdeploy("clickhouse", missing, missingExpand)
	if len(runnable) == 0 {
		return priorPostdeploy{}, gaps, nil
	}
	digests := map[string]string{}
	if err = priorClickHouseDigestsFn(ctx, sshPool, host, ch.EffectivePort(), password, databasesOf(runnable), digests); err != nil {
		return priorPostdeploy{}, nil, fmt.Errorf("read the ledgers the missing postdeploy migrations change: %w", err)
	}
	return priorPostdeploy{clickhouse: runnable, digests: digests}, gaps, nil
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
// showing any release unfinished, so they never name a release to apply. work is what `release apply` would finish
// itself once the gaps are closed.
func priorReleaseRefusal(target string, gaps []priorReleaseGap, work priorReleaseWork) error {
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
			fmt.Fprintf(&b, "Apply %s first (`frameworks cluster release apply --version %s`), then retry %s. Skipping an unfinished release is not supported.", release, release, target)
		} else {
			fmt.Fprintf(&b, "Resolve the items above, then retry %s.", target)
		}
		if len(unreadable) > 0 {
			b.WriteString("\nThe state of these required data migrations could not be read either:\n")
			writeUnreadableGaps(&b, unreadable)
		}
		writeDeferredWork(&b, work)
		return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
	}
	fmt.Fprintf(&b, "[preflight] refusing to apply %s: the state of %d required data migration(s) could not be read, so the earlier releases cannot be verified complete. Nothing was changed.\n", target, len(unreadable))
	writeUnreadableGaps(&b, unreadable)
	fmt.Fprintf(&b, "Resolve the read error above (host reachability, service install), then retry %s.", target)
	writeDeferredWork(&b, work)
	return fmt.Errorf("%s", b.String())
}

func writeDeferredWork(b *strings.Builder, work priorReleaseWork) {
	if len(work.dataMigrations) > 0 {
		fmt.Fprintf(b, "\nOnce these are resolved, release apply runs these required data migrations itself: %s", priorDataMigrationList(work.dataMigrations))
	}
	if !work.postdeploy.empty() {
		fmt.Fprintf(b, "\nOnce these are resolved, release apply applies these postdeploy migrations of earlier releases itself: %s", work.postdeploy.list())
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

// requirePriorPostdeployBackup refuses to apply earlier releases' missing postdeploy migrations unless --backup names a
// fresh backup whose ledger digests still match every database they change. Like the data-migration gate it runs
// before the first mutation and under --dry-run, so the dry run refuses where the real run would.
func requirePriorPostdeployBackup(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, postdeploy priorPostdeploy) error {
	if postdeploy.empty() {
		return nil
	}
	m, err := backup.RequireGate(ctx, stringFlag(cmd, backupFlag).Value, backup.GateRequest{
		Operation:   fmt.Sprintf("postdeploy migrations of earlier releases (%s) on %s", strings.Join(postdeploy.releases(), ", "), strings.Join(digestKeys(postdeploy.digests), ", ")),
		LiveDigests: postdeploy.digests,
		Cluster:     rc.Cluster,
	}, backupNowFn())
	if err != nil {
		return fmt.Errorf("[preflight] release apply applies the missing postdeploy migrations of %s first, and its backup gate refused; pass --backup to release apply. Nothing was changed: %w", strings.Join(postdeploy.releases(), ", "), err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "[backup gate] backup from %s protects %s.\n", m.CreatedAt.Format(time.RFC3339), strings.Join(digestKeys(postdeploy.digests), ", "))
	return nil
}

// runPriorPostdeploy applies earlier releases' missing postdeploy migrations through each of those releases in turn,
// lowest first: `cluster migrate --phase postdeploy --to-version <release>`. Each step's own gate requires that
// release's data migrations completed, which runPriorDataMigrations did before. Under --dry-run each step runs in
// check mode and its migrations are recorded so this run's pre-deploy gates count them as applied.
func runPriorPostdeploy(cmd *cobra.Command, rc *resolvedCluster, postdeploy priorPostdeploy, dryRun, completeInterruptedBaselines bool) error {
	for _, release := range postdeploy.releases() {
		fmt.Fprintf(cmd.OutOrStdout(), "  postdeploy migrations through %s\n", release)
		if err := releaseRunMigrateFn(cmd, rc, dryRun, "postdeploy", true, release, false, completeInterruptedBaselines); err != nil {
			return fmt.Errorf("postdeploy migrations of release %s failed: %w; nothing of this release was applied after them. Fix the cause, then rerun release apply", release, err)
		}
	}
	if dryRun {
		if rc.dryRunPriorPostdeploy == nil {
			rc.dryRunPriorPostdeploy = map[string]struct{}{}
		}
		recordMigrationKeys(rc.dryRunPriorPostdeploy, dryRunEnginePostgres, postdeploy.postgres)
		recordMigrationKeys(rc.dryRunPriorPostdeploy, dryRunEngineClickHouse, postdeploy.clickhouse)
	}
	return nil
}

func recordMigrationKeys(set map[string]struct{}, engine string, keys []provisioner.MigrationKey) {
	for _, key := range keys {
		set[dryRunMigrationKey(engine, key.Database, key.Version, key.Phase, key.Seq)] = struct{}{}
	}
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
