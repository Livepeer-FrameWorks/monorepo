package provisioner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// DataMigrationLedger is the recorded completion state of data migrations in ONE service database, read from that
// database's _data_migrations table. It is read AFTER database initialization; fresh-vs-preserved is decided upstream
// from `_schema_baseline` provenance, not from this ledger's shape. Exists reports whether the database itself is
// present (false = absent database, e.g. a preserved cluster that does not own this db); an absent _data_migrations
// table on an existing database is an empty (Exists=true) ledger, and populated Statuses map id -> status
// ("completed", "running", ...).
type DataMigrationLedger struct {
	Exists   bool
	Statuses map[string]string
}

type dataMigrationLedgerTarget struct {
	database string
	schema   string
}

// ReadDataMigrationLedgers reads _data_migrations (id, status) per database over SSH, using the SAME production access
// path as ReadMigrationLedger (psql as the postgres OS user over the local socket, or ysqlsh for Yugabyte). It is
// fail-closed: a genuine connection/query error is returned so the caller refuses rather than deploying against an
// unverifiable database. An ABSENT database is reported as Exists=false (not an error) so a fresh install is not
// mistaken for a blocked one; an absent _data_migrations table on an existing database is an empty (Exists=true) ledger.
func ReadDataMigrationLedgers(
	ctx context.Context,
	sshPool *ssh.Pool,
	host inventory.Host,
	pg *inventory.PostgresConfig,
	password string,
	databases []SchemaDatabase,
) (map[string]DataMigrationLedger, error) {
	if pg == nil {
		return nil, fmt.Errorf("read data-migration ledger: nil postgres config")
	}
	targets := dataMigrationLedgerTargets(databases)
	out := make(map[string]DataMigrationLedger, len(targets))
	for _, target := range targets {
		var (
			led DataMigrationLedger
			err error
		)
		if pg.IsYugabyte() {
			led, err = readDataMigrationLedgerYugabyteSSH(ctx, sshPool, host, pg, target.database, target.schema)
		} else {
			led, err = readDataMigrationLedgerSSH(ctx, sshPool, host, target.database, target.schema)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", target.database, err)
		}
		out[target.database] = led
	}
	return out, nil
}

func dataMigrationLedgerTargets(databases []SchemaDatabase) []dataMigrationLedgerTarget {
	out := make([]dataMigrationLedgerTarget, 0, len(databases))
	seen := map[string]struct{}{}
	for _, database := range databases {
		normalized, ok := normalizeSchemaDatabase(database)
		if !ok {
			continue
		}
		if _, ok := seen[normalized.Name]; ok {
			continue
		}
		out = append(out, dataMigrationLedgerTarget{database: normalized.Name, schema: normalized.Schema})
		seen[normalized.Name] = struct{}{}
	}
	return out
}

func dataMigrationLedgerRelation(schemaName string) (string, error) {
	if !simpleDBIdentifier.MatchString(schemaName) {
		return "", fmt.Errorf("invalid schema name %q", schemaName)
	}
	return schemaName + "._data_migrations", nil
}

