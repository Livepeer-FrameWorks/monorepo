package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/exec"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"

	"github.com/spf13/cobra"
)

// backupFlag names the flag contract and irreversible migrations take their backup from.
const backupFlag = "backup"

// Backup gate seams. Production reads ledgers and service registries over SSH; tests substitute scripted live
// state and run the real gate against real backup directories.
var (
	contractGateDigestsFn      = contractGateDigests
	serviceDatabaseDigestsFn   = serviceDatabaseDigests
	remoteDataMigrationEntryFn = remoteDataMigrationEntry
)

// requireContractBackup refuses contract migrations unless --backup names a fresh backup whose ledger digests still
// match every database with a pending contract migration. It changes nothing; with no pending contract migration it
// asks for no backup. It runs for --dry-run too, which fails the way the real run would.
func requireContractBackup(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, target string) error {
	digests, err := contractGateDigestsFn(ctx, rc, sshPool, target)
	if err != nil {
		return fmt.Errorf("[backup gate] read the ledgers of databases with pending contract migrations: %w", err)
	}
	if len(digests) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "[backup gate] no pending contract migration up to %s; no backup required.\n", target)
		return nil
	}
	m, err := backup.RequireGate(ctx, stringFlag(cmd, backupFlag).Value, backup.GateRequest{
		Operation:   fmt.Sprintf("contract migrations up to %s on %s", target, strings.Join(digestKeys(digests), ", ")),
		LiveDigests: digests,
		Cluster:     rc.Cluster,
	}, backupNowFn())
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "[backup gate] backup from %s protects %s.\n", m.CreatedAt.Format(time.RFC3339), strings.Join(digestKeys(digests), ", "))
	return nil
}

func digestKeys(digests map[string]string) []string {
	keys := make([]string, 0, len(digests))
	for key := range digests {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// contractGateDigests returns, for every database with a contract migration pending up to target, its live ledger
// digest keyed like the backup manifest.
func contractGateDigests(ctx context.Context, rc *resolvedCluster, sshPool *ssh.Pool, target string) (map[string]string, error) {
	manifest := rc.Manifest
	digests := map[string]string{}
	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		databases := schemaDatabasesFromConfigs(pg.Databases)
		if pg.IsYugabyte() {
			databases = yugabyteSchemaDatabases(pg.Databases, manifest)
		}
		if len(databases) > 0 {
			host, password, err := postgresLedgerAccess(ctx, rc, pg, sshPool)
			if err != nil {
				return nil, err
			}
			missing, err := provisioner.MissingMigrationsForDatabases(ctx, sshPool, host, pg, password, databases, "contract", target)
			if err != nil {
				return nil, err
			}
			if err := addPostgresDigests(ctx, sshPool, host, pg, password, databasesOf(missing), digests); err != nil {
				return nil, err
			}
		}
	}
	if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled && len(ch.Databases) > 0 {
		host, ok := manifest.GetHost(ch.CoordinatorHost())
		if !ok {
			return nil, fmt.Errorf("cannot resolve clickhouse coordinator host %q", ch.CoordinatorHost())
		}
		host.Name = firstNonEmpty(host.Name, ch.CoordinatorHost())
		env, err := rc.SharedEnv()
		if err != nil {
			return nil, fmt.Errorf("load manifest env_files: %w", err)
		}
		missing, err := provisioner.MissingClickHouseMigrationsForDatabases(ctx, sshPool, host, ch.EffectivePort(), env["CLICKHOUSE_PASSWORD"], ch.Databases, "contract", target)
		if err != nil {
			return nil, err
		}
		for _, db := range databasesOf(missing) {
			entries, readErr := provisioner.ReadClickHouseMigrationLedger(ctx, sshPool, host, ch.EffectivePort(), env["CLICKHOUSE_PASSWORD"], db)
			if readErr != nil {
				return nil, readErr
			}
			digests[backup.ClickHouseKey(db)] = backup.LedgerDigest(ledgerRows(entries))
		}
	}
	return digests, nil
}

func postgresLedgerAccess(ctx context.Context, rc *resolvedCluster, pg *inventory.PostgresConfig, sshPool *ssh.Pool) (inventory.Host, string, error) {
	host, err := postgresAdminHost(ctx, rc.Manifest, pg, sshPool)
	if err != nil {
		return inventory.Host{}, "", err
	}
	var sharedEnv map[string]string
	if pg.IsYugabyte() && pg.Password == "" {
		env, envErr := rc.SharedEnv()
		if envErr != nil {
			return inventory.Host{}, "", fmt.Errorf("load manifest env_files: %w", envErr)
		}
		sharedEnv = env
	}
	password, err := resolveYugabytePassword(pg, sharedEnv)
	return host, password, err
}

func addPostgresDigests(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, password string, names []string, digests map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	ledgers, err := provisioner.ReadMigrationLedger(ctx, sshPool, host, pg, password, names)
	if err != nil {
		return err
	}
	for _, name := range names {
		digests[backup.DatabaseKey("", name)] = backup.LedgerDigest(ledgerRows(ledgers[name]))
	}
	return nil
}

func databasesOf(keys []provisioner.MigrationKey) []string {
	var names []string
	for _, key := range keys {
		if !slices.Contains(names, key.Database) {
			names = append(names, key.Database)
		}
	}
	slices.Sort(names)
	return names
}

func ledgerRows(entries []provisioner.LedgerEntry) []backup.LedgerRow {
	rows := make([]backup.LedgerRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, backup.LedgerRow{Version: e.Version, Phase: e.Phase, Seq: e.Seq, Checksum: e.Checksum})
	}
	return rows
}

