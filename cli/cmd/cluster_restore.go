package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"frameworks/cli/internal/releases"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// serviceControl stops and starts the replicas of one manifest service.
type serviceControl interface {
	Stop(ctx context.Context, serviceID string) error
	Start(ctx context.Context, serviceID string) error
}

// Restore seams. Production drives the service roles over Ansible and reads the baseline floor over SSH; tests
// substitute recorders.
var (
	newRestoreServiceControlFn = func(cmd *cobra.Command, rc *resolvedCluster, pool *ssh.Pool) serviceControl {
		return &roleServiceControl{cmd: cmd, rc: rc, pool: pool}
	}
	restoreFloorGapFn = restoreFloorGap
)

func newClusterRestoreCmd() *cobra.Command {
	var from string
	var yes bool
	var sel backupSelection
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore databases from a backup",
		Long: `Restore PostgreSQL/YugabyteDB databases and the ClickHouse billing facts from a backup made by
'frameworks cluster backup create'.

The CLI verifies the manifest and every file hash, restores each database into a shadow database next to the live
one (<name>__restore), and compares the result with the manifest: the migration ledger digest and the row count of
every table. It refuses a backup below the schema migration floor and a Foghorn cell database from another cell.
Only then does it stop the services that use the restored databases, swap the shadow in (the live database is kept
as <name>__prerestore), start the services again and wait for them to validate.

ClickHouse fact tables are loaded into shadow tables and their rows moved into the live tables partition by
partition, so the live tables and the views that read them keep their identity. The live rows are kept in
<table>__prerestore. Restoring a multi-node ClickHouse is not supported.

After checking the restored data, run 'restore finish' to drop the previous copies, or 'restore rollback' to put
them back. Migrations that ran after the backup are listed; re-apply them with 'cluster migrate'.`,
		Example: `  frameworks cluster restore --from /var/backups/frameworks/frameworks-backup-20260919T101500Z --database purser
  frameworks cluster restore --from s3://ops-backups/frameworks/frameworks-backup-20260919T101500Z --all --yes
  frameworks cluster restore finish --database purser`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sel.validate(); err != nil {
				return err
			}
			if strings.TrimSpace(from) == "" {
				return errors.New("--from is required: the backup directory or s3:// URL")
			}
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			return runRestore(cmd, rc, from, sel, yes)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Backup directory or s3:// URL (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	addSelectionFlags(cmd, &sel)
	cmd.AddCommand(newClusterRestoreFinishCmd(), newClusterRestoreRollbackCmd())
	return cmd
}

func newClusterRestoreFinishCmd() *cobra.Command {
	var sel backupSelection
	cmd := &cobra.Command{
		Use:   "finish",
		Short: "Drop the previous copies a restore kept (<name>__prerestore)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sel.validate(); err != nil {
				return err
			}
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runRestoreFinish(cmd, rc, sel)
		},
	}
	addSelectionFlags(cmd, &sel)
	return cmd
}

func newClusterRestoreRollbackCmd() *cobra.Command {
	var sel backupSelection
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Put back the databases a restore replaced",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sel.validate(); err != nil {
				return err
			}
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runRestoreRollback(cmd, rc, sel)
		},
	}
	addSelectionFlags(cmd, &sel)
	return cmd
}

// restoreTarget pairs a manifest entry with the live server it is restored to.
type restoreTarget struct {
	db     backup.Database
	plan   backupPlanDatabase
	runner ssh.StreamRunner
	// liveLedger is the live database's ledger before the swap, for the report of migrations to re-apply.
	liveLedger []backup.LedgerRow
}

