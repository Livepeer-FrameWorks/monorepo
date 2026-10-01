package dockerpg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// SharedYugabyteBaselinesEnv names the directory the contract fixture renders service baselines into:
	// <database>.sql holds the baseline with the database's layout applied, exactly as the release applies it to a
	// schema without tables (its indexes built non-concurrently), and
	// <database>.layout holds "colocated" or "distributed", the placement the release creates the database with.
	SharedYugabyteBaselinesEnv = "FRAMEWORKS_YUGABYTE_TEST_BASELINES"

	// SharedYugabyteLayoutEnv set to "distributed" creates every baseline database distributed, the shape a production
	// database keeps until it is relaid out, while it still receives the baseline with the declared layout applied, as
	// migrations reach such a database. Unset, each database is created in its declared layout.
	SharedYugabyteLayoutEnv = "FRAMEWORKS_YUGABYTE_TEST_LAYOUT"
)

// execSharedYugabyteAdmin runs one statement in the suite-owned engine's maintenance database.
func execSharedYugabyteAdmin(ctx context.Context, adminDSN, statement string) error {
	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("open shared Yugabyte admin connection: %w", err)
	}
	defer admin.Close()
	_, err = admin.ExecContext(ctx, statement)
	return err
}

// OpenSharedYugabyteBaseline returns a database on the suite-owned Yugabyte engine holding the rendered baseline of
// database, created in its declared layout or as SharedYugabyteLayoutEnv selects. The boolean is false when the caller should retain its standalone-container
// fallback.
//
// A test process loads each baseline into a database once and hands it to its tests one at a time. Before a test
// receives a database that an earlier test used, the database's catalog version is compared with the one it had right
// after the load, every row is deleted with triggers disabled, and the rows and sequence positions the baseline itself
// left are restored. A database whose catalog changed (DDL, grants, ANALYZE) or whose test failed is never handed out
// again, so every test starts from the catalog and data the baseline produces; it is dropped instead, because every
// tablet it keeps counts against the engine's tablet replica limit (a distributed baseline holds one per table and
// index). The databases a process still holds for reuse when it exits are dropped by run-go-contract-test.sh.
func OpenSharedYugabyteBaseline(t testing.TB, prefix, database string) (*sql.DB, bool) {
	t.Helper()
	baseDSN := strings.TrimSpace(os.Getenv(SharedYugabyteDSNEnv))
	if baseDSN == "" {
		return nil, false
	}
	pool := sharedBaselinePool(database)
	var entry *sharedBaselineDatabase
	for entry == nil {
		entry = pool.take()
		if entry == nil {
			entry = createSharedBaselineDatabase(t, baseDSN, prefix, database)
			break
		}
		// CREATE DATABASE raises the catalog version of every database on the engine, so an idle database can read
		// as changed without any test having changed it; it is then dropped like a changed one.
		if err := entry.reset(baseDSN); err != nil {
			t.Logf("dropping shared Yugabyte %s baseline database %s instead of reusing it: %v", database, entry.name, err)
			if dropErr := dropSharedYugabyteDatabase(baseDSN, entry.name); dropErr != nil {
				t.Errorf("drop shared Yugabyte test database %s: %v", entry.name, dropErr)
			}
			entry = nil
		}
	}
	db, err := sql.Open("postgres", entry.dsn)
	if err != nil {
		t.Fatalf("open shared Yugabyte test database %s: %v", entry.name, err)
	}
	if err := WaitReadyFor(db, strings.TrimSpace(os.Getenv(SharedYugabyteContainerEnv)), 90*time.Second); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close shared Yugabyte test database %s: %v", entry.name, err)
		}
		reusable, err := entry.unchanged(baseDSN)
		if err != nil {
			t.Errorf("inspect shared Yugabyte test database %s: %v", entry.name, err)
		}
		if reusable && !t.Failed() {
			pool.put(entry)
			return
		}
		if err := dropSharedYugabyteDatabase(baseDSN, entry.name); err != nil {
			t.Errorf("drop shared Yugabyte test database %s: %v", entry.name, err)
		}
	})
	return db, true
}

// dropSharedYugabyteDatabase ends every session on a database that will not be handed out again and drops it.
func dropSharedYugabyteDatabase(adminDSN, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("open shared Yugabyte admin connection: %w", err)
	}
	defer admin.Close()
	if err = endSessions(ctx, admin, name); err != nil {
		return err
	}
	_, err = admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+name)
	return err
}

type sharedBaselineDatabasePool struct {
	mu   sync.Mutex
	idle []*sharedBaselineDatabase
}

var (
	sharedBaselinePoolsMu sync.Mutex
	sharedBaselinePools   = map[string]*sharedBaselineDatabasePool{}
)

func sharedBaselinePool(database string) *sharedBaselineDatabasePool {
	sharedBaselinePoolsMu.Lock()
	defer sharedBaselinePoolsMu.Unlock()
	pool := sharedBaselinePools[database]
	if pool == nil {
		pool = &sharedBaselineDatabasePool{}
		sharedBaselinePools[database] = pool
	}
	return pool
}

