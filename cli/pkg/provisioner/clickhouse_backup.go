package provisioner

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/ssh"
)

// ClickHouseBackupTables are the authoritative ClickHouse tables: the finalized billing facts and the metering
// evidence Purser reads. Rollups, materialized views, raw telemetry and cursors are derived from them or from Kafka and
// are not backed up. The migration ledger is recorded in the manifest, not restored: a restore puts fact rows back
// into the live schema.
var ClickHouseBackupTables = []string{
	"viewer_sessions_final",
	"restream_sessions_final",
	"stream_sessions_final",
	"processing_segments_final",
	"api_requests",
	"storage_snapshots",
	"storage_gb_seconds_5m",
	"projection_divergences",
}

// ClickHouseServer addresses the ClickHouse coordinator from a shell on its host.
type ClickHouseServer struct {
	Port     int
	Password string
	Database string
}

// ClickHouseColumn is one stored column of a table.
type ClickHouseColumn struct {
	Name string
	Type string
}

func (c ClickHouseServer) validate() error {
	if !simpleDBIdentifier.MatchString(c.Database) {
		return fmt.Errorf("invalid clickhouse database name %q", c.Database)
	}
	return nil
}

func (c ClickHouseServer) client(query string) string {
	port := c.Port
	if port == 0 {
		port = 9000
	}
	return fmt.Sprintf("clickhouse-client --host 127.0.0.1 --port %d --database %s --query %s", port, shellQuote(c.Database), shellQuote(query))
}

// Passwords travel over stdin, never in the command SSH includes in errors.
// A base64 line separates the password from the following dump.
func (c ClickHouseServer) runStream(ctx context.Context, r ssh.StreamRunner, command string, in io.Reader, out io.Writer) (*ssh.CommandResult, error) {
	if c.Password != "" {
		secret := strings.NewReader(base64.StdEncoding.EncodeToString([]byte(c.Password)) + "\n")
		if in == nil {
			in = secret
		} else {
			in = io.MultiReader(secret, in)
		}
		command = "IFS= read -r fw_ch_password || exit 1\nexport CLICKHOUSE_PASSWORD=\"$(printf '%s' \"$fw_ch_password\" | base64 -d)\"\nunset fw_ch_password\n" + command
	}
	return r.RunStream(ctx, command, in, out)
}

func (c ClickHouseServer) table(name string) string { return "`" + c.Database + "`.`" + name + "`" }

// ClickHouseQuery runs one query on the coordinator and returns its TabSeparated output.
func ClickHouseQuery(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, query string) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	var out bytes.Buffer
	if _, err := c.runStream(ctx, r, c.client(query), nil, &out); err != nil {
		return "", fmt.Errorf("clickhouse query: %w", err)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// ClickHouseTableColumns lists the stored columns of a table in declaration order. MATERIALIZED, ALIAS and EPHEMERAL
// columns are computed on insert and are left out of the dump. An absent table has no columns.
func ClickHouseTableColumns(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string) ([]ClickHouseColumn, error) {
	out, err := ClickHouseQuery(ctx, r, c, fmt.Sprintf(
		"SELECT name, type FROM system.columns WHERE database = '%s' AND table = '%s' AND default_kind NOT IN ('MATERIALIZED', 'ALIAS', 'EPHEMERAL') ORDER BY position FORMAT TabSeparated",
		c.Database, escapeClickHouseString(table)))
	if err != nil {
		return nil, err
	}
	var columns []ClickHouseColumn
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		name, typ, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("unexpected column row %q", line)
		}
		columns = append(columns, ClickHouseColumn{Name: name, Type: typ})
	}
	return columns, nil
}

func escapeClickHouseString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

func quoteClickHouseColumns(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return strings.Join(quoted, ", ")
}

// DumpClickHouseTable streams the named columns of a table as gzip-compressed TabSeparated rows.
func DumpClickHouseTable(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string, columns []string, w io.Writer) error {
	if err := c.validate(); err != nil {
		return err
	}
	query := fmt.Sprintf("SELECT %s FROM %s FORMAT TabSeparated", quoteClickHouseColumns(columns), c.table(table))
	command := "bash -c " + shellQuote("set -o pipefail\n"+c.client(query)+" | gzip -c\n")
	if _, err := c.runStream(ctx, r, command, nil, w); err != nil {
		return fmt.Errorf("dump clickhouse table %s: %w", table, err)
	}
	return nil
}