func runRestore(cmd *cobra.Command, rc *resolvedCluster, from string, sel backupSelection, yes bool) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	loc, err := backup.ParseLocation(from)
	if err != nil {
		return err
	}
	m, err := backup.ReadManifest(ctx, loc)
	if err != nil {
		return err
	}
	if m.Cluster != rc.Cluster {
		return fmt.Errorf("backup belongs to cluster %q, not target cluster %q; refusing restore", m.Cluster, rc.Cluster)
	}

	keys := map[string]bool{}
	var dbs []backup.Database
	for _, db := range m.Databases {
		if sel.includes(db.Instance, db.Name) {
			dbs = append(dbs, db)
			keys[db.Key()] = true
		}
	}
	for _, want := range sel.databases {
		if !slices.ContainsFunc(dbs, func(db backup.Database) bool { return selectorMatches(want, db.Instance, db.Name) }) {
			return fmt.Errorf("--database %s is not in the backup at %s", want, loc)
		}
	}
	withClickHouse := (sel.all || sel.clickhouse) && m.ClickHouse != nil
	if sel.clickhouse && m.ClickHouse == nil {
		return fmt.Errorf("the backup at %s has no ClickHouse facts", loc)
	}
	if withClickHouse {
		keys[m.ClickHouse.Key()] = true
	}
	if len(keys) == 0 {
		return fmt.Errorf("the selection matches nothing in the backup at %s", loc)
	}

	ux.Heading(out, fmt.Sprintf("Restoring from %s (taken %s)", loc, m.CreatedAt.Format(time.RFC3339)))
	fmt.Fprintln(out, "Verifying backup files...")
	if verifyErr := backup.VerifyFiles(ctx, loc, m.Files(keys)); verifyErr != nil {
		return verifyErr
	}

	sshPool := ssh.NewPool(30*time.Second, stringFlag(cmd, "ssh-key").Value)
	defer sshPool.Close()
	liveSel := backupSelection{clickhouse: withClickHouse}
	for _, db := range dbs {
		liveSel.databases = append(liveSel.databases, db.Key())
	}
	var plan *backupPlan
	if len(liveSel.databases) > 0 || liveSel.clickhouse {
		plan, err = planRestoreTargets(ctx, rc, sshPool, liveSel)
		if err != nil {
			return err
		}
	}

	targets := make([]*restoreTarget, 0, len(dbs))
	for _, db := range dbs {
		p, ok := plan.database(db.Key())
		if !ok {
			return fmt.Errorf("%s is in the backup but not in the cluster manifest", db.Key())
		}
		if p.server.Engine != db.Engine {
			return fmt.Errorf("%s was backed up from %s but the cluster runs %s", db.Key(), db.Engine, p.server.Engine)
		}
		runner, runErr := backupRunnerFn(p.host, sshPool)
		if runErr != nil {
			return fmt.Errorf("connect to %s for %s: %w", p.host.Name, db.Key(), runErr)
		}
		exists, existsErr := provisioner.ServerDatabaseExists(ctx, runner, p.server, db.Name+provisioner.RestorePreviousSuffix)
		if existsErr != nil {
			return existsErr
		}
		if exists {
			return fmt.Errorf("%s%s exists from an earlier restore; run `frameworks cluster restore finish` or `restore rollback` first", db.Name, provisioner.RestorePreviousSuffix)
		}
		targets = append(targets, &restoreTarget{db: db, plan: p, runner: runner})
	}
	var chRunner ssh.StreamRunner
	if withClickHouse {
		if plan.clickhouse == nil {
			return errors.New("the backup has ClickHouse facts but the cluster manifest has no ClickHouse coordinator")
		}
		if plan.clickhouse.nodes > 1 {
			return fmt.Errorf("restoring ClickHouse is supported on a single-node cluster only; this cluster has %d nodes", plan.clickhouse.nodes)
		}
		chRunner, err = backupRunnerFn(plan.clickhouse.host, sshPool)
		if err != nil {
			return fmt.Errorf("connect to clickhouse coordinator: %w", err)
		}
		for _, table := range m.ClickHouse.Tables {
			if preflightErr := provisioner.PreflightClickHouseRestore(ctx, chRunner, plan.clickhouse.server, table.Name); preflightErr != nil {
				return preflightErr
			}
		}
	}

	services := restoreServices(rc.Manifest, dbs, withClickHouse)
	fmt.Fprintln(out, "\nThis restore will:")
	for _, t := range targets {
		fmt.Fprintf(out, "  - restore %s into %s%s, check it, then swap it in (the live database is kept as %s%s)\n",
			t.db.Key(), t.db.Name, provisioner.RestoreShadowSuffix, t.db.Name, provisioner.RestorePreviousSuffix)
	}
	if withClickHouse {
		fmt.Fprintf(out, "  - replace the rows of %d ClickHouse fact tables (live rows kept in <table>%s); facts written after %s are not in the backup\n",
			len(m.ClickHouse.Tables), provisioner.RestorePreviousSuffix, m.CreatedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "  - stop, then start and validate: %s\n", strings.Join(services, ", "))
	if !yes && !confirmRestore(out) {
		fmt.Fprintln(out, "Cancelled")
		return nil
	}

	for _, t := range targets {
		fmt.Fprintf(out, "Restoring %s into %s%s...\n", t.db.Key(), t.db.Name, provisioner.RestoreShadowSuffix)
		if prepareErr := provisioner.PrepareRestoredDatabase(ctx, t.runner, t.plan.server, loc, t.db); prepareErr != nil {
			return fmt.Errorf("%s: %w (the live database is unchanged)", t.db.Key(), prepareErr)
		}
		shadow := t.db.Name + provisioner.RestoreShadowSuffix
		gap, gapErr := restoreFloorGapFn(ctx, rc, sshPool, t, shadow)
		if gapErr != nil {
			return fmt.Errorf("%s: check the schema migration floor: %w", t.db.Key(), gapErr)
		}
		if len(gap) > 0 {
			return fmt.Errorf("%s: the backup is below the schema migration floor and cannot be migrated forward (missing %s); the live database is unchanged", t.db.Key(), migrationKeyList(gap))
		}
		if identityErr := provisioner.CompareCellStorageIdentity(ctx, t.runner, t.plan.server, t.db.Name, shadow); identityErr != nil {
			return identityErr
		}
		live, ledgerErr := provisioner.ReadDatabaseLedger(ctx, t.runner, t.plan.server, t.db.Name)
		if ledgerErr != nil {
			return ledgerErr
		}
		t.liveLedger = live
		if fenceErr := fenceRestoredAuthority(ctx, t.runner, t.plan.server, t.db, shadow); fenceErr != nil {
			return fenceErr
		}
		ux.Success(out, fmt.Sprintf("%s restored and matches the manifest", shadow))
	}
	var liveCHLedger []backup.LedgerRow
	if withClickHouse {
		for _, table := range m.ClickHouse.Tables {
			fmt.Fprintf(out, "Loading %s into %s...\n", table.Name, provisioner.ClickHouseShadowTable(table.Name))
			if prepareErr := provisioner.PrepareRestoredClickHouseTable(ctx, chRunner, plan.clickhouse.server, loc, table); prepareErr != nil {
				return fmt.Errorf("clickhouse %s: %w (the live table is unchanged)", table.Name, prepareErr)
			}
		}
		liveCHLedger, err = provisioner.ReadClickHouseLedger(ctx, chRunner, plan.clickhouse.server)
		if err != nil {
			return err
		}
	}

	control := newRestoreServiceControlFn(cmd, rc, sshPool)
	swapErr := stopSwapStart(ctx, out, control, services, func() error {
		for _, t := range targets {
			if err := provisioner.SwapRestoredDatabase(ctx, t.runner, t.plan.server, t.db.Name); err != nil {
				return fmt.Errorf("%s: %w", t.db.Key(), err)
			}
			ux.Success(out, fmt.Sprintf("%s swapped in", t.db.Key()))
		}
		if withClickHouse {
			for _, table := range m.ClickHouse.Tables {
				if err := provisioner.SwapRestoredClickHouseTable(ctx, chRunner, plan.clickhouse.server, table.Name); err != nil {
					return fmt.Errorf("clickhouse %s: %w", table.Name, err)
				}
			}
			ux.Success(out, "ClickHouse fact tables replaced")
		}
		return nil
	})
	if swapErr != nil {
		return fmt.Errorf("%w; databases already swapped keep their %s copy, see `frameworks cluster restore rollback`", swapErr, provisioner.RestorePreviousSuffix)
	}

	printRestoreReport(out, m, targets, liveCHLedger, withClickHouse)
	return nil
}

