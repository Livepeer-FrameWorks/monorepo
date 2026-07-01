package provisioner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// Minimum-upgrade-version guard.
//
// Migrations strictly below schemaMigrationBaselineFloor are folded into the
// baseline schema and no longer offered. A fresh cluster gets their effect from
// the baseline, but an EXISTING cluster never re-applies the baseline — so if it
// has not already applied every below-floor migration, upgrading past the floor
// would silently skip them. These functions detect that gap so the caller can
// refuse the upgrade with a stepping-stone message ("upgrade to the last
// pre-floor release first") rather than strand the cluster.
//
// Fresh vs stale is decided by a DURABLE marker, not ledger-shape inference: the
// baseline schema writes a `_schema_baseline` row recording the floor it was born
// at (see docs/standards/schema-migrations.md). A database with a marker has every
// migration below that floor folded into its baseline; a database without one is an
// existing cluster whose ledger must actually contain them.

// PostgresBelowFloorGap returns the below-floor migrations absent from a Postgres
// (or Yugabyte) cluster's _migrations ledger. Empty means the cluster is safe to
// upgrade past the floor. Reuses the production ledger-read path.
func PostgresBelowFloorGap(
	ctx context.Context,
	sshPool *ssh.Pool,
	host inventory.Host,
	pg *inventory.PostgresConfig,
	password string,
	databases []SchemaDatabase,
) ([]MigrationKey, error) {
	all, err := discoverMigrations("migrations")
	if err != nil {
		return nil, fmt.Errorf("discover migrations: %w", err)
	}
	expected := belowFloorItemsFromList(all, databases)
	if len(expected) == 0 {
		return nil, nil
	}
	dbNames := migrationLedgerDatabaseNames(databases)
	ledger, err := ReadMigrationLedger(ctx, sshPool, host, pg, password, dbNames)
	if err != nil {
		return nil, err
	}
	markers := make(map[string]string, len(dbNames))
	for _, db := range dbNames {
		floor, mErr := readPostgresBaselineMarker(ctx, sshPool, host, pg, db)
		if mErr != nil {
			return nil, fmt.Errorf("read baseline marker %s: %w", db, mErr)
		}
		markers[db] = floor
	}
	return belowFloorGap(expected, ledger, markers), nil
}

// belowFloorGap returns the below-floor migrations that a database genuinely still
// needs. For each database:
//   - a baseline marker floor M means everything < M is folded into the baseline the
//     database was born from → those migrations are skipped (a fresh cluster passes);
//   - migrations ≥ M (or all of them, if there is no marker — an existing in-place
//     cluster) must be present in the ledger, else they are reported as a gap.
//
// This is durable: it relies on the persisted `_schema_baseline` marker, not on
// ledger emptiness or the newest-applied version (both of which a dropped ledger or
// a non-monotonic history could fake).
func belowFloorGap(expected []map[string]any, ledger map[string][]LedgerEntry, markers map[string]string) []MigrationKey {
	byDB := make(map[string][]map[string]any)
	for _, item := range expected {
		db := fmt.Sprint(item["db"])
		byDB[db] = append(byDB[db], item)
	}
	var missing []MigrationKey
	for db, items := range byDB {
		markerFloor := markers[db]
		toCheck := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if markerFloor != "" && compareSemver(fmt.Sprint(item["version"]), markerFloor) < 0 {
				continue // folded into the baseline this database was born from
			}
			toCheck = append(toCheck, item)
		}
		if len(toCheck) == 0 {
			continue
		}
		missing = append(missing, diffExpectedAgainstLedger(toCheck, map[string][]LedgerEntry{db: ledger[db]})...)
	}
	return missing
}

// ClickHouseBelowFloorGap is the ClickHouse equivalent: below-floor periscope
// migrations absent from the _migrations ledger on the coordinator node.
func ClickHouseBelowFloorGap(
	ctx context.Context,
	sshPool *ssh.Pool,
	host inventory.Host,
	port int,
	password string,
	dbNames []string,
) ([]MigrationKey, error) {
	knownDBs, err := knownClickHouseDatabases()
	if err != nil {
		return nil, err
	}
	all, err := discoverMigrationsInFS(dbsql.Content, "clickhouse/migrations", knownDBs)
	if err != nil {
		return nil, fmt.Errorf("discover clickhouse migrations: %w", err)
	}
	databases := make([]SchemaDatabase, 0, len(dbNames))
	for _, db := range dbNames {
		databases = append(databases, SchemaDatabase{Name: db})
	}
	expected := belowFloorItemsFromList(all, databases)
	if len(expected) == 0 {
		return nil, nil
	}
	ledger := make(map[string][]LedgerEntry, len(dbNames))
	markers := make(map[string]string, len(dbNames))
	for _, db := range dbNames {
		entries, rErr := ReadClickHouseMigrationLedger(ctx, sshPool, host, port, password, db)
		if rErr != nil {
			return nil, fmt.Errorf("%s: %w", db, rErr)
		}
		ledger[db] = entries
		floor, mErr := readClickHouseBaselineMarker(ctx, sshPool, host, port, password, db)
		if mErr != nil {
			return nil, fmt.Errorf("read baseline marker %s: %w", db, mErr)
		}
		markers[db] = floor
	}
	return belowFloorGap(expected, ledger, markers), nil
}

