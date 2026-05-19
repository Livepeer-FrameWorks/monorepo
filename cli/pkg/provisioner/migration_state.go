package provisioner

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	_ "github.com/lib/pq"
)

var simpleDBIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// LedgerEntry is one row from a database's _migrations table.
type LedgerEntry struct {
	Version  string
	Phase    string
	Seq      int
	Checksum string
}

// MigrationKey identifies a single embedded migration not yet applied (or
// applied with a checksum mismatch). Database/Filename are populated for
// human-readable error messages.
type MigrationKey struct {
	Database string
	Version  string
	Phase    string
	Seq      int
	Filename string

	// MismatchedChecksum is set when the ledger row exists but its checksum
	// does not equal the embedded file's checksum.
	MismatchedChecksum string
}

func (k MigrationKey) String() string {
	if k.MismatchedChecksum != "" {
		return fmt.Sprintf("%s/%s/%s/%s (checksum mismatch: ledger=%s)",
			k.Database, k.Version, k.Phase, k.Filename, k.MismatchedChecksum)
	}
	return fmt.Sprintf("%s/%s/%s/%s", k.Database, k.Version, k.Phase, k.Filename)
}

// ReadMigrationLedger returns the _migrations contents per database via the
// production access path:
//   - vanilla pg:  SSH host + `sudo -u postgres psql -d <db> -tAF '|' -c "SELECT ..."`
//     This matches the prod migration role (login_unix_socket,
//     postgres user) — see ansible/.../postgres/tasks/migrate.yml.
//   - yugabyte:    SSH host + local ysqlsh as the built-in yugabyte role.
//     This matches the prod Yugabyte role, whose generated HBA trusts local
//     admin traffic from 127.0.0.1 and does not set a built-in admin password.
//
// A missing _migrations table is treated as an empty ledger (the database
// has not yet been migrated).
func ReadMigrationLedger(
	ctx context.Context,
	sshPool *ssh.Pool,
	host inventory.Host,
	pg *inventory.PostgresConfig,
	password string,
	dbNames []string,
) (map[string][]LedgerEntry, error) {
	if pg == nil {
		return nil, fmt.Errorf("read migration ledger: nil postgres config")
	}
	out := make(map[string][]LedgerEntry, len(dbNames))
	for _, db := range dbNames {
		var (
			entries []LedgerEntry
			err     error
		)
		if pg.IsYugabyte() {
			entries, err = readLedgerYugabyteSSH(ctx, sshPool, host, pg, db)
		} else {
			entries, err = readLedgerSSH(ctx, sshPool, host, db)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", db, err)
		}
		out[db] = entries
	}
	return out, nil
}

// MissingMigrations returns the embedded migrations for one phase up to
// targetVersion that are either absent from the ledger or present with a
// checksum mismatch. targetVersion must be a concrete vX.Y.Z.
func MissingMigrations(
	ctx context.Context,
	sshPool *ssh.Pool,
	host inventory.Host,
	pg *inventory.PostgresConfig,
	password string,
	dbNames []string,
	phase string,
	targetVersion string,
) ([]MigrationKey, error) {
	databases := make([]SchemaDatabase, 0, len(dbNames))
	for _, db := range dbNames {
		databases = append(databases, SchemaDatabase{Name: db})
	}
	return MissingMigrationsForDatabases(ctx, sshPool, host, pg, password, databases, phase, targetVersion)
}

// MissingMigrationsForDatabases is MissingMigrations with full schema database
// mapping support. Use this for physical databases whose embedded SQL source
// comes from a logical database name, such as regional Yugabyte aliases.
func MissingMigrationsForDatabases(
	ctx context.Context,
	sshPool *ssh.Pool,
	host inventory.Host,
	pg *inventory.PostgresConfig,
	password string,
	databases []SchemaDatabase,
	phase string,
	targetVersion string,
) ([]MigrationKey, error) {
	expected, err := BuildMigrationItemsForDatabases(databases, phase, targetVersion)
	if err != nil {
		return nil, err
	}
	if len(expected) == 0 {
		return nil, nil
	}
	ledger, err := ReadMigrationLedger(ctx, sshPool, host, pg, password, migrationLedgerDatabaseNames(databases))
	if err != nil {
		return nil, err
	}
	return diffExpectedAgainstLedger(expected, ledger), nil
}