// stopSwapStart stops the services, runs swap, and starts the services again even when swap failed.
func stopSwapStart(ctx context.Context, out io.Writer, control serviceControl, services []string, swap func() error) error {
	var stopped []string
	for _, svc := range services {
		fmt.Fprintf(out, "Stopping %s...\n", svc)
		// A failed stop may already have stopped some replicas of this service.
		stopped = append(stopped, svc)
		if err := control.Stop(ctx, svc); err != nil {
			startErr := startServices(ctx, out, control, stopped)
			return errors.Join(fmt.Errorf("stop %s: %w", svc, err), startErr)
		}
	}
	swapErr := swap()
	startErr := startServices(ctx, out, control, stopped)
	return errors.Join(swapErr, startErr)
}

func startServices(ctx context.Context, out io.Writer, control serviceControl, services []string) error {
	var errs []error
	for _, svc := range services {
		fmt.Fprintf(out, "Starting %s...\n", svc)
		startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartCommandTimeout)
		err := control.Start(startCtx, svc)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("start %s: %w", svc, err))
		}
	}
	return errors.Join(errs...)
}

func printRestoreReport(out io.Writer, m *backup.Manifest, targets []*restoreTarget, liveCHLedger []backup.LedgerRow, withClickHouse bool) {
	fmt.Fprintln(out, "\nRestore complete.")
	for _, t := range targets {
		after, _ := backup.LedgerDifference(t.liveLedger, t.db.Ledger)
		if len(after) == 0 {
			fmt.Fprintf(out, "  %s: no migration ran after the backup.\n", t.db.Key())
			continue
		}
		fmt.Fprintf(out, "  %s: %d migration(s) ran after the backup and are no longer applied:\n", t.db.Key(), len(after))
		for _, row := range after {
			fmt.Fprintf(out, "    - %s/%s/%d\n", row.Version, row.Phase, row.Seq)
		}
	}
	if withClickHouse {
		after, _ := backup.LedgerDifference(liveCHLedger, m.ClickHouse.Ledger)
		fmt.Fprintf(out, "  %s: fact rows written after %s are not in the restored tables; the periscope-ingest consumers resume from their committed Kafka offsets, so those rows are not replayed.\n",
			m.ClickHouse.Key(), m.CreatedAt.Format(time.RFC3339))
		if len(after) > 0 {
			fmt.Fprintf(out, "  %s: the ClickHouse schema is not restored; %d migration(s) newer than the backup remain applied.\n", m.ClickHouse.Key(), len(after))
		}
	}
	ux.PrintNextSteps(out, []ux.NextStep{
		{Cmd: "frameworks cluster migrate --phase expand --dry-run", Why: "Re-apply migrations that ran after the backup, then postdeploy the same way."},
		{Cmd: "frameworks cluster restore finish --all", Why: "Drop the previous copies once the restored data is confirmed."},
		{Cmd: "frameworks cluster restore rollback --all", Why: "Put the previous databases back instead."},
	})
}