// ReadClickHouseLedger reads the collapsed _migrations ledger. A database without the table has an empty ledger.
func ReadClickHouseLedger(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer) ([]backup.LedgerRow, error) {
	present, err := ClickHouseQuery(ctx, r, c, fmt.Sprintf("SELECT count() FROM system.tables WHERE database = '%s' AND name = '_migrations'", c.Database))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(present) == "0" {
		return nil, nil
	}
	out, err := ClickHouseQuery(ctx, r, c, "SELECT version, phase, seq, argMax(checksum, applied_at) FROM _migrations GROUP BY version, phase, seq ORDER BY version, phase, seq FORMAT TabSeparated")
	if err != nil {
		return nil, err
	}
	entries, err := parseClickHouseLedgerTSV(out)
	if err != nil {
		return nil, err
	}
	rows := make([]backup.LedgerRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, backup.LedgerRow{Version: e.Version, Phase: e.Phase, Seq: e.Seq, Checksum: e.Checksum})
	}
	return rows, nil
}

// ClickHouseRowCount counts a table's rows.
func ClickHouseRowCount(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string) (int64, error) {
	out, err := ClickHouseQuery(ctx, r, c, "SELECT count() FROM "+c.table(table))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// ClickHouseFingerprint is the parity fingerprint (row count and content hash) of a table over the given columns.
func ClickHouseFingerprint(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string, columns []ClickHouseColumn) (string, error) {
	args := make([]string, 0, len(columns))
	for _, col := range columns {
		quoted := quoteClickHouseColumns([]string{col.Name})
		if strings.HasPrefix(col.Type, "AggregateFunction(") {
			quoted = "finalizeAggregation(" + quoted + ")"
		}
		args = append(args, quoted)
	}
	return ClickHouseQuery(ctx, r, c, VerifyFingerprintSQL("`"+c.Database+"`", "`"+table+"`", strings.Join(args, ", ")))
}

// BackupClickHouseTable dumps one table into loc and returns its manifest entry with the dumped row count.
func BackupClickHouseTable(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, loc backup.Location, table string) (backup.ClickHouseTable, error) {
	columns, err := ClickHouseTableColumns(ctx, r, c, table)
	if err != nil {
		return backup.ClickHouseTable{}, err
	}
	if len(columns) == 0 {
		return backup.ClickHouseTable{}, fmt.Errorf("clickhouse table %s.%s does not exist", c.Database, table)
	}
	names := make([]string, len(columns))
	for i, col := range columns {
		names[i] = col.Name
	}
	var rows int64
	file, err := backup.StoreCompressed(ctx, loc, backup.ClickHouseKey(c.Database)+"/"+table+".tsv.gz",
		func(w io.Writer) error { return DumpClickHouseTable(ctx, r, c, table, names, w) },
		func(rd io.Reader) error {
			n, countErr := backup.CountLines(rd)
			rows = n
			return countErr
		})
	if err != nil {
		return backup.ClickHouseTable{}, err
	}
	return backup.ClickHouseTable{Name: table, Columns: names, Rows: rows, File: file}, nil
}

// ClickHouseShadowTable and ClickHousePreviousTable name the tables a restore creates next to a live table.
func ClickHouseShadowTable(table string) string   { return table + RestoreShadowSuffix }
func ClickHousePreviousTable(table string) string { return table + RestorePreviousSuffix }

// clickHousePreparingTable is never a rollback source: its partition copy may be incomplete.
func clickHousePreparingTable(table string) string {
	return table + RestorePreviousSuffix + "_building"
}

// ClickHousePreviousExists reports whether a verified rollback copy has been published.
func ClickHousePreviousExists(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string) (bool, error) {
	out, err := ClickHouseQuery(ctx, r, c, fmt.Sprintf("SELECT count() FROM system.tables WHERE database = '%s' AND name = '%s'", c.Database, escapeClickHouseString(ClickHousePreviousTable(table))))
	if err != nil {
		return false, err
	}
	count, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	return count > 0, err
}

// PreflightClickHouseRestore rejects leftovers before any engine publishes a restore.
func PreflightClickHouseRestore(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string) error {
	for _, name := range []string{ClickHousePreviousTable(table), clickHousePreparingTable(table)} {
		out, err := ClickHouseQuery(ctx, r, c, fmt.Sprintf("SELECT count() FROM system.tables WHERE database = '%s' AND name = '%s'", c.Database, escapeClickHouseString(name)))
		if err != nil {
			return err
		}
		count, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
		if err != nil {
			return fmt.Errorf("check ClickHouse recovery table %s: %w", name, err)
		}
		if count != 0 {
			return fmt.Errorf("ClickHouse recovery table %s exists; inspect the earlier restore and run restore finish or rollback before restoring again", name)
		}
	}
	return nil
}

// PrepareRestoredClickHouseTable loads a table's backup into a shadow table with the live table's structure and
// checks the loaded row count. Every recorded column must still exist in the live table.
func PrepareRestoredClickHouseTable(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, loc backup.Location, table backup.ClickHouseTable) error {
	live, err := ClickHouseTableColumns(ctx, r, c, table.Name)
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return fmt.Errorf("clickhouse table %s.%s does not exist; restore needs its current schema", c.Database, table.Name)
	}
	var missing []string
	for _, name := range table.Columns {
		if !slices.ContainsFunc(live, func(col ClickHouseColumn) bool { return col.Name == name }) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("clickhouse table %s.%s no longer has column(s) %s recorded in the backup", c.Database, table.Name, strings.Join(missing, ", "))
	}
	shadow := ClickHouseShadowTable(table.Name)
	if _, dropErr := ClickHouseQuery(ctx, r, c, "DROP TABLE IF EXISTS "+c.table(shadow)+" SYNC"); dropErr != nil {
		return dropErr
	}
	if _, createErr := ClickHouseQuery(ctx, r, c, fmt.Sprintf("CREATE TABLE %s AS %s", c.table(shadow), c.table(table.Name))); createErr != nil {
		return createErr
	}
	rd, err := loc.Open(ctx, table.File.Path)
	if err != nil {
		return err
	}
	verified := backup.NewVerifyingReader(rd, table.File)
	var loadErr error
	if table.Rows == 0 {
		// clickhouse-client refuses an INSERT without rows; the empty dump is still read to verify it.
		_, loadErr = io.Copy(io.Discard, verified)
	} else {
		query := fmt.Sprintf("INSERT INTO %s (%s) FORMAT TabSeparated", c.table(shadow), quoteClickHouseColumns(table.Columns))
		command := "bash -c " + shellQuote("set -o pipefail\ngunzip -c | "+c.client(query)+"\n")
		_, loadErr = c.runStream(ctx, r, command, verified, io.Discard)
	}
	closeErr := verified.Close()
	if loadErr != nil {
		return fmt.Errorf("load %s: %w", shadow, loadErr)
	}
	if closeErr != nil {
		return closeErr
	}
	count, err := ClickHouseRowCount(ctx, r, c, shadow)
	if err != nil {
		return err
	}
	if count != table.Rows {
		return fmt.Errorf("restored %s has %d rows, the backup has %d", shadow, count, table.Rows)
	}
	return nil
}