// dataMigrationListEntry is one row of `<service> data-migrations list --format json`. Irreversible is a pointer
// so a binary that does not report the field is told apart from one that reports false.
type dataMigrationListEntry struct {
	ID           string `json:"id"`
	Irreversible *bool  `json:"irreversible"`
}

// dataMigrationIrreversible decides whether running a data migration needs a backup. It does when the release that
// declares it disables rollback for its service (rollback_disabled), when the service binary marks it irreversible,
// and when the binary does not report the field or does not list the migration.
func dataMigrationIrreversible(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, service, id string) (bool, string, error) {
	if release, ok := catalogRollbackDisabledRelease(rc.Manifest, service, id); ok {
		return true, fmt.Sprintf("release %s disables rollback for %s", release, service), nil
	}
	entry, found, err := remoteDataMigrationEntryFn(ctx, rc, pool, service, id)
	if err != nil {
		return false, "", err
	}
	switch {
	case !found:
		return true, fmt.Sprintf("%s does not list %s, so it cannot report whether it is reversible", service, id), nil
	case entry.Irreversible == nil:
		return true, fmt.Sprintf("the %s binary does not report whether %s is reversible", service, id), nil
	case *entry.Irreversible:
		return true, fmt.Sprintf("%s marks %s irreversible", service, id), nil
	}
	return false, "", nil
}

// catalogRollbackDisabledRelease returns the catalog release that declares the data migration when that release
// lists the migration's service under rollback_disabled.
func catalogRollbackDisabledRelease(manifest *inventory.Manifest, service, id string) (string, bool) {
	catalog, err := loadReleaseCatalog()
	if err != nil {
		return "", false
	}
	deploy := service
	if svc, ok := manifest.Services[service]; ok {
		if name, nameErr := resolveDeployName(service, svc); nameErr == nil {
			deploy = name
		}
	}
	for _, rel := range catalog {
		for _, req := range rel.RequiredDataMigrations {
			if req.ID == id && slices.Contains(rel.RollbackDisabled, deploy) {
				return rel.Version, true
			}
		}
	}
	return "", false
}

