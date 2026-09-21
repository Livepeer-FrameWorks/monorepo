package provisioner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/ssh"

	"github.com/lib/pq"
)

// This file is the one place that knows how a PostgreSQL or YugabyteDB database is dumped, restored, inspected,
// and swapped for a backup. `cluster backup create` and `cluster restore` call only these functions, so a change to
// the database layout (such as YugabyteDB colocation) adapts backup and restore here.
//
// Every command runs on a database host through ssh.StreamRunner, as the administrative role the provisioning roles
// use: `postgres` over the local socket for PostgreSQL, and the `yugabyte` role over localhost for YugabyteDB.

// Suffixes of the databases a restore creates next to the live one.
const (
	RestoreShadowSuffix   = "__restore"
	RestorePreviousSuffix = "__prerestore"
)

// yugabyteBinaryResolver finds a YugabyteDB client binary in the layouts the yugabyte role and the upstream tarball
// install.
const yugabyteBinaryResolver = `fw_yb_bin() {
  for dir in /opt/yugabyte/postgres/bin /opt/yugabyte/bin /home/yugabyte/postgres/bin /home/yugabyte/bin; do
    if [ -x "$dir/$1" ]; then echo "$dir/$1"; return 0; fi
  done
  command -v "$1"
}
`

// DatabaseServer addresses one PostgreSQL or YugabyteDB server from a shell on its host.
type DatabaseServer struct {
	Engine string // backup.EnginePostgres or backup.EngineYugabyte
	Port   int
}

func (s DatabaseServer) validate() error {
	switch s.Engine {
	case backup.EnginePostgres, backup.EngineYugabyte:
	default:
		return fmt.Errorf("unsupported database engine %q", s.Engine)
	}
	if s.Port <= 0 {
		return fmt.Errorf("database port %d is invalid", s.Port)
	}
	return nil
}

// client returns the shell words that start the interactive client against database.
func (s DatabaseServer) client(database string) string {
	if s.Engine == backup.EngineYugabyte {
		return fmt.Sprintf(`"$(fw_yb_bin ysqlsh)" -X -v ON_ERROR_STOP=1 -h localhost -p %d -U yugabyte -d %s`, s.Port, shellQuote(database))
	}
	return fmt.Sprintf("sudo -u postgres psql -X -v ON_ERROR_STOP=1 -p %d -d %s", s.Port, shellQuote(database))
}

func (s DatabaseServer) maintenanceDatabase() string {
	if s.Engine == backup.EngineYugabyte {
		return "yugabyte"
	}
	return "postgres"
}

// script wraps body in bash with pipefail, with the YugabyteDB binary resolver defined.
func (s DatabaseServer) script(body string) string {
	prefix := "set -o pipefail\n"
	if s.Engine == backup.EngineYugabyte {
		prefix += yugabyteBinaryResolver
	}
	return "bash -c " + shellQuote(prefix+body)
}

// DumpDatabaseSectionCommand is the host command that writes one gzip-compressed dump section to stdout. ysql_dump
// reports some problems on stderr without failing, so any stderr output from it fails the section.
func DumpDatabaseSectionCommand(s DatabaseServer, database, section string) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	if !isBackupSection(section) {
		return "", fmt.Errorf("unknown dump section %q", section)
	}
	if !simpleDBIdentifier.MatchString(database) {
		return "", fmt.Errorf("invalid database name %q", database)
	}
	if s.Engine == backup.EngineYugabyte {
		return s.script(fmt.Sprintf(`err="$(mktemp)"; trap 'rm -f "$err"' EXIT
if ! "$(fw_yb_bin ysql_dump)" -h localhost -p %d -U yugabyte -d %s --section=%s 2>"$err" | gzip -c; then
  echo "ysql_dump --section=%s failed:" >&2; tail -c 2000 "$err" >&2; exit 5
fi
if [ -s "$err" ]; then echo "ysql_dump --section=%s wrote to stderr:" >&2; tail -c 2000 "$err" >&2; exit 4; fi
`, s.Port, shellQuote(database), section, section, section)), nil
	}
	return s.script(fmt.Sprintf("sudo -u postgres pg_dump -p %d -d %s --section=%s | gzip -c\n", s.Port, shellQuote(database), section)), nil
}

// RestoreDatabaseSectionCommand is the host command that loads one gzip-compressed dump section from stdin into
// database.
func RestoreDatabaseSectionCommand(s DatabaseServer, database string) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	if !simpleDBIdentifier.MatchString(database) {
		return "", fmt.Errorf("invalid database name %q", database)
	}
	return s.script(fmt.Sprintf("gunzip -c | %s -q\n", s.client(database))), nil
}