func confirmRestore(out io.Writer) bool {
	fmt.Fprintf(os.Stderr, "\nProceed? The services listed above will be stopped. [y/N]: ")
	response, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		fmt.Fprintf(out, "failed to read confirmation: %v\n", err)
		return false
	}
	r := strings.TrimSpace(strings.ToLower(response))
	return r == "y" || r == "yes"
}

// planRestoreTargets resolves live servers for a restore, finish or rollback.
func planRestoreTargets(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, sel backupSelection) (*backupPlan, error) {
	return planBackupTargets(ctx, rc, pool, sel)
}

func (p *backupPlan) database(key string) (backupPlanDatabase, bool) {
	if p == nil {
		return backupPlanDatabase{}, false
	}
	for _, db := range p.databases {
		if db.entry.Key() == key {
			return db, true
		}
	}
	return backupPlanDatabase{}, false
}

// restoreFloorGap checks a restored primary service database against the schema migration floor. Instance
// databases and databases without an embedded baseline have no platform migrations to check.
func restoreFloorGap(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, t *restoreTarget, restored string) ([]provisioner.MigrationKey, error) {
	if t.db.Instance != "" {
		return nil, nil
	}
	pg := rc.Manifest.Infrastructure.Postgres
	source := firstNonEmpty(t.db.Source, t.db.Name)
	withBaseline, err := provisioner.ServiceDatabasesWithBaseline([]provisioner.SchemaDatabase{{Name: restored, SourceName: source, Schema: source}})
	if err != nil || len(withBaseline) == 0 {
		return nil, err
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
	return provisioner.PostgresBelowFloorGap(ctx, pool, t.plan.host, pg, password, withBaseline)
}

// restoreServices lists the manifest services that use the restored stores, in a stable order.
func restoreServices(manifest *inventory.Manifest, dbs []backup.Database, withClickHouse bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	ids := make([]string, 0, len(manifest.Services))
	for id, svc := range manifest.Services {
		if svc.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, db := range dbs {
		if db.Instance != "" {
			for _, group := range []map[string]inventory.ServiceConfig{manifest.Services, manifest.Interfaces, manifest.Observability} {
				if svc, ok := group[db.Name]; ok && svc.Enabled {
					add(db.Name)
				}
			}
			continue
		}
		logical := firstNonEmpty(db.Source, db.Name)
		for _, id := range ids {
			deploy, err := resolveDeployName(id, manifest.Services[id])
			if err != nil {
				continue
			}
			owned, ok := releases.ServiceDatabaseLookup(deploy)
			if !ok || owned != logical {
				continue
			}
			if db.Source != "" && strings.ReplaceAll(id, "-", "_") != db.Name {
				continue
			}
			add(id)
		}
	}
	if withClickHouse {
		for _, id := range ids {
			if deploy, err := resolveDeployName(id, manifest.Services[id]); err == nil && serviceDependsOnClickHouse(deploy) {
				add(id)
			}
		}
	}
	return out
}

func runRestoreFinish(cmd *cobra.Command, rc *resolvedCluster, sel backupSelection) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	sshPool := ssh.NewPool(30*time.Second, stringFlag(cmd, "ssh-key").Value)
	defer sshPool.Close()
	plan, err := planRestoreTargets(ctx, rc, sshPool, sel)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	for _, p := range plan.databases {
		runner, runErr := backupRunnerFn(p.host, sshPool)
		if runErr != nil {
			return runErr
		}
		exists, existsErr := provisioner.ServerDatabaseExists(ctx, runner, p.server, p.entry.Name+provisioner.RestorePreviousSuffix)
		if existsErr != nil {
			return existsErr
		}
		if !exists {
			continue
		}
		if err := provisioner.FinishRestoredDatabase(ctx, runner, p.server, p.entry.Name); err != nil {
			return fmt.Errorf("%s: %w", p.entry.Key(), err)
		}
		ux.Success(out, fmt.Sprintf("%s: dropped %s%s", p.entry.Key(), p.entry.Name, provisioner.RestorePreviousSuffix))
	}
	if ch := plan.clickhouse; ch != nil {
		runner, runErr := backupRunnerFn(ch.host, sshPool)
		if runErr != nil {
			return runErr
		}
		for _, table := range provisioner.ClickHouseBackupTables {
			if err := provisioner.FinishRestoredClickHouseTable(ctx, runner, ch.server, table); err != nil {
				return fmt.Errorf("clickhouse %s: %w", table, err)
			}
		}
		ux.Success(out, "ClickHouse restore tables dropped")
	}
	return nil
}

func runRestoreRollback(cmd *cobra.Command, rc *resolvedCluster, sel backupSelection) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	out := cmd.OutOrStdout()
	sshPool := ssh.NewPool(30*time.Second, stringFlag(cmd, "ssh-key").Value)
	defer sshPool.Close()
	plan, err := planRestoreTargets(ctx, rc, sshPool, sel)
	if err != nil {
		return err
	}
	type rollbackDB struct {
		plan   backupPlanDatabase
		runner ssh.StreamRunner
	}
	var dbs []rollbackDB
	var entries []backup.Database
	for _, p := range plan.databases {
		runner, runErr := backupRunnerFn(p.host, sshPool)
		if runErr != nil {
			return runErr
		}
		exists, existsErr := provisioner.ServerDatabaseExists(ctx, runner, p.server, p.entry.Name+provisioner.RestorePreviousSuffix)
		if existsErr != nil {
			return existsErr
		}
		if exists {
			if fenceErr := fenceRestoredAuthority(ctx, runner, p.server, p.entry, p.entry.Name+provisioner.RestorePreviousSuffix); fenceErr != nil {
				return fenceErr
			}
			dbs = append(dbs, rollbackDB{plan: p, runner: runner})
			entries = append(entries, p.entry)
		}
	}
	var chRunner ssh.StreamRunner
	var chTables []string
	if plan.clickhouse != nil {
		chRunner, err = backupRunnerFn(plan.clickhouse.host, sshPool)
		if err != nil {
			return err
		}
		for _, table := range provisioner.ClickHouseBackupTables {
			exists, existsErr := provisioner.ClickHousePreviousExists(ctx, chRunner, plan.clickhouse.server, table)
			if existsErr != nil {
				return existsErr
			}
			if exists {
				chTables = append(chTables, table)
			}
		}
	}
	if len(dbs) == 0 && len(chTables) == 0 {
		fmt.Fprintln(out, "No restore to roll back.")
		return nil
	}
	services := restoreServices(rc.Manifest, entries, len(chTables) > 0)
	control := newRestoreServiceControlFn(cmd, rc, sshPool)
	return stopSwapStart(ctx, out, control, services, func() error {
		for _, db := range dbs {
			if err := provisioner.RollbackRestoredDatabase(ctx, db.runner, db.plan.server, db.plan.entry.Name); err != nil {
				return fmt.Errorf("%s: %w", db.plan.entry.Key(), err)
			}
			ux.Success(out, fmt.Sprintf("%s: previous database back in place", db.plan.entry.Key()))
		}
		if len(chTables) > 0 {
			for _, table := range chTables {
				if err := provisioner.RollbackRestoredClickHouseTable(ctx, chRunner, plan.clickhouse.server, table); err != nil {
					return fmt.Errorf("clickhouse %s: %w", table, err)
				}
			}
			ux.Success(out, "ClickHouse fact tables back to their previous rows")
		}
		return nil
	})
}