// ClickHousePartitions lists the active partition ids of a table.
func ClickHousePartitions(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string) ([]string, error) {
	out, err := ClickHouseQuery(ctx, r, c, fmt.Sprintf(
		"SELECT DISTINCT partition_id FROM system.parts WHERE database = '%s' AND table = '%s' AND active ORDER BY partition_id FORMAT TabSeparated",
		c.Database, escapeClickHouseString(table)))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			ids = append(ids, line)
		}
	}
	return ids, nil
}

// replaceClickHouseTableData makes target hold exactly the rows of source, partition by partition. REPLACE
// PARTITION does not fire the materialized views that read the target, so derived tables are not fed twice.
func replaceClickHouseTableData(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, target, source string) error {
	targetParts, err := ClickHousePartitions(ctx, r, c, target)
	if err != nil {
		return err
	}
	sourceParts, err := ClickHousePartitions(ctx, r, c, source)
	if err != nil {
		return err
	}
	for _, id := range targetParts {
		if !slices.Contains(sourceParts, id) {
			if _, err := ClickHouseQuery(ctx, r, c, fmt.Sprintf("ALTER TABLE %s DROP PARTITION ID '%s'", c.table(target), escapeClickHouseString(id))); err != nil {
				return err
			}
		}
	}
	for _, id := range sourceParts {
		if _, err := ClickHouseQuery(ctx, r, c, fmt.Sprintf("ALTER TABLE %s REPLACE PARTITION ID '%s' FROM %s", c.table(target), escapeClickHouseString(id), c.table(source))); err != nil {
			return err
		}
	}
	return nil
}