func migrationLedgerDatabaseNames(databases []SchemaDatabase) []string {
	out := make([]string, 0, len(databases))
	seen := map[string]struct{}{}
	for _, database := range databases {
		name := strings.TrimSpace(database.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		out = append(out, name)
		seen[name] = struct{}{}
	}
	return out
}

// diffExpectedAgainstLedger compares embedded migration items against the
// applied ledger and returns missing/mismatched keys. Pure function, kept
// separate from MissingMigrations for unit-testing without DB/SSH.
func diffExpectedAgainstLedger(expected []map[string]any, ledger map[string][]LedgerEntry) []MigrationKey {
	indexed := make(map[string]map[string]string, len(ledger))
	for db, entries := range ledger {
		m := make(map[string]string, len(entries))
		for _, e := range entries {
			m[ledgerKey(e.Version, e.Phase, e.Seq)] = e.Checksum
		}
		indexed[db] = m
	}

	var missing []MigrationKey
	for _, item := range expected {
		db := fmt.Sprint(item["db"])
		ver := fmt.Sprint(item["version"])
		ph := fmt.Sprint(item["phase"])
		seq, _ := item["sequence"].(int) //nolint:errcheck // upstream BuildMigrationItems always sets int
		checksum := fmt.Sprint(item["checksum"])
		filename := fmt.Sprint(item["filename"])

		applied, ok := indexed[db][ledgerKey(ver, ph, seq)]
		if !ok {
			missing = append(missing, MigrationKey{
				Database: db, Version: ver, Phase: ph, Seq: seq, Filename: filename,
			})
			continue
		}
		if applied != checksum {
			missing = append(missing, MigrationKey{
				Database: db, Version: ver, Phase: ph, Seq: seq, Filename: filename,
				MismatchedChecksum: applied,
			})
		}
	}
	return missing
}

func ledgerKey(version, phase string, seq int) string {
	return version + ":" + phase + ":" + strconv.Itoa(seq)
}

func readLedgerYugabyteSSH(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, dbName string) ([]LedgerEntry, error) {
	if sshPool == nil {
		return nil, errors.New("ssh pool is nil")
	}
	if host.ExternalIP == "" {
		return nil, errors.New("host has no external IP")
	}
	if !simpleDBIdentifier.MatchString(dbName) {
		return nil, fmt.Errorf("invalid database name %q", dbName)
	}

	runner, err := sshPool.Get(&ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh connect: %w", err)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	exec := &SSHExecutor{Runner: runner, BinaryPath: "/opt/yugabyte/bin/ysqlsh"}
	conn := ConnParams{
		Port:     pg.EffectivePort(),
		User:     "yugabyte",
		Database: dbName,
	}
	var out []LedgerEntry
	err = exec.QueryRows(queryCtx, conn, `
		SELECT version, phase, seq, checksum
		FROM _migrations
		ORDER BY version, phase, seq`, nil, func(scan func(dest ...any) error) error {
		var e LedgerEntry
		if err := scan(&e.Version, &e.Phase, &e.Seq, &e.Checksum); err != nil {
			return err
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		if isUndefinedTable(err) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

func readLedgerSSH(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, dbName string) ([]LedgerEntry, error) {
	if sshPool == nil {
		return nil, errors.New("ssh pool is nil")
	}
	if host.ExternalIP == "" {
		return nil, errors.New("host has no external IP")
	}

	cfg := &ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	}

	cmd, err := buildLedgerPsqlCommand(dbName)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	result, err := sshPool.Run(runCtx, cfg, cmd)
	if err != nil {
		return nil, fmt.Errorf("ssh run psql: %w", err)
	}
	if result.ExitCode != 0 {
		if isUndefinedTableOutput(result.Stderr) || isUndefinedTableOutput(result.Stdout) {
			return nil, nil
		}
		return nil, fmt.Errorf("psql exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	if isUndefinedTableOutput(result.Stdout) {
		return nil, nil
	}
	return parseLedgerPipeOutput(result.Stdout)
}

// buildLedgerPsqlCommand returns a remote shell command that runs psql as the
// postgres OS user against the local Unix socket.
func buildLedgerPsqlCommand(dbName string) (string, error) {
	if !simpleDBIdentifier.MatchString(dbName) {
		return "", fmt.Errorf("invalid database name %q", dbName)
	}
	return fmt.Sprintf(
		`sudo -u postgres psql -tAF '|' -d %s -c "SELECT version, phase, seq, checksum FROM _migrations ORDER BY version, phase, seq"`,
		dbName,
	), nil
}

// parseLedgerPipeOutput parses psql -tAF '|' output: one row per line,
// fields separated by '|'. Blank lines are skipped.
func parseLedgerPipeOutput(out string) ([]LedgerEntry, error) {
	var entries []LedgerEntry
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 4 {
			return nil, fmt.Errorf("unexpected ledger row %q: want 4 fields, got %d", line, len(fields))
		}
		seq, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			return nil, fmt.Errorf("ledger row %q: invalid seq %q: %w", line, fields[2], err)
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

func isUndefinedTable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "does not exist") && strings.Contains(msg, "_migrations")
}

func isUndefinedTableOutput(s string) bool {
	return strings.Contains(s, `relation "_migrations" does not exist`)
}
