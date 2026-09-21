package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	fwv "github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/spf13/cobra"
)

// clickhouseBackupDatabase is the ClickHouse database that holds the authoritative billing facts.
const clickhouseBackupDatabase = "periscope"

// Backup seams. Production opens SSH runners on the manifest hosts and reads the clock; tests substitute local
// runners and a fixed time.
var (
	backupRunnerFn = func(host inventory.Host, pool *ssh.Pool) (ssh.StreamRunner, error) {
		return getStreamRunner(host, pool)
	}
	backupNowFn = time.Now
)

// backupSelection is what a backup, restore, finish or rollback covers.
type backupSelection struct {
	all        bool
	databases  []string
	clickhouse bool
}

func (s backupSelection) validate() error {
	if !s.all && len(s.databases) == 0 && !s.clickhouse {
		return errors.New("choose what to cover: --all, or --database <name> (repeatable) and/or --clickhouse")
	}
	if s.all && (len(s.databases) > 0 || s.clickhouse) {
		return errors.New("--all already covers every database; drop --database/--clickhouse")
	}
	return nil
}

// includes reports whether a PostgreSQL/YugabyteDB database is selected. A --database value matches the physical
// name, or <instance>/<name> for a named instance.
func (s backupSelection) includes(instance, name string) bool {
	if s.all {
		return true
	}
	for _, want := range s.databases {
		if selectorMatches(want, instance, name) {
			return true
		}
	}
	return false
}

func selectorMatches(want, instance, name string) bool {
	if instance == "" {
		return want == name || want == backup.DatabaseKey("", name)
	}
	return want == instance+"/"+name
}

func addSelectionFlags(cmd *cobra.Command, sel *backupSelection) {
	cmd.Flags().BoolVar(&sel.all, "all", false, "Cover every PostgreSQL/YugabyteDB database and the ClickHouse billing facts")
	cmd.Flags().StringArrayVar(&sel.databases, "database", nil, "Database to cover (physical name, or <instance>/<name> for a named postgres instance); repeatable")
	cmd.Flags().BoolVar(&sel.clickhouse, "clickhouse", false, "Cover the ClickHouse billing facts")
}

func newClusterBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up the cluster's authoritative databases",
		Long: `Back up the authoritative data of a cluster: every PostgreSQL/YugabyteDB service database (one logical
dump per database, in pre-data, data and post-data sections), the databases of named postgres instances, and the
ClickHouse billing facts. The CLI runs the dump tools on the database hosts over SSH and streams the output to the
destination you name: a local directory or an s3:// URL. A manifest with file hashes, row counts and the migration
ledger digest of every database is written last; a backup without it is incomplete.

Object storage, Kafka, Valkey and SOPS-managed secrets are not part of a backup. Derived ClickHouse tables (rollups,
views, raw telemetry) are rebuilt from the facts and Kafka.

Contract migrations and irreversible data migrations require a backup taken less than an hour earlier whose ledger
digests still match the cluster (--backup on those commands). Nothing else requires one.`,
	}
	cmd.AddCommand(newClusterBackupCreateCmd(), newClusterBackupVerifyCmd())
	return cmd
}

func newClusterBackupCreateCmd() *cobra.Command {
	var to string
	var sel backupSelection
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Dump databases to a local directory or an S3 prefix",
		Example: `  frameworks cluster backup create --to /var/backups/frameworks --all
  frameworks cluster backup create --to s3://ops-backups/frameworks --database purser
  AWS_ENDPOINT_URL_S3=https://fsn1.your-objectstorage.com frameworks cluster backup create --to s3://bucket/prod --all`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sel.validate(); err != nil {
				return err
			}
			if strings.TrimSpace(to) == "" {
				return errors.New("--to is required: a local directory or an s3://bucket/prefix URL")
			}
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if platformErr := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); platformErr != nil {
				return platformErr
			}
			_, err = runBackupCreate(cmd, rc, to, sel)
			return err
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "Destination: a local directory or s3://bucket/prefix (required); the backup goes into a new frameworks-backup-<UTC time> directory under it")
	addSelectionFlags(cmd, &sel)
	return cmd
}

func newClusterBackupVerifyCmd() *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check a backup's manifest and the size and SHA-256 of every file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			loc, err := backup.ParseLocation(from)
			if err != nil {
				return err
			}
			m, err := backup.ReadManifest(cmd.Context(), loc)
			if err != nil {
				return err
			}
			if err := backup.VerifyFiles(cmd.Context(), loc, m.Files(nil)); err != nil {
				return err
			}
			printBackupSummary(cmd, loc, m)
			ux.Success(cmd.OutOrStdout(), "Backup verified")
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Backup directory or s3:// URL (required)")
	_ = cmd.MarkFlagRequired("from") //nolint:errcheck // flag defined above
	return cmd
}

// backupPlanDatabase is one PostgreSQL/YugabyteDB database a backup or restore addresses.
type backupPlanDatabase struct {
	entry  backup.Database // Instance, Name and Source
	host   inventory.Host
	server provisioner.DatabaseServer
}

