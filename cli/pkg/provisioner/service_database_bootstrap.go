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

// ServiceDatabaseState is the live state of one service database that decides
// whether a release has to create it before migrations and deploys.
type ServiceDatabaseState struct {
	// Exists reports that the physical database is present.
	Exists bool
	// HasTables reports that the database's service schema contains base
	// tables. The schema role uses the same signal to skip a baseline.
	HasTables bool
	// Initialized reports that the database carries a _schema_baseline marker
	// (created from a baseline) or migration ledger rows (upgraded in place
	// from before the marker existed).
	Initialized bool
}

// HasUnverifiedSchema reports a database whose service schema has tables but
// neither a baseline marker nor migration ledger rows. On YugabyteDB, where DDL
// is not transactional, an interrupted baseline apply leaves this state.
func (s ServiceDatabaseState) HasUnverifiedSchema() bool {
	return s.Exists && s.HasTables && !s.Initialized
}

// PlanServiceDatabaseBootstrap returns the service databases a release must
// initialize: every absent database, every present database whose service
// schema has no base tables (a bootstrap interrupted between creating the
// database and applying its baseline), and every database with tables but no
// baseline marker or ledger rows, which InitializeServiceDatabases completes
// and verifies against a reference built from the same baseline. A database
// without a probed state fails closed.
func PlanServiceDatabaseBootstrap(databases []SchemaDatabase, states map[string]ServiceDatabaseState) ([]SchemaDatabase, error) {
	var pending []SchemaDatabase
	for _, database := range databases {
		state, ok := states[database.Name]
		if !ok {
			return nil, fmt.Errorf("service database %q was not probed; refusing to decide whether it needs its baseline", database.Name)
		}
		if state.Exists && state.HasTables && state.Initialized {
			continue
		}
		pending = append(pending, database)
	}
	return pending, nil
}

// ReadServiceDatabaseStates probes each service database over SSH through the
// same administrative path as the migration ledger reads: psql as the postgres
// OS user on the local socket, or local ysqlsh as the built-in yugabyte role.
// It only runs SELECT statements, so it is safe in dry-run on both engines.
// Databases must already carry their defaults (see ServiceDatabasesWithBaseline).
func ReadServiceDatabaseStates(ctx context.Context, sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, databases []SchemaDatabase) (map[string]ServiceDatabaseState, error) {
	if err := validateSchemaDatabaseIdentifiers(databases); err != nil {
		return nil, err
	}
	reader, err := newServiceDatabaseProbe(sshPool, host, pg)
	if err != nil {
		return nil, err
	}
	return readServiceDatabaseStates(ctx, reader, databases)
}

func readServiceDatabaseStates(ctx context.Context, reader serviceDatabaseProbe, databases []SchemaDatabase) (map[string]ServiceDatabaseState, error) {
	existing, err := reader.databaseNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	states := make(map[string]ServiceDatabaseState, len(databases))
	for _, database := range databases {
		if _, ok := existing[database.Name]; !ok {
			states[database.Name] = ServiceDatabaseState{}
			continue
		}
		hasTables, err := reader.schemaHasTables(ctx, database.Name, database.Schema)
		if err != nil {
			return nil, fmt.Errorf("%s: probe schema %s: %w", database.Name, database.Schema, err)
		}
		initialized, err := probeDatabaseInitialized(ctx, reader, database.Name)
		if err != nil {
			return nil, fmt.Errorf("%s: probe baseline marker and migration ledger: %w", database.Name, err)
		}
		states[database.Name] = ServiceDatabaseState{Exists: true, HasTables: hasTables, Initialized: initialized}
	}
	return states, nil
}

func validateSchemaDatabaseIdentifiers(databases []SchemaDatabase) error {
	for _, database := range databases {
		if !simpleDBIdentifier.MatchString(database.Name) {
			return fmt.Errorf("invalid database name %q", database.Name)
		}
		if !simpleDBIdentifier.MatchString(database.Schema) {
			return fmt.Errorf("database %q: invalid schema name %q", database.Name, database.Schema)
		}
	}
	return nil
}