// SwapRestoredClickHouseTable copies the live rows into the previous-copy table, then replaces the live rows with
// the shadow's, and checks that the live table now carries the shadow's fingerprint. The live table keeps its
// identity, so the materialized views that read it keep working.
func SwapRestoredClickHouseTable(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string) error {
	shadow, previous := ClickHouseShadowTable(table), ClickHousePreviousTable(table)
	exists, err := ClickHousePreviousExists(ctx, r, c, table)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%s.%s already exists from an earlier restore; run `frameworks cluster restore finish` or `restore rollback` first", c.Database, previous)
	}
	preparing := clickHousePreparingTable(table)
	if _, createErr := ClickHouseQuery(ctx, r, c, fmt.Sprintf("CREATE TABLE %s AS %s", c.table(preparing), c.table(table))); createErr != nil {
		return createErr
	}
	if copyErr := replaceClickHouseTableData(ctx, r, c, preparing, table); copyErr != nil {
		return fmt.Errorf("copy live %s aside: %w", table, copyErr)
	}
	columns, err := ClickHouseTableColumns(ctx, r, c, table)
	if err != nil {
		return err
	}
	live, err := ClickHouseFingerprint(ctx, r, c, table, columns)
	if err != nil {
		return err
	}
	copied, err := ClickHouseFingerprint(ctx, r, c, preparing, columns)
	if err != nil {
		return err
	}
	if live != copied {
		return fmt.Errorf("%s previous-copy fingerprint %q does not match live %q", table, copied, live)
	}
	// Publishing one verified table name is atomic. A crash before this rename leaves
	// only a preparation table; a crash after it leaves a complete rollback source.
	if _, renameErr := ClickHouseQuery(ctx, r, c, fmt.Sprintf("RENAME TABLE %s TO %s", c.table(preparing), c.table(previous))); renameErr != nil {
		return renameErr
	}
	if replaceErr := replaceClickHouseTableData(ctx, r, c, table, shadow); replaceErr != nil {
		return fmt.Errorf("replace %s with the restored rows: %w", table, replaceErr)
	}
	want, err := ClickHouseFingerprint(ctx, r, c, shadow, columns)
	if err != nil {
		return err
	}
	got, err := ClickHouseFingerprint(ctx, r, c, table, columns)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s fingerprint %q after the swap, restored rows have %q", table, got, want)
	}
	return nil
}

// RollbackRestoredClickHouseTable puts the previous rows back and drops the restore's tables.
func RollbackRestoredClickHouseTable(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string) error {
	previous := ClickHousePreviousTable(table)
	exists, err := ClickHousePreviousExists(ctx, r, c, table)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s.%s has no %s copy; there is no restore to roll back", c.Database, table, previous)
	}
	if err := replaceClickHouseTableData(ctx, r, c, table, previous); err != nil {
		return err
	}
	return FinishRestoredClickHouseTable(ctx, r, c, table)
}

// FinishRestoredClickHouseTable drops the shadow and previous-copy tables of a restore.
func FinishRestoredClickHouseTable(ctx context.Context, r ssh.StreamRunner, c ClickHouseServer, table string) error {
	for _, name := range []string{ClickHousePreviousTable(table), clickHousePreparingTable(table), ClickHouseShadowTable(table)} {
		if _, err := ClickHouseQuery(ctx, r, c, "DROP TABLE IF EXISTS "+c.table(name)+" SYNC"); err != nil {
			return err
		}
	}
	return nil
}