func isBackupSection(section string) bool {
	for _, known := range backup.Sections {
		if section == known {
			return true
		}
	}
	return false
}

// DumpDatabaseSection streams one compressed dump section of database to w.
func DumpDatabaseSection(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, database, section string, w io.Writer) error {
	command, err := DumpDatabaseSectionCommand(s, database, section)
	if err != nil {
		return err
	}
	if _, err := r.RunStream(ctx, command, nil, w); err != nil {
		return fmt.Errorf("dump %s section %s: %w", database, section, err)
	}
	return nil
}

// RestoreDatabaseSection loads one compressed dump section from rd into database.
func RestoreDatabaseSection(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, database string, rd io.Reader) error {
	command, err := RestoreDatabaseSectionCommand(s, database)
	if err != nil {
		return err
	}
	if _, err := r.RunStream(ctx, command, rd, io.Discard); err != nil {
		return fmt.Errorf("restore into %s: %w", database, err)
	}
	return nil
}

// DatabaseQuery runs one SQL statement in database and returns its unaligned, tuples-only output.
func DatabaseQuery(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, database, sql string) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	var out bytes.Buffer
	command := s.script(fmt.Sprintf("%s -q -tA -c %s\n", s.client(database), shellQuote(sql)))
	if _, err := r.RunStream(ctx, command, nil, &out); err != nil {
		return "", fmt.Errorf("query %s: %w", database, err)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// DatabaseFacts is what a restore needs to recreate a database as it was: owner, encoding and locale, and the
// database-level grants (the runtime role's CONNECT, for example).
type DatabaseFacts struct {
	Exists   bool
	Owner    string
	Encoding string
	Collate  string
	Ctype    string
	Grants   []backup.DatabaseGrant
}

// ReadDatabaseFacts reads a database's owner, encoding, locale and grants.
func ReadDatabaseFacts(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, database string) (DatabaseFacts, error) {
	out, err := DatabaseQuery(ctx, r, s, s.maintenanceDatabase(), fmt.Sprintf(
		"SELECT pg_get_userbyid(datdba) || '|' || pg_encoding_to_char(encoding) || '|' || datcollate || '|' || datctype FROM pg_database WHERE datname = %s",
		pq.QuoteLiteral(database)))
	if err != nil {
		return DatabaseFacts{}, err
	}
	if strings.TrimSpace(out) == "" {
		return DatabaseFacts{}, nil
	}
	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) != 4 {
		return DatabaseFacts{}, fmt.Errorf("unexpected pg_database row for %s: %q", database, out)
	}
	facts := DatabaseFacts{Exists: true, Owner: parts[0], Encoding: parts[1], Collate: parts[2], Ctype: parts[3]}
	grants, err := DatabaseQuery(ctx, r, s, s.maintenanceDatabase(), fmt.Sprintf(
		"SELECT a.privilege_type || '|' || CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END FROM pg_database d, aclexplode(d.datacl) a WHERE d.datname = %s ORDER BY 1",
		pq.QuoteLiteral(database)))
	if err != nil {
		return DatabaseFacts{}, err
	}
	for _, line := range strings.Split(grants, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		privilege, grantee, ok := strings.Cut(line, "|")
		if !ok {
			return DatabaseFacts{}, fmt.Errorf("unexpected grant row for %s: %q", database, line)
		}
		facts.Grants = append(facts.Grants, backup.DatabaseGrant{Privilege: privilege, Grantee: grantee})
	}
	return facts, nil
}

// ReadDatabaseLedger reads a database's _migrations rows. A database without the table has an empty ledger.
func ReadDatabaseLedger(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, database string) ([]backup.LedgerRow, error) {
	present, err := DatabaseQuery(ctx, r, s, database, "SELECT to_regclass('_migrations') IS NOT NULL")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(present) != "t" {
		return nil, nil
	}
	out, err := DatabaseQuery(ctx, r, s, database, "SELECT version || '|' || phase || '|' || seq || '|' || checksum FROM _migrations ORDER BY version, phase, seq")
	if err != nil {
		return nil, err
	}
	var rows []backup.LedgerRow
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 4 {
			return nil, fmt.Errorf("unexpected ledger row in %s: %q", database, line)
		}
		seq, convErr := strconv.Atoi(fields[2])
		if convErr != nil {
			return nil, fmt.Errorf("ledger row in %s: seq %q: %w", database, fields[2], convErr)
		}
		rows = append(rows, backup.LedgerRow{Version: fields[0], Phase: fields[1], Seq: seq, Checksum: fields[3]})
	}
	return rows, nil
}