func (p *sharedBaselineDatabasePool) take() *sharedBaselineDatabase {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.idle) == 0 {
		return nil
	}
	entry := p.idle[len(p.idle)-1]
	p.idle = p.idle[:len(p.idle)-1]
	return entry
}

func (p *sharedBaselineDatabasePool) put(entry *sharedBaselineDatabase) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idle = append(p.idle, entry)
}

// sharedBaselineDatabase is one loaded baseline database and what its baseline left in it.
type sharedBaselineDatabase struct {
	name           string
	dsn            string
	catalogVersion int64
	// tables lists every non-partition table; deleting from a partitioned parent empties its partitions.
	tables []string
	// restore reinserts the rows the baseline inserted and sets every sequence back to where the baseline left it.
	restore string
}

// renderedBaseline reads the layout-rewritten baseline and declared placement the fixture rendered for database.
func renderedBaseline(t testing.TB, database string) (string, bool) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv(SharedYugabyteBaselinesEnv))
	if dir == "" {
		t.Fatalf("%s is set without %s; run Yugabyte contracts through their make target so the fixture renders the release baselines", SharedYugabyteDSNEnv, SharedYugabyteBaselinesEnv)
	}
	baseline, err := os.ReadFile(filepath.Join(dir, database+".sql"))
	if err != nil {
		t.Fatalf("read rendered %s baseline: %v", database, err)
	}
	layout, err := os.ReadFile(filepath.Join(dir, database+".layout"))
	if err != nil {
		t.Fatalf("read rendered %s layout: %v", database, err)
	}
	switch override := strings.TrimSpace(os.Getenv(SharedYugabyteLayoutEnv)); override {
	case "":
	case "distributed":
		return string(baseline), false
	default:
		t.Fatalf("%s is %q, want distributed or unset", SharedYugabyteLayoutEnv, override)
	}
	switch placement := strings.TrimSpace(string(layout)); placement {
	case "colocated":
		return string(baseline), true
	case "distributed":
		return string(baseline), false
	default:
		t.Fatalf("rendered %s layout is %q, want colocated or distributed", database, placement)
		return "", false
	}
}

func createSharedBaselineDatabase(t testing.TB, baseDSN, prefix, database string) *sharedBaselineDatabase {
	t.Helper()
	baseline, colocated := renderedBaseline(t, database)
	parsed, err := url.Parse(baseDSN)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		t.Fatalf("parse %s: %v", SharedYugabyteDSNEnv, err)
	}
	name := sharedYugabyteDatabaseName(prefix, os.Getpid(), sharedYugabyteDatabaseSequence.Add(1))
	statement := "CREATE DATABASE " + name
	if colocated {
		statement += " WITH COLOCATION = true"
	}
	createCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if createErr := execSharedYugabyteAdmin(createCtx, baseDSN, statement); createErr != nil {
		t.Fatalf("create shared Yugabyte %s baseline database %s: %v", database, name, createErr)
	}
	parsed.Path = "/" + name
	entry := &sharedBaselineDatabase{name: name, dsn: parsed.String()}
	db, err := sql.Open("postgres", entry.dsn)
	if err != nil {
		t.Fatalf("open shared Yugabyte test database %s: %v", name, err)
	}
	defer db.Close()
	if err := WaitReadyFor(db, strings.TrimSpace(os.Getenv(SharedYugabyteContainerEnv)), 90*time.Second); err != nil {
		t.Fatal(err)
	}
	// ysqlsh sends the baseline one statement at a time. As one multi-statement query the baseline runs in a single
	// implicit transaction that YugabyteDB cannot retry, and the colocated foghorn baseline sent that way failed with
	// 40001 on every attempt on the pinned engine.
	container := strings.TrimSpace(os.Getenv(SharedYugabyteContainerEnv))
	if out, err := runDockerStdin(context.Background(), baseline, "exec", "-i", container, "ysqlsh", "-X", "-h", container, "-U", "yugabyte",
		"-d", name, "-v", "ON_ERROR_STOP=1", "-q"); err != nil {
		t.Fatalf("apply rendered %s baseline to %s: %v\n%s", database, name, err, out)
	}
	if err := entry.capture(db); err != nil {
		t.Fatalf("record %s baseline state in %s: %v", database, name, err)
	}
	return entry
}

const sharedBaselineTablesQuery = `SELECT format('%I.%I', n.nspname, c.relname)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p')
  AND NOT c.relispartition
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT LIKE 'pg_temp%'
ORDER BY 1`

const sharedBaselineColumnsQuery = `SELECT quote_ident(attname)
FROM pg_attribute
WHERE attrelid = $1::regclass AND attnum > 0 AND NOT attisdropped AND attgenerated = ''
ORDER BY attnum`