// newServiceDatabaseProbe returns the administrative SQL path for the engine:
// psql as the postgres OS user on the local socket, or local ysqlsh as the
// built-in yugabyte role.
func newServiceDatabaseProbe(sshPool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig) (serviceDatabaseProbe, error) {
	if pg == nil {
		return nil, errors.New("service database probe: nil postgres config")
	}
	if sshPool == nil {
		return nil, errors.New("service database probe: ssh pool is nil")
	}
	if host.ExternalIP == "" {
		return nil, errors.New("service database probe: host has no external IP")
	}
	if !pg.IsYugabyte() {
		return psqlStateProbe{sshPool: sshPool, host: host}, nil
	}
	runner, err := sshPool.Get(&ssh.ConnectionConfig{Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("ssh connect: %w", err)
	}
	return ysqlStateProbe{executor: &SSHExecutor{Runner: runner, BinaryPath: "/opt/yugabyte/bin/ysqlsh"}, port: pg.EffectivePort()}, nil
}

// probeDatabaseInitialized checks for the baseline marker and the migration
// ledger in two steps: a query naming a table that does not exist fails to
// plan, so presence is read from the catalog before the rows are counted.
func probeDatabaseInitialized(ctx context.Context, reader serviceDatabaseProbe, database string) (bool, error) {
	present, err := reader.scalarText(ctx, database, initializationTablesQuery)
	if err != nil {
		return false, err
	}
	for _, table := range strings.Split(strings.TrimSpace(present), ",") {
		switch table {
		case "_schema_baseline", "_migrations":
		default:
			continue
		}
		hasRows, err := reader.scalarBool(ctx, database, tableHasRowsQuery(table))
		if err != nil {
			return false, err
		}
		if hasRows {
			return true, nil
		}
	}
	return false, nil
}

const (
	listDatabasesQuery = "SELECT datname FROM pg_database WHERE NOT datistemplate"
	probeTimeout       = 15 * time.Second
	// adminStatementTimeout bounds catalog reads and database creation or
	// removal, which take seconds on YugabyteDB.
	adminStatementTimeout = 5 * time.Minute
)

// schemaHasTablesQuery matches the schema role's baseline probe. The schema
// name is validated as a simple identifier before it is embedded.
func schemaHasTablesQuery(schema string) string {
	return fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = '%s' AND table_type = 'BASE TABLE')", schema)
}

// initializationTablesQuery lists which of the baseline marker and migration
// ledger tables exist in the public schema, comma-separated.
const initializationTablesQuery = "SELECT COALESCE(string_agg(table_name, ',' ORDER BY table_name), '') FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('_schema_baseline', '_migrations')"

// tableHasRowsQuery is only built for the two fixed table names above. A
// baseline marker row holding the verification-pending value does not count:
// it marks a database whose completed baseline has not been verified yet.
func tableHasRowsQuery(table string) string {
	if table == "_schema_baseline" {
		return fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM public._schema_baseline WHERE floor <> '%s')", baselineVerificationPendingFloor)
	}
	return fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM public.%s)", table)
}

// serviceDatabaseProbe is the administrative SQL transport for service
// databases. Queries and statements are sent as single statements outside an
// explicit transaction, so CREATE DATABASE and DROP DATABASE are allowed.
type serviceDatabaseProbe interface {
	databaseNames(ctx context.Context) (map[string]struct{}, error)
	schemaHasTables(ctx context.Context, database, schema string) (bool, error)
	scalarText(ctx context.Context, database, query string) (string, error)
	scalarBool(ctx context.Context, database, query string) (bool, error)
	// textRows returns one string per row of a single-column query. Values
	// must not contain newlines or the '|' separator.
	textRows(ctx context.Context, database, query string) ([]string, error)
	exec(ctx context.Context, database, statement string) error
	// maintenanceDatabase is the always-present database administrative
	// statements such as CREATE DATABASE connect to.
	maintenanceDatabase() string
	yugabyte() bool
}

type psqlStateProbe struct {
	sshPool *ssh.Pool
	host    inventory.Host
}

func (p psqlStateProbe) run(ctx context.Context, database, query string) (string, error) {
	return p.runWithin(ctx, probeTimeout, database, query)
}