// ReadClickHouseMigrationLedger reads the _migrations ledger from a ClickHouse
// database via SSH + a local clickhouse-client (the production access path). A
// missing _migrations table is treated as an empty ledger. The password rides
// the CLICKHOUSE_PASSWORD env, never argv.
func ReadClickHouseMigrationLedger(
	ctx context.Context,
	sshPool *ssh.Pool,
	host inventory.Host,
	port int,
	password string,
	dbName string,
) ([]LedgerEntry, error) {
	if sshPool == nil {
		return nil, errors.New("ssh pool is nil")
	}
	if host.ExternalIP == "" {
		return nil, errors.New("host has no external IP")
	}
	if !simpleDBIdentifier.MatchString(dbName) {
		return nil, fmt.Errorf("invalid database name %q", dbName)
	}
	if port == 0 {
		port = 9000
	}

	pwPrefix := ""
	if password != "" {
		pwPrefix = fmt.Sprintf("CLICKHOUSE_PASSWORD=%s ", shellQuote(password))
	}
	// argMax(checksum, applied_at) collapses the ReplacingMergeTree ledger to the
	// latest row per (version, phase, seq); TabSeparated for line/field parsing.
	query := "SELECT version, phase, seq, argMax(checksum, applied_at) FROM _migrations GROUP BY version, phase, seq ORDER BY version, phase, seq FORMAT TabSeparated"
	cmd := fmt.Sprintf("%sclickhouse-client --host 127.0.0.1 --port %d --database %s --query %s",
		pwPrefix, port, shellQuote(dbName), shellQuote(query))

	cfg := &ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	result, err := sshPool.Run(runCtx, cfg, cmd)
	if err != nil {
		return nil, fmt.Errorf("ssh run clickhouse-client: %w", err)
	}
	if result.ExitCode != 0 {
		if isClickHouseUnknownTable(result.Stderr) {
			return nil, nil
		}
		return nil, fmt.Errorf("clickhouse-client exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return parseClickHouseLedgerTSV(result.Stdout)
}

func parseClickHouseLedgerTSV(out string) ([]LedgerEntry, error) {
	var entries []LedgerEntry
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			return nil, fmt.Errorf("unexpected clickhouse ledger row %q: want 4 fields, got %d", line, len(fields))
		}
		seq, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			return nil, fmt.Errorf("clickhouse ledger row %q: invalid seq %q: %w", line, fields[2], err)
		}
		entries = append(entries, LedgerEntry{
			Version:  strings.TrimSpace(fields[0]),
			Phase:    strings.TrimSpace(fields[1]),
			Seq:      seq,
			Checksum: strings.TrimSpace(fields[3]),
		})
	}
	return entries, nil
}

func isClickHouseUnknownTable(stderr string) bool {
	// ClickHouse: "Code: 60. DB::Exception: ... Table … does not exist" / UNKNOWN_TABLE.
	// Scoped to the bookkeeping tables this package reads (_migrations,
	// _schema_baseline) so a genuinely-missing table reads as "no rows" while an
	// unrelated failure (missing database/user, auth) still surfaces as an error.
	if strings.Contains(stderr, "UNKNOWN_TABLE") {
		return true
	}
	return strings.Contains(stderr, "does not exist") &&
		(strings.Contains(stderr, "_migrations") || strings.Contains(stderr, "_schema_baseline"))
}

// baselineMarkerQuery reads the born-at floor from the durable marker table. A
// COALESCE'd scalar subquery always returns exactly one row (empty string when the
// table exists but is empty), so only a MISSING table is an error → treated as "no
// marker" (an existing in-place cluster).
const baselineMarkerQuery = "SELECT COALESCE((SELECT floor FROM public._schema_baseline ORDER BY applied_at DESC LIMIT 1), '')"

// readPostgresBaselineMarker returns the floor a Postgres/Yugabyte database was born
// at, or "" if the marker table is absent (existing in-place cluster).
func readPostgresBaselineMarker(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, dbName string) (string, error) {
	if !simpleDBIdentifier.MatchString(dbName) {
		return "", fmt.Errorf("invalid database name %q", dbName)
	}
	if pg != nil && pg.IsYugabyte() {
		return readBaselineMarkerYugabyteSSH(ctx, sshPool, host, pg, dbName)
	}
	return readBaselineMarkerPsqlSSH(ctx, sshPool, host, dbName)
}