const sharedBaselineSequencesQuery = `SELECT format('%I.%I', schemaname, sequencename) FROM pg_sequences ORDER BY 1`

const sharedBaselineCatalogVersionQuery = `SELECT v.current_version
FROM pg_yb_catalog_version v
JOIN pg_database d ON d.oid = v.db_oid
WHERE d.datname = $1`

// capture records the catalog version, the tables, and the rows and sequence positions the baseline left. Rows are
// kept in their composite text form, which every column type reads back exactly.
func (d *sharedBaselineDatabase) capture(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := db.QueryRowContext(ctx, sharedBaselineCatalogVersionQuery, d.name).Scan(&d.catalogVersion); err != nil {
		return fmt.Errorf("read catalog version: %w", err)
	}
	tables, err := queryStrings(ctx, db, sharedBaselineTablesQuery)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	d.tables = tables
	var restore strings.Builder
	for _, table := range tables {
		rows, readErr := queryStrings(ctx, db, "SELECT r::text FROM "+table+" AS r")
		if readErr != nil {
			return fmt.Errorf("read %s: %w", table, readErr)
		}
		if len(rows) == 0 {
			continue
		}
		columns, columnsErr := queryStrings(ctx, db, sharedBaselineColumnsQuery, table)
		if columnsErr != nil {
			return fmt.Errorf("list %s columns: %w", table, columnsErr)
		}
		selected := make([]string, len(columns))
		for i, column := range columns {
			selected[i] = "(v.r)." + column
		}
		values := make([]string, len(rows))
		for i, row := range rows {
			values[i] = "(" + quoteLiteral(row) + "::" + table + ")"
		}
		fmt.Fprintf(&restore, "INSERT INTO %s (%s) OVERRIDING SYSTEM VALUE SELECT %s FROM (VALUES %s) AS v(r);\n",
			table, strings.Join(columns, ", "), strings.Join(selected, ", "), strings.Join(values, ", "))
	}
	sequences, err := queryStrings(ctx, db, sharedBaselineSequencesQuery)
	if err != nil {
		return fmt.Errorf("list sequences: %w", err)
	}
	for _, sequence := range sequences {
		var lastValue int64
		var called bool
		if err := db.QueryRowContext(ctx, "SELECT last_value, is_called FROM "+sequence).Scan(&lastValue, &called); err != nil {
			return fmt.Errorf("read sequence %s: %w", sequence, err)
		}
		fmt.Fprintf(&restore, "SELECT setval(%s, %d, %t);\n", quoteLiteral(sequence), lastValue, called)
	}
	d.restore = restore.String()
	return nil
}

// unchanged reports whether the database kept the catalog version its baseline load produced, after ending every
// session a test left on it.
func (d *sharedBaselineDatabase) unchanged(adminDSN string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return false, err
	}
	defer admin.Close()
	if err := endSessions(ctx, admin, d.name); err != nil {
		return false, err
	}
	var version int64
	if err := admin.QueryRowContext(ctx, sharedBaselineCatalogVersionQuery, d.name).Scan(&version); err != nil {
		return false, fmt.Errorf("read catalog version: %w", err)
	}
	return version == d.catalogVersion, nil
}

// reset returns the database to the rows and sequence positions its baseline load left. Triggers and foreign-key
// checks are off for the transaction so ledger guards and constraint order cannot refuse the cleanup.
func (d *sharedBaselineDatabase) reset(adminDSN string) error {
	unchanged, err := d.unchanged(adminDSN)
	if err != nil {
		return err
	}
	if !unchanged {
		return errors.New("its catalog version changed since the baseline load")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, err := sql.Open("postgres", d.dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	var script strings.Builder
	script.WriteString("BEGIN;\nSET LOCAL session_replication_role = replica;\n")
	for _, table := range d.tables {
		script.WriteString("DELETE FROM " + table + ";\n")
	}
	script.WriteString(d.restore)
	script.WriteString("COMMIT;\n")
	if _, err := db.ExecContext(ctx, script.String()); err != nil {
		return err
	}
	return db.Close()
}

// endSessions terminates every session on database except the caller's and waits until none remain.
func endSessions(ctx context.Context, admin *sql.DB, database string) error {
	for {
		var remaining int
		if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, database).Scan(&remaining); err != nil {
			return fmt.Errorf("count sessions on %s: %w", database, err)
		}
		if remaining == 0 {
			return nil
		}
		if _, err := admin.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, database); err != nil {
			return fmt.Errorf("end sessions on %s: %w", database, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sessions on %s did not end: %w", database, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func queryStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// runDockerStdin runs a docker command with stdin and no deadline of its own; setup that runs for as long as the
// engine needs is bounded by the package -timeout.
func runDockerStdin(ctx context.Context, stdin string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdin = strings.NewReader(stdin)
	out, err := command.CombinedOutput()
	return string(out), err
}

// quoteLiteral quotes a string as a standard-conforming SQL literal.
func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
