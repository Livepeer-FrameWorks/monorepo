# Backup and Restore - CLI Backups of the Authoritative Stores

`frameworks cluster backup create` dumps every store the cluster cannot rebuild, and `frameworks cluster restore`
puts it back through a verified shadow and a reversible swap. The same backups gate the two operations a service
rollback cannot undo: contract migrations and irreversible data migrations. Operator usage is in
[operators/backup-restore.mdx](../../website_docs/src/content/docs/operators/backup-restore.mdx).

## Architecture

```
operator machine (frameworks CLI)                      database hosts (over SSH)
┌───────────────────────────────────────┐              ┌──────────────────────────────────────────┐
│ cli/cmd/cluster_backup.go             │  RunStream   │ bash -o pipefail:                          │
│   planBackupTargets (manifest)        │─────────────▶│  sudo -u postgres pg_dump --section=S | gzip│
│   provisioner.BackupDatabase          │◀── gzip ─────│  ysql_dump -h localhost --section=S | gzip │
│   provisioner.BackupClickHouseTable   │              │  clickhouse-client SELECT … TSV | gzip     │
│        │ StoreFile / StoreCompressed  │              └──────────────────────────────────────────┘
│        ▼   (sha256, size, evidence)   │
│ backup.Location: LocalDir | S3Prefix  │  → <to>/frameworks-backup-<UTC>/…, manifest.json last
└───────────────────────────────────────┘
```

- `cli/pkg/backup` is engine-independent: the manifest, the locations (local directory and S3 with its own
  multipart writer), the streaming store with in-flight inspection, the dump evidence parser, the ledger digest,
  and the gate.
- `cli/pkg/provisioner/database_backup.go` is the one place that knows how a PostgreSQL or YugabyteDB database is
  dumped, restored, inspected, compared and swapped. `clickhouse_backup.go` is its ClickHouse counterpart.
- `cli/cmd/cluster_backup.go`, `cluster_restore.go` and `cluster_backup_gate.go` resolve targets from the
  manifest, drive services, and apply the gate.
- `ssh.StreamRunner.RunStream` carries stdin and stdout through `ssh` (and the local runner), so dumps and loads
  are never buffered in memory or staged on the database host.

## Stores

| Store                                     | Unit                      | Tooling                                         | Restore                                                    |
| ----------------------------------------- | ------------------------- | ----------------------------------------------- | ---------------------------------------------------------- |
| YugabyteDB service databases (production) | one physical database     | `ysql_dump --section=pre-data/data/post-data`   | `ysqlsh` into `<db>__restore`, rename swap                 |
| PostgreSQL service databases (small/dev)  | one physical database     | `pg_dump --section=…`                           | `psql` into `<db>__restore`, rename swap                   |
| Named postgres instances (`support`)      | one database per instance | `pg_dump --section=…`                           | same as PostgreSQL                                         |
| ClickHouse billing facts (`periscope`)    | eight fact tables         | `clickhouse-client SELECT <columns> FORMAT TSV` | load `<table>__restore`, `REPLACE PARTITION` into the live |

Object storage, Kafka, Valkey and SOPS secrets are out of scope. ClickHouse rollups, views, raw telemetry and
cursors are derived and not dumped.

## Data Flows

### Backup

```
per database:
  ReadDatabaseFacts (owner, encoding, locale, aclexplode grants)
  ledger before  ─┐
  pre-data  → StoreFile                                   ─┐
  data      → StoreCompressed + ParseDataSection (COPY row counts, _migrations rows)
  post-data → StoreFile                                   ─┘
  ledger after   ─┴─ both must equal the ledger parsed from the data section
clickhouse: ledger before; per table columns → dump (TSV line count = rows); ledger after (must match)
manifest.json written last (Validate: format, timestamps, digests, sections)
```

Row counts and the ledger digest come from the dump itself, so they describe the same snapshot as the data, not a
separate live read that concurrent writes would make wrong.

### Restore

```
ReadManifest → VerifyFiles(selected) → engine check → refuse existing <db>__prerestore
confirm
per database: CreateRestoreShadow (owner, locale, grants) → load sections (VerifyingReader)
              → CompareRestoredDatabase (ledger digest, per-table counts)
              → below-floor gap on the shadow → cell storage identity (Foghorn)
clickhouse:   per table CREATE TABLE <t>__restore AS <t> → INSERT TSV → count check
stop services (role cleanup) → swap: rename live → __prerestore, shadow → live
                             → ClickHouse: copy live partitions to __prerestore, REPLACE from shadow, fingerprint
start services (role restart + validate), always, even after a failed swap
report migrations that ran after the backup; `restore finish` drops copies, `restore rollback` swaps back
```

### Migration gate

```
cluster migrate --phase contract [--dry-run]    pending contract migrations → live ledger digest per database
cluster data-migrate run svc.id (not --dry-run)  irreversible? → live ledger digest per service database
  backup.RequireGate(--backup, digests, now):
    ReadManifest → CheckGate (age ≤ MaxAge = 1h from CreatedAt, clock not ahead by > 5m, same cluster
                              when both name one, every target key present with the same digest)
                 → VerifyFiles for the target databases
```

Irreversible means: the catalog release that declares the migration lists the service under `rollback_disabled`,
or the service binary's `data-migrations list --format json` marks it `irreversible`, lacks the field, or does not
list the migration. Commodore's field encryption and Purser's EUR ledger conversion are irreversible.

## Decisions