// backupPlanClickHouse is the ClickHouse coordinator a backup or restore addresses.
type backupPlanClickHouse struct {
	host   inventory.Host
	server provisioner.ClickHouseServer
	nodes  int
}

type backupPlan struct {
	databases  []backupPlanDatabase
	clickhouse *backupPlanClickHouse
}

// planBackupTargets resolves the selected stores against the manifest: the primary PostgreSQL/YugabyteDB with its
// per-cell database aliases, every named postgres instance, and the ClickHouse coordinator.
func planBackupTargets(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, sel backupSelection) (*backupPlan, error) {
	manifest := rc.Manifest
	plan := &backupPlan{}
	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		databases := schemaDatabasesFromConfigs(pg.Databases)
		engine := backup.EnginePostgres
		if pg.IsYugabyte() {
			databases = yugabyteSchemaDatabases(pg.Databases, manifest)
			engine = backup.EngineYugabyte
		}
		var selected []provisioner.SchemaDatabase
		for _, db := range databases {
			if sel.includes("", db.Name) {
				selected = append(selected, db)
			}
		}
		if len(selected) > 0 {
			host, err := backupPostgresHost(ctx, manifest, pg, pool)
			if err != nil {
				return nil, err
			}
			server := provisioner.DatabaseServer{Engine: engine, Port: pg.EffectivePort()}
			for _, db := range selected {
				plan.databases = append(plan.databases, backupPlanDatabase{
					entry:  backup.Database{Name: db.Name, Source: db.SourceName},
					host:   host,
					server: server,
				})
			}
		}
		for i := range pg.Instances {
			inst := &pg.Instances[i]
			host, ok := manifest.GetHost(inst.Host)
			if !ok {
				return nil, fmt.Errorf("postgres instance %s: host %q is not in the manifest", inst.Name, inst.Host)
			}
			host.Name = firstNonEmpty(host.Name, inst.Host)
			for _, db := range inst.Databases {
				if !sel.includes(inst.Name, db.Name) {
					continue
				}
				plan.databases = append(plan.databases, backupPlanDatabase{
					entry:  backup.Database{Instance: inst.Name, Name: db.Name},
					host:   host,
					server: provisioner.DatabaseServer{Engine: backup.EnginePostgres, Port: postgresInstancePort(inst)},
				})
			}
		}
	}
	if sel.all || sel.clickhouse {
		ch := manifest.Infrastructure.ClickHouse
		switch {
		case ch != nil && ch.Enabled && slices.Contains(ch.Databases, clickhouseBackupDatabase):
			host, ok := manifest.GetHost(ch.CoordinatorHost())
			if !ok {
				return nil, fmt.Errorf("cannot resolve clickhouse coordinator host %q", ch.CoordinatorHost())
			}
			host.Name = firstNonEmpty(host.Name, ch.CoordinatorHost())
			env, err := rc.SharedEnv()
			if err != nil {
				return nil, fmt.Errorf("load manifest env_files: %w", err)
			}
			plan.clickhouse = &backupPlanClickHouse{
				host:   host,
				server: provisioner.ClickHouseServer{Port: ch.EffectivePort(), Password: env["CLICKHOUSE_PASSWORD"], Database: clickhouseBackupDatabase},
				nodes:  len(ch.Nodes),
			}
		case sel.clickhouse:
			return nil, fmt.Errorf("--clickhouse: the manifest has no enabled ClickHouse with the %s database", clickhouseBackupDatabase)
		}
	}
	for _, want := range sel.databases {
		found := false
		for _, db := range plan.databases {
			if selectorMatches(want, db.entry.Instance, db.entry.Name) {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("--database %s is not a database in the manifest", want)
		}
	}
	if len(plan.databases) == 0 && plan.clickhouse == nil {
		return nil, errors.New("the manifest has no database to cover")
	}
	return plan, nil
}

// backupPostgresHostFn selects the host the dump tools run on. Tests substitute a fixed host.
var backupPostgresHostFn = postgresAdminHost

func backupPostgresHost(ctx context.Context, manifest *inventory.Manifest, pg *inventory.PostgresConfig, pool *ssh.Pool) (inventory.Host, error) {
	return backupPostgresHostFn(ctx, manifest, pg, pool)
}