func (p psqlStateProbe) runWithin(ctx context.Context, timeout time.Duration, database, query string) (string, error) {
	cfg := &ssh.ConnectionConfig{Address: p.host.ExternalIP, Port: 22, User: p.host.User, HostName: p.host.Name, Timeout: 30 * time.Second}
	cmd := fmt.Sprintf("sudo -u postgres psql -X -v ON_ERROR_STOP=1 -tAc %s -d %s", shellQuote(query), shellQuote(database))
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := p.sshPool.Run(runCtx, cfg, cmd)
	if err != nil {
		return "", fmt.Errorf("ssh run psql: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("psql exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return result.Stdout, nil
}

func (p psqlStateProbe) databaseNames(ctx context.Context) (map[string]struct{}, error) {
	out, err := p.run(ctx, "postgres", listDatabasesQuery)
	if err != nil {
		return nil, err
	}
	return parseDatabaseNameLines(out), nil
}

func (p psqlStateProbe) schemaHasTables(ctx context.Context, database, schema string) (bool, error) {
	return p.scalarBool(ctx, database, schemaHasTablesQuery(schema))
}

func (p psqlStateProbe) scalarText(ctx context.Context, database, query string) (string, error) {
	out, err := p.run(ctx, database, query)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (p psqlStateProbe) scalarBool(ctx context.Context, database, query string) (bool, error) {
	out, err := p.run(ctx, database, query)
	if err != nil {
		return false, err
	}
	return parsePsqlBoolScalar(out)
}

func (p psqlStateProbe) textRows(ctx context.Context, database, query string) ([]string, error) {
	out, err := p.runWithin(ctx, adminStatementTimeout, database, query)
	if err != nil {
		return nil, err
	}
	return parseTextRows(out), nil
}

func (p psqlStateProbe) exec(ctx context.Context, database, statement string) error {
	_, err := p.runWithin(ctx, adminStatementTimeout, database, statement)
	return err
}

func (psqlStateProbe) maintenanceDatabase() string { return "postgres" }

func (psqlStateProbe) yugabyte() bool { return false }

type ysqlStateProbe struct {
	executor *SSHExecutor
	port     int
}

func (p ysqlStateProbe) conn(database string) ConnParams {
	return ConnParams{Port: p.port, User: "yugabyte", Database: database}
}

func (p ysqlStateProbe) databaseNames(ctx context.Context) (map[string]struct{}, error) {
	queryCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	names := map[string]struct{}{}
	err := p.executor.QueryRows(queryCtx, p.conn("yugabyte"), listDatabasesQuery, nil, func(scan func(dest ...any) error) error {
		var name string
		if err := scan(&name); err != nil {
			return err
		}
		if name = strings.TrimSpace(name); name != "" {
			names[name] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

func (p ysqlStateProbe) schemaHasTables(ctx context.Context, database, schema string) (bool, error) {
	return p.scalarBool(ctx, database, schemaHasTablesQuery(schema))
}

func (p ysqlStateProbe) scalarText(ctx context.Context, database, query string) (string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var value string
	if err := p.executor.QueryRow(queryCtx, p.conn(database), query, nil, &value); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func (p ysqlStateProbe) scalarBool(ctx context.Context, database, query string) (bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var value bool
	if err := p.executor.QueryRow(queryCtx, p.conn(database), query, nil, &value); err != nil {
		return false, err
	}
	return value, nil
}

func (p ysqlStateProbe) textRows(ctx context.Context, database, query string) ([]string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, adminStatementTimeout)
	defer cancel()
	var rows []string
	err := p.executor.QueryRows(queryCtx, p.conn(database), query, nil, func(scan func(dest ...any) error) error {
		var value string
		if err := scan(&value); err != nil {
			return err
		}
		rows = append(rows, value)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (p ysqlStateProbe) exec(ctx context.Context, database, statement string) error {
	execCtx, cancel := context.WithTimeout(ctx, adminStatementTimeout)
	defer cancel()
	return p.executor.Exec(execCtx, p.conn(database), statement)
}

func (ysqlStateProbe) maintenanceDatabase() string { return "yugabyte" }

func (ysqlStateProbe) yugabyte() bool { return true }

func parseDatabaseNameLines(out string) map[string]struct{} {
	names := map[string]struct{}{}
	for line := range strings.SplitSeq(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func parseTextRows(out string) []string {
	var rows []string
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			rows = append(rows, line)
		}
	}
	return rows
}

func parsePsqlBoolScalar(out string) (bool, error) {
	var value bool
	if err := scanPsqlValue(strings.TrimSpace(out), &value); err != nil {
		return false, err
	}
	return value, nil
}