func readDataMigrationLedgerSSH(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, dbName, schemaName string) (DataMigrationLedger, error) {
	if sshPool == nil {
		return DataMigrationLedger{}, errors.New("ssh pool is nil")
	}
	if host.ExternalIP == "" {
		return DataMigrationLedger{}, errors.New("host has no external IP")
	}
	if !simpleDBIdentifier.MatchString(dbName) {
		return DataMigrationLedger{}, fmt.Errorf("invalid database name %q", dbName)
	}
	relation, err := dataMigrationLedgerRelation(schemaName)
	if err != nil {
		return DataMigrationLedger{}, err
	}

	cfg := &ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	}
	cmd := fmt.Sprintf(`sudo -u postgres psql -tAF '|' -d %s -c "SELECT id, status FROM %s"`, dbName, relation)
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	result, err := sshPool.Run(runCtx, cfg, cmd)
	if err != nil {
		return DataMigrationLedger{}, fmt.Errorf("ssh run psql: %w", err)
	}
	if result.ExitCode != 0 {
		// An absent DATABASE is Exists=false (a preserved cluster that does not own this db). A missing _data_migrations
		// TABLE on an existing database is an empty ledger (Exists=true). Anything else fails closed — since the gate
		// runs after database initialization, an unexpected error is a real problem, not a fresh install.
		if isUndefinedDatabaseOutput(result.Stderr) {
			return DataMigrationLedger{Exists: false}, nil
		}
		if isUndefinedDataMigrationTableOutput(result.Stderr, relation) || isUndefinedDataMigrationTableOutput(result.Stdout, relation) {
			return DataMigrationLedger{Exists: true, Statuses: map[string]string{}}, nil
		}
		return DataMigrationLedger{}, fmt.Errorf("psql exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	if isUndefinedDataMigrationTableOutput(result.Stdout, relation) {
		return DataMigrationLedger{Exists: true, Statuses: map[string]string{}}, nil
	}
	statuses, perr := parseDataMigrationPipeOutput(result.Stdout)
	if perr != nil {
		return DataMigrationLedger{}, perr
	}
	return DataMigrationLedger{Exists: true, Statuses: statuses}, nil
}

func readDataMigrationLedgerYugabyteSSH(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, dbName, schemaName string) (DataMigrationLedger, error) {
	if sshPool == nil {
		return DataMigrationLedger{}, errors.New("ssh pool is nil")
	}
	if host.ExternalIP == "" {
		return DataMigrationLedger{}, errors.New("host has no external IP")
	}
	if !simpleDBIdentifier.MatchString(dbName) {
		return DataMigrationLedger{}, fmt.Errorf("invalid database name %q", dbName)
	}
	relation, err := dataMigrationLedgerRelation(schemaName)
	if err != nil {
		return DataMigrationLedger{}, err
	}

	runner, err := sshPool.Get(&ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		return DataMigrationLedger{}, fmt.Errorf("ssh connect: %w", err)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	exec := &SSHExecutor{Runner: runner, UseYugabyteTools: true}
	conn := ConnParams{
		Port:     pg.EffectivePort(),
		User:     "yugabyte",
		Database: dbName,
	}
	statuses := map[string]string{}
	err = exec.QueryRows(queryCtx, conn, `SELECT id, status FROM `+relation, nil, func(scan func(dest ...any) error) error {
		var id, status string
		if scanErr := scan(&id, &status); scanErr != nil {
			return scanErr
		}
		statuses[id] = status
		return nil
	})
	if err != nil {
		if isUndefinedDatabaseErr(err) {
			return DataMigrationLedger{Exists: false}, nil
		}
		if isUndefinedDataMigrationTableErr(err) {
			return DataMigrationLedger{Exists: true, Statuses: map[string]string{}}, nil
		}
		return DataMigrationLedger{}, err
	}
	return DataMigrationLedger{Exists: true, Statuses: statuses}, nil
}

// parseDataMigrationPipeOutput parses psql -tAF '|' output for (id, status): one row per line, two '|'-separated
// fields. Blank lines are skipped.
func parseDataMigrationPipeOutput(out string) (map[string]string, error) {
	statuses := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 2 {
			return nil, fmt.Errorf("unexpected _data_migrations row %q: want 2 fields, got %d", line, len(fields))
		}
		statuses[strings.TrimSpace(fields[0])] = strings.TrimSpace(fields[1])
	}
	return statuses, nil
}

func isUndefinedDatabaseOutput(s string) bool {
	return strings.Contains(s, "does not exist") && strings.Contains(s, "database ")
}

func isUndefinedDataMigrationTableOutput(s, relation string) bool {
	return strings.Contains(s, `relation "`+relation+`" does not exist`)
}

func isUndefinedDatabaseErr(err error) bool {
	return err != nil && isUndefinedDatabaseOutput(err.Error())
}

func isUndefinedDataMigrationTableErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "does not exist") && strings.Contains(err.Error(), "_data_migrations")
}