func readBaselineMarkerPsqlSSH(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, dbName string) (string, error) {
	if sshPool == nil {
		return "", errors.New("ssh pool is nil")
	}
	if host.ExternalIP == "" {
		return "", errors.New("host has no external IP")
	}
	cfg := &ssh.ConnectionConfig{Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second}
	cmd := fmt.Sprintf("sudo -u postgres psql -tAc %s -d %s", shellQuote(baselineMarkerQuery), shellQuote(dbName))
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := sshPool.Run(runCtx, cfg, cmd)
	if err != nil {
		return "", fmt.Errorf("ssh run psql: %w", err)
	}
	if result.ExitCode != 0 {
		if strings.Contains(result.Stderr, "does not exist") {
			return "", nil // marker table absent → existing cluster
		}
		return "", fmt.Errorf("psql exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return strings.TrimSpace(result.Stdout), nil
}

func readBaselineMarkerYugabyteSSH(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, dbName string) (string, error) {
	if sshPool == nil {
		return "", errors.New("ssh pool is nil")
	}
	if host.ExternalIP == "" {
		return "", errors.New("host has no external IP")
	}
	runner, err := sshPool.Get(&ssh.ConnectionConfig{Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second})
	if err != nil {
		return "", fmt.Errorf("ssh connect: %w", err)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	exec := &SSHExecutor{Runner: runner, BinaryPath: "/opt/yugabyte/bin/ysqlsh"}
	conn := ConnParams{Port: pg.EffectivePort(), User: "yugabyte", Database: dbName}
	var floor string
	if qErr := exec.QueryRow(queryCtx, conn, baselineMarkerQuery, nil, &floor); qErr != nil {
		if strings.Contains(qErr.Error(), "does not exist") {
			return "", nil // marker table absent → existing cluster
		}
		return "", qErr
	}
	return strings.TrimSpace(floor), nil
}

// readClickHouseBaselineMarker returns the floor a ClickHouse database was born at,
// or "" if the _schema_baseline table is absent (existing in-place cluster).
func readClickHouseBaselineMarker(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, port int, password, dbName string) (string, error) {
	if sshPool == nil {
		return "", errors.New("ssh pool is nil")
	}
	if host.ExternalIP == "" {
		return "", errors.New("host has no external IP")
	}
	if !simpleDBIdentifier.MatchString(dbName) {
		return "", fmt.Errorf("invalid database name %q", dbName)
	}
	if port == 0 {
		port = 9000
	}
	pwPrefix := ""
	if password != "" {
		pwPrefix = fmt.Sprintf("CLICKHOUSE_PASSWORD=%s ", shellQuote(password))
	}
	query := "SELECT floor FROM _schema_baseline ORDER BY applied_at DESC LIMIT 1 FORMAT TabSeparatedRaw"
	cmd := fmt.Sprintf("%sclickhouse-client --host 127.0.0.1 --port %d --database %s --query %s",
		pwPrefix, port, shellQuote(dbName), shellQuote(query))
	cfg := &ssh.ConnectionConfig{Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := sshPool.Run(runCtx, cfg, cmd)
	if err != nil {
		return "", fmt.Errorf("ssh run clickhouse-client: %w", err)
	}
	if result.ExitCode != 0 {
		if isClickHouseUnknownTable(result.Stderr) {
			return "", nil // marker table absent → existing cluster
		}
		return "", fmt.Errorf("clickhouse-client exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return strings.TrimSpace(result.Stdout), nil
}

// FormatBelowFloorRefusal builds the operator-facing stepping-stone message for a
// cluster that is below the baseline floor: it must first be brought up to the
// floor via an older release (one whose floor still OFFERED these migrations),
// then upgraded here. The exact stepping-stone tag is operator knowledge (the last
// release before the floor was raised to this value).
func FormatBelowFloorRefusal(engine string, gap []MigrationKey) string {
	keys := make([]string, 0, len(gap))
	for _, k := range gap {
		keys = append(keys, k.String())
	}
	return fmt.Sprintf(
		"%s cluster is below the schema baseline floor (%s): %d migration(s) folded into the baseline were never applied here, so upgrading would skip them.\n"+
			"Step the cluster up to the floor first — upgrade to a release older than %s (whose floor still offered these migrations), let them apply, then upgrade to this release.\nMissing: %s",
		engine, schemaMigrationBaselineFloor, len(gap), schemaMigrationBaselineFloor, strings.Join(keys, ", "))
}