// CountDatabaseRows counts the rows of the named tables. Table names are the qualified names pg_dump writes in its
// COPY headers, which are already quoted where needed.
func CountDatabaseRows(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, database string, tables []string) (map[string]int64, error) {
	sorted := append([]string(nil), tables...)
	sort.Strings(sorted)
	counts := make(map[string]int64, len(sorted))
	const batch = 100
	for start := 0; start < len(sorted); start += batch {
		end := min(start+batch, len(sorted))
		parts := make([]string, 0, end-start)
		for _, table := range sorted[start:end] {
			parts = append(parts, fmt.Sprintf("SELECT %s || '|' || count(*) FROM %s", pq.QuoteLiteral(table), table))
		}
		out, err := DatabaseQuery(ctx, r, s, database, strings.Join(parts, " UNION ALL "))
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			idx := strings.LastIndex(line, "|")
			if idx < 0 {
				return nil, fmt.Errorf("unexpected row count %q", line)
			}
			n, convErr := strconv.ParseInt(line[idx+1:], 10, 64)
			if convErr != nil {
				return nil, fmt.Errorf("row count %q: %w", line, convErr)
			}
			counts[line[:idx]] = n
		}
	}
	return counts, nil
}

// CreateRestoreShadow creates an empty database to restore into, with the owner, encoding, locale and grants the
// backup recorded. A leftover shadow from an interrupted restore is dropped first.
func CreateRestoreShadow(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, db backup.Database, shadow string) error {
	if !simpleDBIdentifier.MatchString(shadow) {
		return fmt.Errorf("invalid database name %q", shadow)
	}
	if err := DropDatabase(ctx, r, s, shadow); err != nil {
		return err
	}
	statement := fmt.Sprintf("CREATE DATABASE %s OWNER %s", pq.QuoteIdentifier(shadow), pq.QuoteIdentifier(db.Owner))
	if s.Engine == backup.EnginePostgres && db.Encoding != "" {
		statement += fmt.Sprintf(" TEMPLATE template0 ENCODING %s LC_COLLATE %s LC_CTYPE %s",
			pq.QuoteLiteral(db.Encoding), pq.QuoteLiteral(db.Collate), pq.QuoteLiteral(db.Ctype))
	}
	if _, err := DatabaseQuery(ctx, r, s, s.maintenanceDatabase(), statement); err != nil {
		return err
	}
	for _, grant := range db.Grants {
		grantee := pq.QuoteIdentifier(grant.Grantee)
		if grant.Grantee == "PUBLIC" {
			grantee = "PUBLIC"
		}
		if !isDatabasePrivilege(grant.Privilege) {
			return fmt.Errorf("database %s: unexpected privilege %q", db.Name, grant.Privilege)
		}
		if _, err := DatabaseQuery(ctx, r, s, s.maintenanceDatabase(), fmt.Sprintf("GRANT %s ON DATABASE %s TO %s", grant.Privilege, pq.QuoteIdentifier(shadow), grantee)); err != nil {
			return err
		}
	}
	return nil
}

func isDatabasePrivilege(p string) bool {
	switch p {
	case "CREATE", "CONNECT", "TEMPORARY":
		return true
	}
	return false
}