// runBackupCreate dumps the selected stores into a new directory under `to` and writes the manifest last. Any
// failure fails the command and leaves no manifest.
func runBackupCreate(cmd *cobra.Command, rc *resolvedCluster, to string, sel backupSelection) (backup.Location, error) {
	out := cmd.OutOrStdout()
	parent, err := backup.ParseLocation(to)
	if err != nil {
		return nil, err
	}
	sshPool := ssh.NewPool(30*time.Second, stringFlag(cmd, "ssh-key").Value)
	defer sshPool.Close()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := planBackupTargets(ctx, rc, sshPool, sel)
	if err != nil {
		return nil, err
	}

	started := backupNowFn().UTC()
	loc := parent.Child("frameworks-backup-" + started.Format("20060102T150405Z"))
	ux.Heading(out, fmt.Sprintf("Backing up to %s", loc))
	m := &backup.Manifest{FormatVersion: backup.FormatVersion, CreatedAt: started, CLIVersion: fwv.Version, Cluster: rc.Cluster}

	for _, target := range plan.databases {
		runner, runErr := backupRunnerFn(target.host, sshPool)
		if runErr != nil {
			return nil, fmt.Errorf("connect to %s for %s: %w", target.host.Name, target.entry.Key(), runErr)
		}
		fmt.Fprintf(out, "  %s (%s on %s)...\n", target.entry.Key(), target.server.Engine, target.host.Name)
		entry, dumpErr := provisioner.BackupDatabase(ctx, runner, target.server, loc, target.entry)
		if dumpErr != nil {
			return nil, fmt.Errorf("back up %s: %w", target.entry.Key(), dumpErr)
		}
		m.Databases = append(m.Databases, entry)
	}
	if ch := plan.clickhouse; ch != nil {
		runner, runErr := backupRunnerFn(ch.host, sshPool)
		if runErr != nil {
			return nil, fmt.Errorf("connect to clickhouse coordinator %s: %w", ch.host.Name, runErr)
		}
		fmt.Fprintf(out, "  %s (%d billing fact tables on %s)...\n", backup.ClickHouseKey(ch.server.Database), len(provisioner.ClickHouseBackupTables), ch.host.Name)
		entry, dumpErr := backupClickHouse(ctx, runner, ch.server, loc)
		if dumpErr != nil {
			return nil, fmt.Errorf("back up clickhouse: %w", dumpErr)
		}
		m.ClickHouse = entry
	}

	m.CompletedAt = backupNowFn().UTC()
	if err := backup.WriteManifest(ctx, loc, m); err != nil {
		return nil, err
	}
	printBackupSummary(cmd, loc, m)
	ux.Success(out, fmt.Sprintf("Backup complete: %s", loc))
	fmt.Fprintf(out, "Pass it to a contract or irreversible migration within %s: --backup %s\n", backup.MaxAge, loc)
	return loc, nil
}

// backupClickHouse dumps the billing fact tables; the ledger read before and after must agree.
func backupClickHouse(ctx context.Context, runner ssh.StreamRunner, server provisioner.ClickHouseServer, loc backup.Location) (*backup.ClickHouse, error) {
	before, err := provisioner.ReadClickHouseLedger(ctx, runner, server)
	if err != nil {
		return nil, err
	}
	entry := &backup.ClickHouse{Database: server.Database}
	for _, table := range provisioner.ClickHouseBackupTables {
		t, tableErr := provisioner.BackupClickHouseTable(ctx, runner, server, loc, table)
		if tableErr != nil {
			return nil, tableErr
		}
		entry.Tables = append(entry.Tables, t)
	}
	after, err := provisioner.ReadClickHouseLedger(ctx, runner, server)
	if err != nil {
		return nil, err
	}
	if backup.LedgerDigest(before) != backup.LedgerDigest(after) {
		return nil, errors.New("the clickhouse migration ledger changed during the backup; run it again when no migration is running")
	}
	if after == nil {
		after = []backup.LedgerRow{}
	}
	entry.Ledger, entry.LedgerDigest = after, backup.LedgerDigest(after)
	return entry, nil
}

func printBackupSummary(cmd *cobra.Command, loc backup.Location, m *backup.Manifest) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\nBackup %s\n  started %s, completed %s (CLI %s)\n", loc, m.CreatedAt.Format(time.RFC3339), m.CompletedAt.Format(time.RFC3339), m.CLIVersion)
	for _, db := range m.Databases {
		var size int64
		for _, s := range db.Sections {
			size += s.File.Size
		}
		fmt.Fprintf(out, "  %-32s %-9s %d tables, %d ledger rows, %s compressed\n", db.Key(), db.Engine, len(db.RowCounts), len(db.Ledger), humanBytes(size))
	}
	if ch := m.ClickHouse; ch != nil {
		var size, rows int64
		for _, t := range ch.Tables {
			size += t.File.Size
			rows += t.Rows
		}
		fmt.Fprintf(out, "  %-32s %-9s %d tables, %d rows, %d ledger rows, %s compressed\n", ch.Key(), backup.EngineClickHouse, len(ch.Tables), rows, len(ch.Ledger), humanBytes(size))
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// getRunner returns an SSH runner for a host, or a local runner for localhost.
func getRunner(host inventory.Host, pool *ssh.Pool) (ssh.Runner, error) {
	if host.ExternalIP == "" || host.ExternalIP == "localhost" || host.ExternalIP == "127.0.0.1" {
		return ssh.NewLocalRunner(""), nil
	}
	return pool.Get(&ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	})
}

// getStreamRunner is getRunner for commands that stream stdin and stdout.
func getStreamRunner(host inventory.Host, pool *ssh.Pool) (ssh.StreamRunner, error) {
	if host.ExternalIP == "" || host.ExternalIP == "localhost" || host.ExternalIP == "127.0.0.1" {
		return ssh.NewLocalRunner(""), nil
	}
	return pool.Get(&ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	})
}
