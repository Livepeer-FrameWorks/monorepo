//go:build schema_verify

package provisioner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/ssh"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// dockerStreamRunner runs the backup and restore host commands inside a container, the way ssh.Client runs them on
// a database host: `sh -c <command>` with stdin and stdout streamed.
type dockerStreamRunner struct{ container string }

func (d dockerStreamRunner) RunStream(ctx context.Context, command string, stdin io.Reader, stdout io.Writer) (*ssh.CommandResult, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", d.container, "sh", "-c", command)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := &ssh.CommandResult{Command: command, Stderr: strings.TrimSpace(stderr.String())}
	if err != nil {
		result.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		return result, fmt.Errorf("%w: %s", err, result.Stderr)
	}
	return result, nil
}

// installSudoShim gives the PostgreSQL container the `sudo -u <user>` the production commands use.
func installSudoShim(t *testing.T, container string) {
	t.Helper()
	shim := "#!/bin/sh\nif [ \"$1\" = \"-u\" ]; then u=\"$2\"; shift 2; exec gosu \"$u\" \"$@\"; fi\nexec \"$@\"\n"
	if out, err := docker(t, shim, "exec", "-i", container, "sh", "-c", "cat > /usr/local/bin/sudo && chmod 0755 /usr/local/bin/sudo"); err != nil {
		t.Fatalf("install sudo shim: %v\n%s", err, out)
	}
}

// backupSeedSQL builds a service database with an owner-owned schema, a foreign key, a sequence, values that need
// COPY escaping, NULLs, and a migration ledger with the role's definition.
const backupSeedSQL = `
CREATE SCHEMA purser AUTHORIZATION purser;
SET ROLE purser;
CREATE TABLE purser.invoices (id BIGSERIAL PRIMARY KEY, tenant_id TEXT NOT NULL, amount NUMERIC(12,2) NOT NULL, note TEXT, legacy_code TEXT);
CREATE TABLE purser.invoice_lines (id BIGSERIAL PRIMARY KEY, invoice_id BIGINT NOT NULL REFERENCES purser.invoices(id), description TEXT NOT NULL);
CREATE INDEX invoices_tenant_idx ON purser.invoices (tenant_id);
INSERT INTO purser.invoices (tenant_id, amount, note, legacy_code) VALUES
  ('t1', 10.50, E'tab\there', 'L1'), ('t1', 20.00, E'line\nbreak', 'L2'), ('t2', 0.01, NULL, 'L3'), ('t2', 99.99, E'back\\slash', NULL);
INSERT INTO purser.invoice_lines (invoice_id, description) SELECT id, 'line for ' || id FROM purser.invoices;
CREATE TABLE public._migrations (
  version TEXT NOT NULL, phase TEXT NOT NULL DEFAULT 'expand', seq INT NOT NULL, filename TEXT NOT NULL,
  checksum TEXT NOT NULL, transactional BOOLEAN NOT NULL DEFAULT true, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (version, phase, seq));
INSERT INTO public._migrations (version, phase, seq, filename, checksum) VALUES
  ('v0.3.10', 'expand', 1, '001_a.sql', 'aaa'), ('v0.3.10', 'postdeploy', 1, '001_b.sql', 'bbb'), ('v0.3.11', 'expand', 1, '001_c.sql', 'ccc');
RESET ROLE;
`

var backupSeedLedger = []backup.LedgerRow{
	{Version: "v0.3.10", Phase: "expand", Seq: 1, Checksum: "aaa"},
	{Version: "v0.3.10", Phase: "postdeploy", Seq: 1, Checksum: "bbb"},
	{Version: "v0.3.11", Phase: "expand", Seq: 1, Checksum: "ccc"},
}