// ServerDatabaseExists reports whether a database exists on the server.
func ServerDatabaseExists(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, name string) (bool, error) {
	out, err := DatabaseQuery(ctx, r, s, s.maintenanceDatabase(), fmt.Sprintf("SELECT count(*) FROM pg_database WHERE datname = %s", pq.QuoteLiteral(name)))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

// DropDatabase drops a database if it exists, ending its sessions on this server first.
func DropDatabase(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, name string) error {
	if err := TerminateDatabaseSessions(ctx, r, s, name); err != nil {
		return err
	}
	_, err := DatabaseQuery(ctx, r, s, s.maintenanceDatabase(), "DROP DATABASE IF EXISTS "+pq.QuoteIdentifier(name))
	return err
}

// TerminateDatabaseSessions ends the sessions connected to a database on this server. On YugabyteDB the view covers
// only the local tserver; the services that use the database are stopped before a restore, so no remote session is
// expected, and a remaining one fails the rename.
func TerminateDatabaseSessions(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, name string) error {
	_, err := DatabaseQuery(ctx, r, s, s.maintenanceDatabase(), fmt.Sprintf(
		"SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE datname = %s AND pid <> pg_backend_pid()", pq.QuoteLiteral(name)))
	return err
}

// RenameDatabase renames a database after ending its sessions.
func RenameDatabase(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, from, to string) error {
	if err := TerminateDatabaseSessions(ctx, r, s, from); err != nil {
		return err
	}
	_, err := DatabaseQuery(ctx, r, s, s.maintenanceDatabase(), fmt.Sprintf("ALTER DATABASE %s RENAME TO %s", pq.QuoteIdentifier(from), pq.QuoteIdentifier(to)))
	return err
}

// SwapRestoredDatabase puts the restored shadow in place of the live database and keeps the live one as the
// previous copy. When the live database does not exist (a restore after it was lost), the shadow is only renamed.
// A failed second rename puts the live database back.
func SwapRestoredDatabase(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, live string) error {
	shadow, previous := live+RestoreShadowSuffix, live+RestorePreviousSuffix
	exists, err := ServerDatabaseExists(ctx, r, s, live)
	if err != nil {
		return err
	}
	if exists {
		if err := RenameDatabase(ctx, r, s, live, previous); err != nil {
			return fmt.Errorf("move %s aside: %w", live, err)
		}
	}
	if err := RenameDatabase(ctx, r, s, shadow, live); err != nil {
		if exists {
			if backErr := RenameDatabase(ctx, r, s, previous, live); backErr != nil {
				return fmt.Errorf("put restored %s in place: %w; and moving the original back failed (it is %s now): %w", live, err, previous, backErr)
			}
		}
		return fmt.Errorf("put restored %s in place: %w", live, err)
	}
	return nil
}

// RollbackRestoredDatabase puts the previous copy back and keeps the restored database as the shadow.
func RollbackRestoredDatabase(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, live string) error {
	shadow, previous := live+RestoreShadowSuffix, live+RestorePreviousSuffix
	hasPrevious, err := ServerDatabaseExists(ctx, r, s, previous)
	if err != nil {
		return err
	}
	if !hasPrevious {
		return fmt.Errorf("%s has no %s copy; there is no restore to roll back", live, previous)
	}
	hasLive, err := ServerDatabaseExists(ctx, r, s, live)
	if err != nil {
		return err
	}
	// The swap and its recovery both have a gap between their two renames.
	// Restore the original name first; the shadow may be the only restored copy.
	if !hasLive {
		return RenameDatabase(ctx, r, s, previous, live)
	}
	hasShadow, err := ServerDatabaseExists(ctx, r, s, shadow)
	if err != nil {
		return err
	}
	if hasShadow {
		return fmt.Errorf("%s, %s and %s all exist; refusing to discard an ambiguous restore copy", live, previous, shadow)
	}
	if err := RenameDatabase(ctx, r, s, live, shadow); err != nil {
		return fmt.Errorf("move restored %s aside: %w", live, err)
	}
	if err := RenameDatabase(ctx, r, s, previous, live); err != nil {
		if backErr := RenameDatabase(ctx, r, s, shadow, live); backErr != nil {
			return fmt.Errorf("put %s back: %w; and returning the restored copy failed: %w", live, err, backErr)
		}
		return fmt.Errorf("put %s back: %w", live, err)
	}
	return nil
}

// FinishRestoredDatabase drops the previous copy and any shadow a restore left behind.
func FinishRestoredDatabase(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, live string) error {
	exists, err := ServerDatabaseExists(ctx, r, s, live)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing; refusing to delete recovery copies; use restore rollback to recover the original database", live)
	}
	if err := DropDatabase(ctx, r, s, live+RestorePreviousSuffix); err != nil {
		return err
	}
	return DropDatabase(ctx, r, s, live+RestoreShadowSuffix)
}

