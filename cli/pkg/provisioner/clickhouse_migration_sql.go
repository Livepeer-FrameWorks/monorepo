package provisioner

import (
	"fmt"
	"strings"
)

// escapeCHString escapes a value for a single-quoted ClickHouse string literal
// (backslash first, then quote), so a password/host/partition-id containing a
// quote can't break or inject into the generated SQL.
func escapeCHString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

// ClickHouse cross-host data-migration SQL builder.
//
// Pure string templating — all statements run ON THE DESTINATION (new) node and
// pull from the source (old) node via the `remote()` table function over the
// WireGuard mesh on the plaintext native port (the role runs SSL off, so
// remoteSecure:9440 is not available; the mesh already encrypts the hop). The
// caller supplies partition metadata read from authoritative `system.tables` /
// `system.parts` at run time — none of it is inferred here.

// RemoteSource describes the old ClickHouse node as a `remote()` source.
type RemoteSource struct {
	Host string // old node mesh hostname, e.g. yuga-eu-1.internal
	Port int    // native port (9000)
	DB   string // database, e.g. periscope
	User string
	Pass string
}

func (r RemoteSource) port() int {
	if r.Port == 0 {
		return 9000
	}
	return r.Port
}

// table returns the `remote(...)` table-function expression for one table. The
// password is inline (remote() has no out-of-band cred channel); callers must
// stage the resulting SQL with no_log + 0600 + cleanup, exactly like migrate.yml.
// Remote builds a `remote(...)` expression for any db.table on the source, with
// escaped credentials. db/table are bare identifiers (trusted catalog / system).
func (r RemoteSource) Remote(db, table string) string {
	return fmt.Sprintf("remote('%s:%d', %s, %s, '%s', '%s')",
		escapeCHString(r.Host), r.port(), db, table, escapeCHString(r.User), escapeCHString(r.Pass))
}

func (r RemoteSource) table(table string) string { return r.Remote(r.DB, table) }

// migStageSuffix is appended to a table name to form its migration staging table.
const migStageSuffix = "__migstage"

// SyncPartitionSQL idempotently re-copies a single partition, keyed on the stable
// `partition_id` (NOT the formatted `partition` value) so it is correct for ANY
// partition shape — scalar, expression, or tuple (e.g. (toYYYYMM(ts), tenant_id)),
// where the displayed partition value is not a usable SQL literal. It fills a
// staging table from the source via the `_partition_id` virtual column, atomically
// REPLACEs the destination partition by ID, then truncates staging. `REPLACE
// PARTITION … FROM` is local-only, so staging is the cross-host landing zone.
// Idempotent for additive engines (Summing/Aggregating): a re-run REPLACES, never adds.
func SyncPartitionSQL(src RemoteSource, db, table, partitionID string) []string {
	stage := table + migStageSuffix
	pid := escapeCHString(partitionID)
	return []string{
		fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s.%s AS %s.%s", db, stage, db, table),
		fmt.Sprintf("TRUNCATE TABLE IF EXISTS %s.%s", db, stage),
		fmt.Sprintf("INSERT INTO %s.%s SELECT * FROM %s WHERE _partition_id = '%s'",
			db, stage, src.table(table), pid),
		fmt.Sprintf("ALTER TABLE %s.%s REPLACE PARTITION ID '%s' FROM %s.%s",
			db, table, pid, db, stage),
		fmt.Sprintf("TRUNCATE TABLE %s.%s", db, stage),
	}
}

// DropStagingSQL removes a table's migration staging sibling. Run after a table's
// sync completes so no `__migstage` artifacts linger in the Replicated database
// (they would otherwise show up as uncatalogued tables in verify).
func DropStagingSQL(db, table string) string {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s.%s SYNC", db, table+migStageSuffix)
}

// FullReplaceTableSQL idempotently re-copies an UNPARTITIONED table: fill staging
// from the source, then atomically EXCHANGE it with the destination, then drop
// staging. EXCHANGE TABLES is atomic on Atomic/Replicated databases, so readers
// never see an empty table. Used for current-state / unpartitioned tables that
// have no partition key to slice on.
func FullReplaceTableSQL(src RemoteSource, db, table string) []string {
	stage := table + migStageSuffix
	return []string{
		fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s.%s AS %s.%s", db, stage, db, table),
		fmt.Sprintf("TRUNCATE TABLE IF EXISTS %s.%s", db, stage),
		fmt.Sprintf("INSERT INTO %s.%s SELECT * FROM %s", db, stage, src.table(table)),
		fmt.Sprintf("EXCHANGE TABLES %s.%s AND %s.%s", db, table, db, stage),
		fmt.Sprintf("DROP TABLE IF EXISTS %s.%s SYNC", db, stage),
	}
}

// StopRefreshableViewSQL / StartRefreshableViewSQL pause a refreshable MV for the
// migration (APPEND mode double-appends if it fires mid-copy) and resume at cutover.
// Insert-trigger MVs need no such toggle: the copy lands rows via staging +
// ALTER REPLACE PARTITION / EXCHANGE, none of which fires an MV (only a direct
// INSERT into the MV's own source table does), and each MV's target table is
// itself copied directly from the source.
func StopRefreshableViewSQL(db, mv string) string {
	return fmt.Sprintf("SYSTEM STOP VIEW %s.%s", db, mv)
}
func StartRefreshableViewSQL(db, mv string) string {
	return fmt.Sprintf("SYSTEM START VIEW %s.%s", db, mv)
}

// VerifyFingerprintSQL / VerifyRemoteFingerprintSQL return a parity fingerprint:
// row count plus a content hash over the columns named in hashArgs — both computed
// with `final = 1` so additive/replacing merge state is COLLAPSED first (a raw
// count differs between nodes purely by unmerged-part count, so it is not a valid
// parity check for Summing/Aggregating/Replacing). The hash catches value drift
// that equal counts would miss. The caller builds hashArgs as a column list in
// which AggregateFunction(...) columns are wrapped in finalizeAggregation() —
// cityHash64 can't digest a raw aggregate state, but it CAN digest the finalized
// value, so aggregate-state tables get a real content hash, not just a count.
// Output is TSV: "<count>\t<hash>".
func VerifyFingerprintSQL(db, table, hashArgs string) string {
	return fmt.Sprintf("SELECT count(), sum(cityHash64(%s)) FROM %s.%s SETTINGS final = 1", hashArgs, db, table)
}
func VerifyRemoteFingerprintSQL(src RemoteSource, table, hashArgs string) string {
	return fmt.Sprintf("SELECT count(), sum(cityHash64(%s)) FROM %s SETTINGS final = 1", hashArgs, src.table(table))
}