// runDatabaseBackupRoundTrip backs up the seeded purser database, changes it the way a contract migration would,
// and proves that a tampered backup is refused, that restore brings back the backed-up state exactly, and that
// rollback and finish work.
func runDatabaseBackupRoundTrip(t *testing.T, r ssh.StreamRunner, s DatabaseServer) {
	t.Helper()
	ctx := context.Background()
	query := func(db, sql string) string {
		t.Helper()
		out, err := DatabaseQuery(ctx, r, s, db, sql)
		if err != nil {
			t.Fatalf("%s: %s: %v", db, sql, err)
		}
		return strings.TrimSpace(out)
	}
	run := func(db, sql string) {
		t.Helper()
		query(db, sql)
	}

	dir := t.TempDir()
	loc := backup.LocalDir(dir)
	started := time.Now().UTC()
	entry, err := BackupDatabase(ctx, r, s, loc, backup.Database{Name: "purser"})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if entry.Engine != s.Engine || entry.Owner != "purser" || entry.LedgerDigest != backup.LedgerDigest(backupSeedLedger) {
		t.Fatalf("entry = %+v", entry)
	}
	if !grantsInclude(entry.Grants, "CONNECT", "purser_runtime") {
		t.Fatalf("grants = %+v; the runtime role's CONNECT must be recorded", entry.Grants)
	}
	for table, want := range map[string]int64{"purser.invoices": 4, "purser.invoice_lines": 4, "public._migrations": 3} {
		if entry.RowCounts[table] != want {
			t.Fatalf("row counts = %v; %s want %d", entry.RowCounts, table, want)
		}
	}
	m := &backup.Manifest{FormatVersion: backup.FormatVersion, CreatedAt: started, CompletedAt: time.Now().UTC(), Databases: []backup.Database{entry}}
	if err := backup.WriteManifest(ctx, loc, m); err != nil {
		t.Fatal(err)
	}
	read, err := backup.ReadManifest(ctx, loc)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.VerifyFiles(ctx, loc, read.Files(nil)); err != nil {
		t.Fatalf("fresh backup does not verify: %v", err)
	}
	liveLedger, err := ReadDatabaseLedger(ctx, r, s, "purser")
	if err != nil || backup.LedgerDigest(liveLedger) != entry.LedgerDigest {
		t.Fatalf("live ledger digest differs from the dumped one: %v", err)
	}

	// A contract-style change after the backup: rows added, a column dropped, a ledger row recorded.
	run("purser", "INSERT INTO purser.invoices (tenant_id, amount) VALUES ('t3', 1.00)")
	run("purser", "ALTER TABLE purser.invoices DROP COLUMN legacy_code")
	run("purser", "INSERT INTO public._migrations (version, phase, seq, filename, checksum) VALUES ('v0.3.11', 'contract', 1, '001_drop.sql', 'ddd')")

	// A damaged backup never reaches the live database.
	damaged := t.TempDir()
	if err := os.CopyFS(damaged, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(damaged, "postgres", "purser", "data.sql.gz")
	raw, err := os.ReadFile(dataFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataFile, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRestoredDatabase(ctx, r, s, backup.LocalDir(damaged), entry); err == nil {
		t.Fatal("a truncated data section restored without error")
	}
	if got := query("purser", "SELECT count(*) FROM purser.invoices"); got != "5" {
		t.Fatalf("live database changed by a failed restore: %s rows", got)
	}

	if err := PrepareRestoredDatabase(ctx, r, s, loc, entry); err != nil {
		t.Fatalf("prepare restore: %v", err)
	}
	if err := CompareCellStorageIdentity(ctx, r, s, "purser", "purser"+RestoreShadowSuffix); err != nil {
		t.Fatal(err)
	}
	if err := SwapRestoredDatabase(ctx, r, s, "purser"); err != nil {
		t.Fatalf("swap: %v", err)
	}
	assertRestored := func() {
		t.Helper()
		restoredLedger, ledgerErr := ReadDatabaseLedger(ctx, r, s, "purser")
		if ledgerErr != nil || backup.LedgerDigest(restoredLedger) != entry.LedgerDigest {
			t.Fatalf("restored ledger = %+v, %v", restoredLedger, ledgerErr)
		}
		for _, check := range []struct{ sql, want string }{
			{"SELECT count(*) FROM purser.invoices", "4"},
			{"SELECT count(*) FROM purser.invoice_lines", "4"},
			{"SELECT count(*) FROM information_schema.columns WHERE table_schema = 'purser' AND table_name = 'invoices' AND column_name = 'legacy_code'", "1"},
			{"SELECT note FROM purser.invoices WHERE legacy_code = 'L1'", "tab\there"},
			{"SELECT note IS NULL FROM purser.invoices WHERE legacy_code = 'L3'", "t"},
			{"SELECT note FROM purser.invoices WHERE tenant_id = 't2' AND legacy_code IS NULL", `back\slash`},
			{"SELECT count(*) FROM pg_constraint WHERE conname = 'invoice_lines_invoice_id_fkey'", "1"},
			{"SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid = 'purser.invoices'::regclass", "purser"},
			{"SELECT datacl::text LIKE '%purser_runtime=c/%' FROM pg_database WHERE datname = 'purser'", "t"},
			{"SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = 'purser'", "purser"},
		} {
			if got := query("purser", check.sql); got != check.want {
				t.Fatalf("after restore %s = %q, want %q", check.sql, got, check.want)
			}
		}
		if got := query("purser", "INSERT INTO purser.invoices (tenant_id, amount) VALUES ('t9', 1) RETURNING id > 4"); got != "t" {
			t.Fatalf("restored sequence would reuse ids: %s", got)
		}
		run("purser", "DELETE FROM purser.invoices WHERE tenant_id = 't9'")
	}
	assertRestored()
	if got := query("purser"+RestorePreviousSuffix, "SELECT count(*) FROM purser.invoices"); got != "5" {
		t.Fatalf("previous copy holds %s rows, want the 5 live rows from before the restore", got)
	}

	if err := RollbackRestoredDatabase(ctx, r, s, "purser"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := query("purser", "SELECT count(*) FROM public._migrations WHERE phase = 'contract'"); got != "1" {
		t.Fatalf("rollback did not put the pre-restore database back (contract rows = %s)", got)
	}

	if err := PrepareRestoredDatabase(ctx, r, s, loc, entry); err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	// Persist the exact state left by interruption between the two renames.
	if err := RenameDatabase(ctx, r, s, "purser", "purser"+RestorePreviousSuffix); err != nil {
		t.Fatal(err)
	}
	if err := FinishRestoredDatabase(ctx, r, s, "purser"); err == nil {
		t.Fatal("finish accepted a missing live database")
	}
	for name, want := range map[string]string{"purser" + RestorePreviousSuffix: "5", "purser" + RestoreShadowSuffix: "4"} {
		if got := query(name, "SELECT count(*) FROM purser.invoices"); got != want {
			t.Fatalf("finish changed %s: %s rows, want %s", name, got, want)
		}
	}
	if err := RollbackRestoredDatabase(ctx, r, s, "purser"); err != nil {
		t.Fatalf("recover interrupted swap: %v", err)
	}
	if got := query("purser", "SELECT count(*) FROM purser.invoices"); got != "5" {
		t.Fatalf("recovery lost original data: %s rows", got)
	}
	if got := query("purser"+RestoreShadowSuffix, "SELECT count(*) FROM purser.invoices"); got != "4" {
		t.Fatalf("recovery lost restored data: %s rows", got)
	}
	if err := SwapRestoredDatabase(ctx, r, s, "purser"); err != nil {
		t.Fatalf("second swap: %v", err)
	}
	if err := FinishRestoredDatabase(ctx, r, s, "purser"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	for _, name := range []string{"purser" + RestorePreviousSuffix, "purser" + RestoreShadowSuffix} {
		if exists, existsErr := ServerDatabaseExists(ctx, r, s, name); existsErr != nil || exists {
			t.Fatalf("%s still exists after finish (%v)", name, existsErr)
		}
	}
	assertRestored()
}

func grantsInclude(grants []backup.DatabaseGrant, privilege, grantee string) bool {
	for _, g := range grants {
		if g.Privilege == privilege && g.Grantee == grantee {
			return true
		}
	}
	return false
}

func TestDatabaseBackupRestoreRoundTrip_RealPG(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-pg-backup-restore")
	pgStart(t, name)
	installSudoShim(t, name)
	pgApply(t, name, "postgres", "CREATE ROLE purser LOGIN; CREATE ROLE purser_runtime LOGIN; CREATE DATABASE purser OWNER purser; GRANT CONNECT ON DATABASE purser TO purser_runtime;")
	pgApply(t, name, "purser", backupSeedSQL)
	runDatabaseBackupRoundTrip(t, dockerStreamRunner{container: name}, DatabaseServer{Engine: backup.EnginePostgres, Port: 5432})
}

func TestDatabaseBackupRestoreRoundTrip_RealYugabyte(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, "fw-sv-yb-backup-restore")
	ysql := func(db, sql string) {
		t.Helper()
		if out, err := docker(t, sql, "exec", "-i", container, "ysqlsh", "-h", "127.0.0.1", "-U", "yugabyte", "-d", db, "-v", "ON_ERROR_STOP=1", "-q"); err != nil {
			t.Fatalf("ysqlsh %s: %v\n%s", db, err, out)
		}
	}
	ysql("yugabyte", "DROP DATABASE IF EXISTS purser; DROP DATABASE IF EXISTS purser__restore; DROP DATABASE IF EXISTS purser__prerestore;")
	ysql("yugabyte", "DROP ROLE IF EXISTS purser_runtime; DROP ROLE IF EXISTS purser; CREATE ROLE purser LOGIN; CREATE ROLE purser_runtime LOGIN;")
	ysql("yugabyte", "CREATE DATABASE purser OWNER purser")
	ysql("yugabyte", "GRANT CONNECT ON DATABASE purser TO purser_runtime")
	ysql("purser", backupSeedSQL)
	runDatabaseBackupRoundTrip(t, dockerStreamRunner{container: container}, DatabaseServer{Engine: backup.EngineYugabyte, Port: 5433})
}

// TestClickHouseBackupRestoreRoundTrip_RealClickHouse dumps the billing fact tables of the real periscope baseline,
// lets new facts arrive, and proves the restore puts back exactly the backed-up rows (fingerprint and count) while
// keeping the live table and its views, and that rollback returns the newer rows.
type interruptedCopyRunner struct {
	ssh.StreamRunner
	copies int
}

func (r *interruptedCopyRunner) RunStream(ctx context.Context, command string, in io.Reader, out io.Writer) (*ssh.CommandResult, error) {
	if strings.Contains(command, "REPLACE PARTITION") && strings.Contains(command, "__prerestore_building") {
		r.copies++
		if r.copies == 2 {
			return nil, errors.New("injected second-partition copy failure")
		}
	}
	return r.StreamRunner.RunStream(ctx, command, in, out)
}

func TestClickHouseBackupRestoreRoundTrip_RealClickHouse(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-ch-backup-restore")
	chStart(t, name)
	baseline, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatal(err)
	}
	chApply(t, name, string(baseline))
	chApply(t, name, `CREATE TABLE IF NOT EXISTS periscope._migrations (
  version LowCardinality(String), phase LowCardinality(String), seq UInt32, filename String, checksum FixedString(64),
  applied_at DateTime64(3) DEFAULT now64()) ENGINE = ReplicatedReplacingMergeTree(applied_at) ORDER BY (version, phase, seq);
INSERT INTO periscope._migrations (version, phase, seq, filename, checksum) VALUES ('v0.3.10', 'expand', 1, '001.sql', repeat('a', 64));
INSERT INTO periscope.api_requests (timestamp, tenant_id, operation_type, operation_name, request_count)
  SELECT toDateTime('2026-07-01 00:00:00') + number * 86400, generateUUIDv4(), 'query', concat('op\t', toString(number)), number + 1 FROM numbers(40);`)

	ctx := context.Background()
	r := dockerStreamRunner{container: name}
	c := ClickHouseServer{Port: 9000, Database: "periscope"}
	columns, err := ClickHouseTableColumns(ctx, r, c, "api_requests")
	if err != nil {
		t.Fatal(err)
	}
	before, err := ClickHouseFingerprint(ctx, r, c, "api_requests", columns)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ReadClickHouseLedger(ctx, r, c)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger = %+v, %v", ledger, err)
	}

	loc := backup.LocalDir(t.TempDir())
	tables := map[string]backup.ClickHouseTable{}
	for _, table := range ClickHouseBackupTables {
		entry, backupErr := BackupClickHouseTable(ctx, r, c, loc, table)
		if backupErr != nil {
			t.Fatalf("backup %s: %v", table, backupErr)
		}
		tables[table] = entry
	}
	if tables["api_requests"].Rows != 40 {
		t.Fatalf("api_requests dumped %d rows, want 40", tables["api_requests"].Rows)
	}

	chApply(t, name, `INSERT INTO periscope.api_requests (timestamp, tenant_id, operation_type, request_count)
  SELECT toDateTime('2026-09-15 00:00:00') + number * 60, generateUUIDv4(), 'mutation', 7 FROM numbers(15);`)

	t.Run("interrupted preparation cannot roll back live data", func(t *testing.T) {
		live, err := ClickHouseFingerprint(ctx, r, c, "api_requests", columns)
		if err != nil {
			t.Fatal(err)
		}
		if err := PrepareRestoredClickHouseTable(ctx, r, c, loc, tables["api_requests"]); err != nil {
			t.Fatal(err)
		}
		interrupted := &interruptedCopyRunner{StreamRunner: r}
		if err := SwapRestoredClickHouseTable(ctx, interrupted, c, "api_requests"); err == nil || interrupted.copies != 2 {
			t.Fatalf("expected second-partition failure, got %v after %d copies", err, interrupted.copies)
		}
		if err := RollbackRestoredClickHouseTable(ctx, r, c, "api_requests"); err == nil {
			t.Fatal("rollback accepted an incomplete preparation")
		}
		got, err := ClickHouseFingerprint(ctx, r, c, "api_requests", columns)
		if err != nil || got != live {
			t.Fatalf("live table changed after rejected rollback: %q, %v; want %q", got, err, live)
		}
		if err := FinishRestoredClickHouseTable(ctx, r, c, "api_requests"); err != nil {
			t.Fatal(err)
		}
	})

	for _, table := range ClickHouseBackupTables {
		if err := PrepareRestoredClickHouseTable(ctx, r, c, loc, tables[table]); err != nil {
			t.Fatalf("prepare %s: %v", table, err)
		}
		if err := SwapRestoredClickHouseTable(ctx, r, c, table); err != nil {
			t.Fatalf("swap %s: %v", table, err)
		}
	}
	after, err := ClickHouseFingerprint(ctx, r, c, "api_requests", columns)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("restored api_requests fingerprint %q, backup had %q", after, before)
	}
	if n, countErr := ClickHouseRowCount(ctx, r, c, ClickHousePreviousTable("api_requests")); countErr != nil || n != 55 {
		t.Fatalf("previous copy has %d rows (%v), want the 55 live rows", n, countErr)
	}

	if err := RollbackRestoredClickHouseTable(ctx, r, c, "api_requests"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n, _ := ClickHouseRowCount(ctx, r, c, "api_requests"); n != 55 {
		t.Fatalf("rollback left %d rows, want 55", n)
	}
	for _, table := range ClickHouseBackupTables {
		if err := FinishRestoredClickHouseTable(ctx, r, c, table); err != nil {
			t.Fatal(err)
		}
	}
	leftovers, err := ClickHouseQuery(ctx, r, c, "SELECT count() FROM system.tables WHERE database = 'periscope' AND (name LIKE '%__restore' OR name LIKE '%__prerestore' OR name LIKE '%__prerestore_building')")
	if err != nil || strings.TrimSpace(leftovers) != "0" {
		t.Fatalf("restore tables left behind: %s (%v)", leftovers, err)
	}
}