- **Dumps, not `yb-admin` snapshots.** Snapshots stay on the tablet servers and need an identical tablet layout to
  import; the colocation relayout changes that layout. Logical dumps are portable across layouts.
- **ClickHouse TabSeparated per table, not `BACKUP`.** `BACKUP` writes to a server-side disk that must be
  configured (`backups.allowed_path`), which the ClickHouse role does not render, and it does not stream. Per-table
  TSV streams over SSH, gives an exact row count (one line per row), and restores into a shadow with the live
  schema. The content fingerprint (`VerifyFingerprintSQL`) proves the swap moved exactly the shadow's rows.
- **Partition replacement, not `EXCHANGE TABLES`.** Replacing partitions keeps the live table's identity, so the
  materialized views that read it keep their source and are not fed the restored rows a second time.
- **The ClickHouse ledger is recorded, not restored.** Restore puts fact rows back into the live schema; restoring
  the ledger without the schema would make the two disagree.
- **Destinations.** `--to` names a parent; each backup gets a new `frameworks-backup-<UTC time>` directory, so a
  backup never overwrites another. S3 uses the AWS SDK default credential chain and `AWS_ENDPOINT_URL_S3`; a
  custom endpoint switches to path-style addressing, the convention Foghorn's S3 client uses for S3-compatible
  stores. Request checksums are sent only when required, which R2 and Hetzner accept.
- **No policy.** The CLI enforces no destination, off-site copy, encryption or retention. The only enforcement is
  the gate, and its one-hour limit is a code constant (`backup.MaxAge`).
- **`release apply` is not gated.** Only the contract phase and irreversible data migrations need a backup.
- **Stopping services.** Restore stops the services that own the restored databases through each role's
  `cleanup` tasks and starts them with the role's `restart` then `validate` tasks, the same Ansible paths
  `cluster restart` uses. For per-cell Foghorn databases only the owning cell stops.
- **Dump errors.** Exit codes fail every dump; `ysql_dump` additionally fails on any stderr output, because it
  reports some problems without a non-zero exit.

## Key Files

- `cli/pkg/backup/manifest.go` - manifest, `LedgerDigest`, `VerifyFiles`, `VerifyingReader`
- `cli/pkg/backup/location.go`, `s3.go`, `stream.go` - destinations and streaming store with inspection
- `cli/pkg/backup/evidence.go` - COPY-block row counts and ledger rows from a data section
- `cli/pkg/backup/gate.go` - `MaxAge`, `CheckGate`, `RequireGate`
- `cli/pkg/provisioner/database_backup.go` - PostgreSQL/YugabyteDB dump, restore, compare, swap, rollback, finish
- `cli/pkg/provisioner/clickhouse_backup.go` - ClickHouse fact tables
- `cli/cmd/cluster_backup.go`, `cluster_restore.go`, `cluster_backup_gate.go` - commands and gate wiring
- `cli/pkg/provisioner/database_backup_realdb_test.go` - real-engine round trips (`make verify-backup-restore-postgres`,
  `verify-backup-restore-clickhouse`, `verify-backup-restore-yugabyte`)

## Gotchas

- A restore makes each restored database unavailable from service stop to validation. There is no point-in-time
  or single-table restore.
- On YugabyteDB, `pg_stat_activity` shows only the local tserver. Restore ends local sessions and relies on the
  stopped services for the rest; a remaining remote session fails the rename and leaves the live database in
  place. The colocation relayout's fence (`requireNoSessions` across nodes) replaces this once it lands.
- ClickHouse fact rows written after the backup are lost on restore; periscope-ingest resumes from its committed
  Kafka offsets and does not replay them.
- Restoring ClickHouse is refused on a multi-node cluster.
- The gate compares ledger digests and, when both the manifest and the command's manifest source name a cluster,
  the cluster name. A backup of another cluster at the same migration state, taken through a source that names no
  cluster, would still match.
- `CREATE TABLE <t>__restore AS <t>` relies on `ReplicatedMergeTree()` taking its default `{uuid}` path, which the
  baseline uses.

## YugabyteDB colocation

The colocation branch (`../monorepo-yugabyte-colocation`) changes the physical layout. Its adaptation is confined
to `cli/pkg/provisioner/database_backup.go` and the target planner in `cli/cmd/cluster_backup.go`:

- `CreateRestoreShadow` creates the shadow without options on YugabyteDB. Under colocation the shadow must be
  created with the target layout (`COLOCATION = true` and the per-table colocation the relayout applies), which
  means the pre-data section must go through the relayout's rewrite (`buildCopySchema`) instead of a plain load.
- `PrepareRestoredDatabase` + `SwapRestoredDatabase` become the relayout's clone-from-dump primitive: fence
  (lease, revoke CONNECT, `requireNoSessions` across nodes), restore into the shadow, compare evidence and schema
  (`CollectEvidence`, `schemaDifferences`), rename swap with the pre-relayout copy kept. The manifest's per-table
  row counts map onto the relayout evidence.
- Restore must refuse while a relayout lease is held (`RelayoutsInProgress`); today there is no lease to check.
- `yugabyteBinaryResolver` duplicates the branch's `YugabyteBinaryResolverShell`; use the exported one.
- Dump commands address YSQL on `localhost`, as the migration ledger reader does in this tree; the relayout uses
  the node's YSQL address, which the colocated layout may require for both.
- If colocation merges databases, `planBackupTargets` enumerates physical databases from the new layout; the
  manifest key (`postgres/<physical name>`) and the gate's per-database digests stay as they are.