// roleServiceControl stops a service with its role's stop tasks and starts it with the role's restart and
// validate tasks, on every host the manifest places it.
type roleServiceControl struct {
	cmd  *cobra.Command
	rc   *resolvedCluster
	pool *ssh.Pool
}

func (c *roleServiceControl) targets(serviceID string) (string, []inventory.Host, error) {
	manifest := c.rc.Manifest
	for _, group := range []map[string]inventory.ServiceConfig{manifest.Services, manifest.Interfaces, manifest.Observability} {
		svc, ok := group[serviceID]
		if !ok || !svc.Enabled {
			continue
		}
		deploy, err := resolveDeployName(serviceID, svc)
		if err != nil {
			return "", nil, err
		}
		names := append([]string{}, svc.Hosts...)
		if svc.Host != "" {
			names = append([]string{svc.Host}, names...)
		}
		var hosts []inventory.Host
		for _, name := range names {
			host, found := manifest.GetHost(name)
			if !found {
				return "", nil, fmt.Errorf("%s: host %q is not in the manifest", serviceID, name)
			}
			host.Name = firstNonEmpty(host.Name, name)
			hosts = append(hosts, host)
		}
		return deploy, hosts, nil
	}
	return "", nil, fmt.Errorf("service %s is not enabled in the manifest", serviceID)
}