// BackupDatabase dumps one database into loc under its manifest key and returns the completed manifest entry.
// entry carries the identity (Instance, Name, Source); the rest is filled from the server and the dump. The ledger
// and row counts come from the data section, and the live ledger read before and after the dump must equal them,
// so a migration that ran during the dump fails the backup.
func BackupDatabase(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, loc backup.Location, entry backup.Database) (backup.Database, error) {
	facts, err := ReadDatabaseFacts(ctx, r, s, entry.Name)
	if err != nil {
		return entry, err
	}
	if !facts.Exists {
		return entry, fmt.Errorf("database %s does not exist", entry.Name)
	}
	entry.Engine = s.Engine
	entry.Owner, entry.Grants = facts.Owner, facts.Grants
	if s.Engine == backup.EnginePostgres {
		entry.Encoding, entry.Collate, entry.Ctype = facts.Encoding, facts.Collate, facts.Ctype
	}
	before, err := ReadDatabaseLedger(ctx, r, s, entry.Name)
	if err != nil {
		return entry, err
	}
	var evidence *backup.DumpEvidence
	entry.Sections = nil
	for _, section := range backup.Sections {
		name := entry.Key() + "/" + section + ".sql.gz"
		produce := func(w io.Writer) error { return DumpDatabaseSection(ctx, r, s, entry.Name, section, w) }
		var file backup.File
		if section == "data" {
			file, err = backup.StoreCompressed(ctx, loc, name, produce, func(rd io.Reader) error {
				parsed, parseErr := backup.ParseDataSection(rd)
				evidence = parsed
				return parseErr
			})
		} else {
			file, err = backup.StoreFile(ctx, loc, name, produce)
		}
		if err != nil {
			return entry, err
		}
		entry.Sections = append(entry.Sections, backup.Section{Name: section, File: file})
	}
	after, err := ReadDatabaseLedger(ctx, r, s, entry.Name)
	if err != nil {
		return entry, err
	}
	dumped := backup.LedgerDigest(evidence.Ledger)
	if backup.LedgerDigest(before) != dumped || backup.LedgerDigest(after) != dumped {
		return entry, fmt.Errorf("the migration ledger of %s changed during the backup; run it again when no migration is running", entry.Name)
	}
	entry.Ledger, entry.LedgerDigest, entry.RowCounts = evidence.Ledger, dumped, evidence.RowCounts
	if entry.Ledger == nil {
		entry.Ledger = []backup.LedgerRow{}
	}
	return entry, nil
}

// PrepareRestoredDatabase restores db from loc into the shadow database next to the live one and checks the result
// against the manifest. The live database is not touched.
func PrepareRestoredDatabase(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, loc backup.Location, db backup.Database) error {
	shadow := db.Name + RestoreShadowSuffix
	if err := CreateRestoreShadow(ctx, r, s, db, shadow); err != nil {
		return fmt.Errorf("create %s: %w", shadow, err)
	}
	for _, section := range db.Sections {
		rd, err := loc.Open(ctx, section.File.Path)
		if err != nil {
			return err
		}
		verified := backup.NewVerifyingReader(rd, section.File)
		restoreErr := RestoreDatabaseSection(ctx, r, s, shadow, verified)
		closeErr := verified.Close()
		if restoreErr != nil {
			return fmt.Errorf("%s section %s: %w", db.Name, section.Name, restoreErr)
		}
		if closeErr != nil {
			return fmt.Errorf("%s section %s: %w", db.Name, section.Name, closeErr)
		}
	}
	return CompareRestoredDatabase(ctx, r, s, shadow, db)
}

// CompareCellStorageIdentity refuses a restored Foghorn database whose cell storage identity differs from the live
// one: each cell's database belongs to one immutable S3 backend. Databases without the table are not Foghorn cells.
func CompareCellStorageIdentity(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, live, restored string) error {
	read := func(database string) (string, error) {
		present, err := DatabaseQuery(ctx, r, s, database, "SELECT to_regclass('foghorn.cell_storage_identity') IS NOT NULL")
		if err != nil || strings.TrimSpace(present) != "t" {
			return "", err
		}
		return DatabaseQuery(ctx, r, s, database, "SELECT backend_id FROM foghorn.cell_storage_identity")
	}
	liveID, err := read(live)
	if err != nil {
		return err
	}
	restoredID, err := read(restored)
	if err != nil {
		return err
	}
	if liveID != "" && restoredID != "" && liveID != restoredID {
		return fmt.Errorf("%s belongs to storage backend %q but the backup belongs to %q; a Foghorn cell database cannot be restored from another cell", live, liveID, restoredID)
	}
	return nil
}

// CompareRestoredDatabase checks a restored database against its manifest entry: the ledger must have the recorded
// digest and every table must hold the recorded number of rows.
func CompareRestoredDatabase(ctx context.Context, r ssh.StreamRunner, s DatabaseServer, restored string, db backup.Database) error {
	ledger, err := ReadDatabaseLedger(ctx, r, s, restored)
	if err != nil {
		return err
	}
	var problems []string
	if digest := backup.LedgerDigest(ledger); digest != db.LedgerDigest {
		problems = append(problems, fmt.Sprintf("ledger digest %s, manifest says %s", digest, db.LedgerDigest))
	}
	tables := make([]string, 0, len(db.RowCounts))
	for table := range db.RowCounts {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	counts, err := CountDatabaseRows(ctx, r, s, restored, tables)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if got, want := counts[table], db.RowCounts[table]; got != want {
			problems = append(problems, fmt.Sprintf("%s has %d rows, manifest says %d", table, got, want))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("restored %s does not match the backup: %s", db.Name, strings.Join(problems, "; "))
	}
	return nil
}