// requireDataMigrationBackup refuses an irreversible data migration unless --backup names a fresh backup whose
// ledger digests still match every database of the service. A dry run executes inside a read-only transaction and
// needs no backup.
func requireDataMigrationBackup(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, pool *ssh.Pool, service, id string, dryRun bool) error {
	if dryRun {
		return nil
	}
	irreversible, reason, err := dataMigrationIrreversible(ctx, rc, pool, service, id)
	if err != nil {
		return fmt.Errorf("[backup gate] decide whether %s.%s is reversible: %w", service, id, err)
	}
	if !irreversible {
		return nil
	}
	digests, err := serviceDatabaseDigestsFn(ctx, rc, pool, service)
	if err != nil {
		return fmt.Errorf("[backup gate] read the ledgers of %s: %w", service, err)
	}
	if len(digests) == 0 {
		return fmt.Errorf("[backup gate] %s.%s is irreversible (%s) but %s has no database in the manifest to protect", service, id, reason, service)
	}
	m, err := backup.RequireGate(ctx, stringFlag(cmd, backupFlag).Value, backup.GateRequest{
		Operation:   fmt.Sprintf("irreversible data migration %s.%s (%s)", service, id, reason),
		LiveDigests: digests,
		Cluster:     rc.Cluster,
	}, backupNowFn())
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "[backup gate] backup from %s protects %s.\n", m.CreatedAt.Format(time.RFC3339), strings.Join(digestKeys(digests), ", "))
	return nil
}

// serviceDatabaseDigests returns the live ledger digest of every physical database the service owns. A per-cell
// alias (foghorn-eu) owns its own cell database; otherwise every physical database of the owned logical database.
func serviceDatabaseDigests(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, service string) (map[string]string, error) {
	manifest := rc.Manifest
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil, nil
	}
	deploy := service
	if svc, ok := manifest.Services[service]; ok {
		name, err := resolveDeployName(service, svc)
		if err != nil {
			return nil, err
		}
		deploy = name
	}
	owned, ok := releases.ServiceDatabaseLookup(deploy)
	if !ok {
		return nil, nil
	}
	databases := schemaDatabasesFromConfigs(pg.Databases)
	if pg.IsYugabyte() {
		databases = yugabyteSchemaDatabases(pg.Databases, manifest)
	}
	alias := strings.ReplaceAll(service, "-", "_")
	var names []string
	for _, db := range databases {
		if db.Name == alias && db.SourceName == owned {
			names = []string{db.Name}
			break
		}
		if db.Name == owned || db.SourceName == owned {
			names = append(names, db.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	host, password, err := postgresLedgerAccess(ctx, rc, pg, pool)
	if err != nil {
		return nil, err
	}
	digests := map[string]string{}
	return digests, addPostgresDigests(ctx, pool, host, pg, password, names, digests)
}

// remoteDataMigrationEntry reads the service binary's registry entry for id.
func remoteDataMigrationEntry(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, service, id string) (dataMigrationListEntry, bool, error) {
	host, ok := manifestHostFor(rc.Manifest)(service)
	if !ok {
		return dataMigrationListEntry{}, false, fmt.Errorf("no host for service %q in manifest", service)
	}
	runtime := manifestRuntimeFor(rc.Manifest)(service)
	adopted, err := remoteDataMigrationsAdopted(ctx, pool, host, runtime)
	if err != nil {
		return dataMigrationListEntry{}, false, err
	}
	if !adopted {
		return dataMigrationListEntry{}, false, fmt.Errorf("%s has not adopted data-migrations (missing %s)", service, datamigrate.AdoptionMarkerPath(runtime))
	}
	state, err := detect.NewDetector(pool, host).Detect(ctx, runtime)
	if err != nil {
		return dataMigrationListEntry{}, false, fmt.Errorf("detect %s: %w", runtime, err)
	}
	shellCmd, err := exec.Command(exec.SpecFromDetection(state.Mode, state.Metadata, runtime), []string{"data-migrations", "list", "--format", "json"})
	if err != nil {
		return dataMigrationListEntry{}, false, err
	}
	result, err := pool.Run(ctx, &ssh.ConnectionConfig{Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second}, shellCmd)
	if err != nil {
		return dataMigrationListEntry{}, false, fmt.Errorf("list %s data migrations: %w", service, err)
	}
	return findDataMigrationEntry(result.Stdout, id)
}

func findDataMigrationEntry(listJSON, id string) (dataMigrationListEntry, bool, error) {
	var entries []dataMigrationListEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(listJSON)), &entries); err != nil {
		return dataMigrationListEntry{}, false, fmt.Errorf("decode data-migrations list: %w", err)
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry, true, nil
		}
	}
	return dataMigrationListEntry{}, false, nil
}