func (c *roleServiceControl) Stop(ctx context.Context, serviceID string) error {
	deploy, hosts, err := c.targets(serviceID)
	if err != nil {
		return err
	}
	prov, err := provisioner.GetProvisioner(deploy, c.pool)
	if err != nil {
		return err
	}
	stopper, ok := prov.(provisioner.Stopper)
	if !ok {
		return fmt.Errorf("provisioner for %s does not support role-based stop", deploy)
	}
	for _, host := range hosts {
		config, cfgErr := buildServiceRoleConfig(c.cmd, c.rc, serviceID, deploy, host)
		if cfgErr != nil {
			return cfgErr
		}
		if err := stopper.Stop(ctx, host, config); err != nil {
			return fmt.Errorf("on %s: %w", host.Name, err)
		}
	}
	return nil
}

func (c *roleServiceControl) Start(ctx context.Context, serviceID string) error {
	deploy, hosts, err := c.targets(serviceID)
	if err != nil {
		return err
	}
	prov, err := provisioner.GetProvisioner(deploy, c.pool)
	if err != nil {
		return err
	}
	restarter, ok := prov.(provisioner.Restarter)
	if !ok {
		return fmt.Errorf("provisioner for %s does not support role-based restart", deploy)
	}
	for _, host := range hosts {
		config, cfgErr := buildServiceRoleConfig(c.cmd, c.rc, serviceID, deploy, host)
		if cfgErr != nil {
			return cfgErr
		}
		if state, detectErr := provisioner.DetectWithConfig(ctx, prov, host, config); detectErr == nil {
			config = probeInstalledRelease(config, state)
		}
		if err := restarter.Restart(ctx, host, config); err != nil {
			return fmt.Errorf("on %s: %w", host.Name, err)
		}
		if err := prov.Validate(ctx, host, config); err != nil {
			return fmt.Errorf("on %s: started but did not validate: %w", host.Name, err)
		}
	}
	return nil
}

// fenceRestoredAuthority fences the inactive copy before it can become live.
// A failed fence leaves the live database unchanged, including during rollback.
func fenceRestoredAuthority(ctx context.Context, runner ssh.StreamRunner, server provisioner.DatabaseServer, entry backup.Database, database string) error {
	if entry.Instance != "" || firstNonEmpty(entry.Source, entry.Name) != "foghorn" {
		return nil
	}
	_, err := provisioner.DatabaseQuery(ctx, runner, server, database, `INSERT INTO foghorn.media_authority_restore_fence (singleton, fenced_at) VALUES (TRUE, clock_timestamp()) ON CONFLICT (singleton) DO UPDATE SET fenced_at = clock_timestamp();`)
	if err != nil {
		return fmt.Errorf("fence restored media authority in %s before database activation: %w; live database is unchanged", database, err)
	}
	return nil
}

// mediaAuthorityRestoreFenceSQL raises the restore fence in a database that has
// one, and does nothing in any other: a dump restores every service database,
// and the Foghorn ones are named per cell.
const mediaAuthorityRestoreFenceSQL = `DO $$
BEGIN
    IF to_regclass('foghorn.media_authority_restore_fence') IS NOT NULL THEN
        INSERT INTO foghorn.media_authority_restore_fence (singleton, fenced_at)
        VALUES (TRUE, NOW())
        ON CONFLICT (singleton) DO UPDATE SET fenced_at = NOW();
    END IF;
END
$$;`

// raiseMediaAuthorityRestoreFenceCommand runs mediaAuthorityRestoreFenceSQL in
// every database the restore brought back.
func raiseMediaAuthorityRestoreFenceCommand() string {
	return fmt.Sprintf(`
set -e
cd /opt/frameworks/postgres
databases=$(docker compose exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -At -c "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate")
test -n "$databases"
for db in $databases; do
  docker compose exec -T postgres psql -U postgres -d "$db" -v ON_ERROR_STOP=1 -c %s
done
`, ssh.ShellQuote(mediaAuthorityRestoreFenceSQL))
}
