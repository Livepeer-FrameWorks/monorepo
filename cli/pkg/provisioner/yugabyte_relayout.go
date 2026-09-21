package provisioner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// YugabyteNode runs SQL and shell scripts on one YugabyteDB tserver with local superuser access.
type YugabyteNode interface {
	Name() string
	// Query runs sql, which may contain psql meta-commands, against database with ON_ERROR_STOP set and returns the
	// quiet, unaligned, tuples-only output with pipe-separated columns.
	Query(ctx context.Context, database, sql string) (string, error)
	// Shell runs a POSIX shell script and returns its stdout. A non-zero exit is an error.
	Shell(ctx context.Context, script string) (string, error)
}

// YugabyteBinaryResolverShell selects tools from the published native release, or from an in-place/container
// installation not yet converted by the role. A broken published release must never fall back to older tools.
const YugabyteBinaryResolverShell = `fw_yb_bin() {
  if [ -e /opt/yugabyte/current ] || [ -L /opt/yugabyte/current ]; then
    for dir in /opt/yugabyte/current/postgres/bin /opt/yugabyte/current/bin; do
      if [ -x "$dir/$1" ]; then readlink -f "$dir/$1"; return 0; fi
    done
    echo "Selected YugabyteDB release lacks $1" >&2
    return 1
  fi
  for dir in /opt/yugabyte/postgres/bin /opt/yugabyte/bin /home/yugabyte/postgres/bin /home/yugabyte/bin; do
    if [ -x "$dir/$1" ]; then echo "$dir/$1"; return 0; fi
  done
  command -v "$1"
}`

// YugabyteMigrationLedgerDDL creates the migration ledger with the definition the yugabyte role's migrate tasks use.
const YugabyteMigrationLedgerDDL = `CREATE TABLE IF NOT EXISTS _migrations (
  version    TEXT NOT NULL,
  phase      TEXT NOT NULL DEFAULT 'expand',
  seq        INT NOT NULL,
  filename   TEXT NOT NULL,
  checksum   TEXT NOT NULL,
  transactional BOOLEAN NOT NULL DEFAULT true,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (version, phase, seq)
);`

// RelayoutState is one durable step of a database relayout, recorded in the journal in the admin database.
type RelayoutState string

const (
	RelayoutPlanned RelayoutState = "planned"
	// RelayoutPrepared means the shadow holds the source's complete schema in the declared layout and no data. It is
	// built while services run; the source is untouched until fence.
	RelayoutPrepared       RelayoutState = "prepared"
	RelayoutFenced         RelayoutState = "fenced"
	RelayoutDumped         RelayoutState = "dumped"
	RelayoutRestored       RelayoutState = "restored"
	RelayoutVerified       RelayoutState = "verified"
	RelayoutRenamedOld     RelayoutState = "renamed_old"
	RelayoutRenamedNew     RelayoutState = "renamed_new"
	RelayoutAccessRestored RelayoutState = "access_restored"
	RelayoutFinished       RelayoutState = "finished"
	RelayoutRolledBack     RelayoutState = "rolled_back"
)

const (
	relayoutAdminDatabase   = "yugabyte"
	relayoutShadowSuffix    = "__relayout"
	relayoutPreSuffix       = "__pre_relayout"
	relayoutPreflightSuffix = "__relayout_check"
	relayoutMinimumLeaseTTL = 3 * time.Second
	relayoutDefaultACL      = "default"
	relayoutDefaultLeaseTTL = 15 * time.Minute
	relayoutDefaultSettle   = 3 * time.Second
	// relayoutSessionWaitRounds bounds how many settle delays a session check waits for terminated backends to exit.
	relayoutSessionWaitRounds = 20
	// relayoutSessionDrainTimeout bounds how long terminated backends may take to exit before a session proof fails.
	relayoutSessionDrainTimeout = 2 * time.Minute
	// relayoutApplicationName prefixes the application name of every connection a relayout opens, so session
	// diagnostics tell them apart; RelayoutApplicationName adds the invocation's lease owner.
	relayoutApplicationName = "frameworks-relayout"
)

// The journal lives in the admin database so database renames can never orphan it.
const relayoutJournalDDL = `CREATE TABLE IF NOT EXISTS public.frameworks_relayout_journal (
    database_name    TEXT PRIMARY KEY,
    state            TEXT NOT NULL,
    lease_owner      TEXT,
    lease_expires_at TIMESTAMPTZ,
    source_acl       TEXT,
    dump_path        TEXT,
    dump_sha256      TEXT,
    dump_host        TEXT,
    receipt          JSONB,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE public.frameworks_relayout_journal ADD COLUMN IF NOT EXISTS dump_host TEXT;
ALTER TABLE public.frameworks_relayout_journal ADD COLUMN IF NOT EXISTS source_oid TEXT;
ALTER TABLE public.frameworks_relayout_journal ADD COLUMN IF NOT EXISTS source_comment TEXT;
ALTER TABLE public.frameworks_relayout_journal ADD COLUMN IF NOT EXISTS pending_copy TEXT`

// relayoutRuntimeLedgers are tables no baseline declares. The migrate role creates _migrations in public. Each
// service's data-migration runner creates _data_migrations and _data_migration_runs with unqualified names through
// its own connection, so they land in the first schema of the role's search_path ("$user", public): the service
// schema when one is named after the role, public otherwise. A relayout places them colocated in whichever schema
// holds them; any other table outside the layout blocks it.
var relayoutRuntimeLedgers = []string{"_migrations", "_data_migrations", "_data_migration_runs"}

// isRelayoutRuntimeTable reports whether a schema-qualified table is one of the runtime ledgers.
func isRelayoutRuntimeTable(table string) bool {
	name := table
	if _, unqualified, ok := strings.Cut(table, "."); ok {
		name = unqualified
	}
	return slices.Contains(relayoutRuntimeLedgers, name)
}

// relayoutSchemaSections build the shadow's complete schema before the fence: pre-data creates tables with their
// primary keys, and post-data builds indexes, adds constraints, and creates triggers. YugabyteDB spends most of a copy
// here, on schema changes every tserver must pick up, so none of it happens while services are fenced.
var relayoutSchemaSections = []string{"pre-data", "post-data"}

// relayoutDataSections are dumped and loaded while the source is fenced, into the shadow that already has its schema.
var relayoutDataSections = []string{"data"}

// relayoutReplicaRoleOptions load data with session_replication_role set to replica, which skips user triggers and
// foreign-key checks for that session only, without the per-table DDL of disabling triggers. The source is consistent
// and fenced, so its rows already satisfy every constraint, and trigger-fed tables are copied rather than refilled.
const relayoutReplicaRoleOptions = "-c session_replication_role=replica"

// relayoutPrivilegeRows lists the effective privileges on every schema, relation, column, routine, and user-defined
// type: kind, schema, object, grantee, privilege, and whether it carries the grant option. The grantor is left out on
// purpose. A dump restores each grant through SET SESSION AUTHORIZATION to the recorded grantor, but PostgreSQL
// records a grant a superuser issues on another role's object as made by the owner, so an object created by the
// migrate role and later handed to the service role comes back with the owner as grantor. Who holds which privilege,
// and with which grant option, is what a copy must preserve. A NULL ACL stands for its type's default.
const relayoutPrivilegeRows = `SELECT kind, schema_name, object_name,
       CASE WHEN grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(grantee) END AS grantee_name, privilege_type, bool_or(is_grantable) AS grantable
  FROM (
    SELECT 'schema' AS kind, n.nspname AS schema_name, n.nspname AS object_name, a.grantee, a.privilege_type, a.is_grantable
      FROM pg_namespace n, aclexplode(coalesce(n.nspacl, acldefault('n', n.nspowner))) a
     WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%'
    UNION ALL
    SELECT 'relation', n.nspname, c.relname, a.grantee, a.privilege_type, a.is_grantable
      FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace,
           aclexplode(coalesce(c.relacl, acldefault((CASE WHEN c.relkind = 'S' THEN 's' ELSE 'r' END)::"char", c.relowner))) a
     WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f') AND n.nspname NOT IN ('pg_catalog', 'information_schema')
    UNION ALL
    SELECT 'column', n.nspname, c.relname || '.' || at.attname, a.grantee, a.privilege_type, a.is_grantable
      FROM pg_attribute at JOIN pg_class c ON c.oid = at.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace,
           aclexplode(at.attacl) a
     WHERE at.attacl IS NOT NULL AND at.attnum > 0 AND NOT at.attisdropped AND n.nspname NOT IN ('pg_catalog', 'information_schema')
    UNION ALL
    SELECT 'routine', n.nspname, p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', a.grantee, a.privilege_type, a.is_grantable
      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace, aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
     WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
    UNION ALL
    SELECT 'type', n.nspname, t.typname, a.grantee, a.privilege_type, a.is_grantable
      FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace, aclexplode(coalesce(t.typacl, acldefault('T', t.typowner))) a
     WHERE t.typtype IN ('e', 'd', 'r', 'm') AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  ) privileges
 GROUP BY kind, schema_name, object_name, grantee, privilege_type`

// relayoutPrivilegeQuery prints relayoutPrivilegeRows as introspection lines.
const relayoutPrivilegeQuery = `SELECT 'priv|' || kind || '|' || schema_name || '|' || object_name || '|' || grantee_name || '|' || privilege_type || '|' || grantable::text
  FROM (` + relayoutPrivilegeRows + `) p`

// relayoutGrantLinePrefixes are the introspection lines that name a grantor; a relayout compares relayoutPrivilegeQuery
// lines in their place.
var relayoutGrantLinePrefixes = []string{"table-grant|", "routine-grant|", "usage-grant|"}

// relayoutEvidenceSQL tags every row with its evidence kind. Table checksums sum two 60-bit slices of each row's md5,
// computed over its jsonb form so physical column order does not matter; the sums are order independent and use
// constant memory on large tables. Attribute rows cover what the schema introspection leaves out: schema owners,
// effective privileges on every object, identity and generated columns, sequence ownership, and the structure and
// owners of the runtime ledger tables. Values have '|' escaped so each row splits into exactly three fields.
const relayoutEvidenceSQL = `SELECT 'colocated', yb_is_database_colocated();
SELECT 'attribute', 'schema:' || nspname, replace(pg_get_userbyid(nspowner), '|', '%7C')
  FROM pg_namespace
 WHERE nspname NOT IN ('pg_catalog', 'information_schema') AND nspname NOT LIKE 'pg_toast%' AND nspname NOT LIKE 'pg_temp%'
 ORDER BY 2;
SELECT 'attribute', replace('privilege:' || kind || ':' || schema_name || '.' || object_name || ':' || grantee_name || ':' || privilege_type, '|', '%7C'), grantable::text
  FROM (` + relayoutPrivilegeRows + `) p
 ORDER BY 2;
SELECT 'attribute', 'column_generation:' || n.nspname || '.' || c.relname || '.' || a.attname, 'identity=' || a.attidentity::text || ' generated=' || a.attgenerated::text
  FROM pg_attribute a
  JOIN pg_class c ON c.oid = a.attrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE (a.attidentity <> '' OR a.attgenerated <> '') AND a.attnum > 0 AND NOT a.attisdropped AND n.nspname NOT IN ('pg_catalog', 'information_schema')
 ORDER BY 2;
SELECT 'attribute', 'sequence_owner:' || sn.nspname || '.' || s.relname, d.deptype::text || ' ' || tn.nspname || '.' || t.relname || '.' || a.attname
  FROM pg_depend d
  JOIN pg_class s ON s.oid = d.objid AND s.relkind = 'S'
  JOIN pg_namespace sn ON sn.oid = s.relnamespace
  JOIN pg_class t ON t.oid = d.refobjid
  JOIN pg_namespace tn ON tn.oid = t.relnamespace
  JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = d.refobjsubid
 WHERE d.classid = 'pg_class'::regclass AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
 ORDER BY 2;
SELECT 'attribute', 'runtime_column:' || c.table_schema || '.' || c.table_name || '.' || c.column_name,
       replace(c.data_type || ' nullable=' || c.is_nullable || ' default=' || coalesce(c.column_default, ''), '|', '%7C')
  FROM information_schema.columns c
 WHERE c.table_schema NOT IN ('pg_catalog', 'information_schema') AND c.table_name IN ('_migrations', '_schema_baseline', '_data_migrations', '_data_migration_runs')
 ORDER BY 2;
SELECT 'attribute', 'runtime_owner:' || schemaname || '.' || tablename, tableowner
  FROM pg_tables
 WHERE schemaname NOT IN ('pg_catalog', 'information_schema') AND tablename IN ('_migrations', '_schema_baseline', '_data_migrations', '_data_migration_runs')
 ORDER BY 2;
SELECT 'internal_triggers_disabled', count(*) FROM pg_trigger WHERE tgisinternal AND tgenabled <> 'O';
SELECT 'trigger', n.nspname || '.' || c.relname || '.' || t.tgname, t.tgenabled::text
  FROM pg_trigger t
  JOIN pg_class c ON c.oid = t.tgrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE NOT t.tgisinternal AND n.nspname NOT IN ('pg_catalog', 'information_schema')
 ORDER BY 2;
SELECT 'constraint', n.nspname || '.' || c.relname || '.' || con.conname, con.convalidated
  FROM pg_constraint con
  JOIN pg_class c ON c.oid = con.conrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
   AND NOT (con.contype = 'c' AND con.conname LIKE '%_not_null')
 ORDER BY 2;
SELECT format('SELECT %L, %L, last_value, is_called FROM %I.%I', 'sequence', schemaname || '.' || sequencename, schemaname, sequencename)
  FROM pg_sequences
 WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
 ORDER BY 1
\gexec
SELECT format('SELECT %L, %L, count(*), coalesce(sum((''x'' || substr(md5(to_jsonb(t)::text), 1, 15))::bit(60)::bigint::numeric), 0)::text || '':'' || coalesce(sum((''x'' || substr(md5(to_jsonb(t)::text), 16, 15))::bit(60)::bigint::numeric), 0)::text FROM %I.%I t',
              'table', schemaname || '.' || tablename, schemaname, tablename)
  FROM pg_tables
 WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
 ORDER BY schemaname, tablename
\gexec
`

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// YugabyteRelayout moves one existing database into its declared layout by restoring a staged dump into a shadow
// database created with that layout and renaming it into place. Every step records its effect in a leased journal,
// so separate invocations resume from observed state instead of re-deriving it.
type YugabyteRelayout struct {
	Primary YugabyteNode
	// Nodes are every tserver. Sessions are local to a tserver, so fencing terminates and counts them on each node.
	Nodes    []YugabyteNode
	YSQLHost string
	YSQLPort int
	// Database is the physical database name, such as quartermaster or foghorn_eu.
	Database string
	// Roles are revoked from the database while it is fenced, in addition to every grantee in its recorded ACL.
	Roles       []string
	DumpDir     string
	LeaseOwner  string
	LeaseTTL    time.Duration
	SettleDelay time.Duration
}

// RelayoutRecord is the journal row for one database.
type RelayoutRecord struct {
	Database       string
	State          RelayoutState
	LeaseOwner     string
	LeaseExpiresAt string
	SourceACL      string
	// SourceOID identifies the original database across its rename to the pre-relayout name; rollback and finish
	// only rename or drop a database that still has it.
	SourceOID string
	// SourceComment is the original database comment, which a dump without --create does not carry.
	SourceComment string
	DumpPath      string
	DumpSHA256    string
	// DumpHost is the node holding the staged dump; only that node can read or remove it.
	DumpHost string
	// PendingCopy names a copy database this relayout is creating. It is recorded before CREATE DATABASE, so an empty
	// database under that name that lacks its marker can be recognised as an interrupted create.
	PendingCopy string
	Receipt     *RelayoutReceipt
}

// RelayoutEvidence is the data-integrity fingerprint of one database.
type RelayoutEvidence struct {
	Database     string `json:"database"`
	Colocated    bool   `json:"colocated"`
	SchemaDigest string `json:"schema_digest"`
	// Tables maps schema.table to "rows|checksum".
	Tables map[string]string `json:"tables"`
	// Sequences maps schema.sequence to "last_value|is_called".
	Sequences map[string]string `json:"sequences"`
	// Triggers maps schema.table.trigger to pg_trigger.tgenabled for user triggers.
	Triggers                 map[string]string `json:"triggers"`
	InternalTriggersDisabled int               `json:"internal_triggers_disabled"`
	// Constraints maps schema.table.constraint to pg_constraint.convalidated.
	Constraints map[string]string `json:"constraints"`
	// Attributes maps kind:name to schema owners and privileges, column privileges, identity and generated columns,
	// sequence ownership, and runtime ledger table structure and owners.
	Attributes map[string]string `json:"attributes"`
}

// RelayoutReceipt is the persisted proof that a shadow database is a faithful copy of its source in the declared
// layout. Cutover recomputes it and refuses to rename when anything changed.
type RelayoutReceipt struct {
	Database         string           `json:"database"`
	Shadow           string           `json:"shadow"`
	DumpSHA256       string           `json:"dump_sha256"`
	LayoutSHA256     string           `json:"layout_sha256"`
	Source           RelayoutEvidence `json:"source"`
	ShadowEvidence   RelayoutEvidence `json:"shadow_evidence"`
	PlacementDrift   []string         `json:"placement_drift"`
	CapabilityProbes int              `json:"capability_probes"`
	ProbeFailures    []string         `json:"probe_failures"`
	VerifiedAt       time.Time        `json:"verified_at"`
}

// ShadowName is the database the relayout restores into.
func (r *YugabyteRelayout) ShadowName() string { return r.Database + relayoutShadowSuffix }

// PreRelayoutName is the name the original database keeps after cutover until finish drops it.
func (r *YugabyteRelayout) PreRelayoutName() string { return r.Database + relayoutPreSuffix }

// PreflightName is the scratch database a preflight rehearsal builds and drops.
func (r *YugabyteRelayout) PreflightName() string { return r.Database + relayoutPreflightSuffix }

func (r *YugabyteRelayout) leaseTTL() time.Duration {
	if r.LeaseTTL <= 0 {
		return relayoutDefaultLeaseTTL
	}
	return r.LeaseTTL
}

func (r *YugabyteRelayout) validate() error {
	switch {
	case r.Primary == nil:
		return errors.New("relayout requires a primary YugabyteDB node")
	case !layoutDatabaseNamePattern.MatchString(r.Database):
		return fmt.Errorf("relayout database %q must be a lowercase identifier", r.Database)
	case strings.HasSuffix(r.Database, relayoutShadowSuffix) || strings.HasSuffix(r.Database, relayoutPreSuffix):
		return fmt.Errorf("relayout database %q is itself a relayout artifact", r.Database)
	case len(r.PreflightName()) > 63:
		return fmt.Errorf("relayout database %q is too long for its %s name", r.Database, relayoutPreflightSuffix)
	case strings.TrimSpace(r.LeaseOwner) == "":
		return errors.New("relayout requires a lease owner")
	case r.LeaseTTL != 0 && r.LeaseTTL < relayoutMinimumLeaseTTL:
		return fmt.Errorf("relayout lease TTL %s is shorter than %s", r.LeaseTTL, relayoutMinimumLeaseTTL)
	case strings.TrimSpace(r.YSQLHost) == "" || r.YSQLPort <= 0:
		return errors.New("relayout requires the local YSQL host and port")
	case strings.TrimSpace(r.DumpDir) == "" || !path.IsAbs(r.DumpDir):
		return fmt.Errorf("relayout dump directory %q must be an absolute path", r.DumpDir)
	}
	return nil
}

func (r *YugabyteRelayout) nodes() []YugabyteNode {
	if len(r.Nodes) > 0 {
		return r.Nodes
	}
	return []YugabyteNode{r.Primary}
}

// onNodes runs fn on every node at once and returns each node's output in node order, or every node's error joined in
// node order. Each check inside the fenced window reaches every tserver over its own SSH connection, so running them
// together keeps the window from growing with the node count.
func (r *YugabyteRelayout) onNodes(fn func(YugabyteNode) (string, error)) ([]string, error) {
	nodes := r.nodes()
	outputs := make([]string, len(nodes))
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Go(func() { outputs[i], errs[i] = fn(node) })
	}
	wg.Wait()
	return outputs, errors.Join(errs...)
}

func (r *YugabyteRelayout) admin(ctx context.Context, sql string) (string, error) {
	out, err := r.Primary.Query(ctx, relayoutAdminDatabase, sql)
	if err != nil {
		return out, fmt.Errorf("%s: %w", r.Primary.Name(), err)
	}
	return strings.TrimSpace(out), nil
}

func relayoutLiteral(value string) string { return pq.QuoteLiteral(value) }

func relayoutIdentifier(value string) string { return pq.QuoteIdentifier(value) }

func relayoutBase64Column(expression string) string {
	return fmt.Sprintf("translate(encode(convert_to(coalesce(%s, ''), 'UTF8'), 'base64'), E'\\n', '')", expression)
}

// RelayoutApplicationName is the application name every connection and client process of one relayout invocation
// carries, derived from its lease owner, so the work of an invocation that died can be found and ended on every
// tserver. It fits PostgreSQL's 63-byte limit.
func RelayoutApplicationName(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return relayoutApplicationName + "-" + hex.EncodeToString(sum[:6])
}

func (r *YugabyteRelayout) applicationName() string { return RelayoutApplicationName(r.LeaseOwner) }

// Acquire creates the journal row when needed and takes the relayout lease, which expires so a crashed invocation
// cannot block the database forever. Taking over an expired lease first ends the previous owner's remote work.
func (r *YugabyteRelayout) Acquire(ctx context.Context) (*RelayoutRecord, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	holder, err := r.admin(ctx, fmt.Sprintf(`%s;
INSERT INTO public.frameworks_relayout_journal (database_name, state) VALUES (%s, %s) ON CONFLICT (database_name) DO NOTHING;
SELECT coalesce(lease_owner, '') || '|' || coalesce((lease_expires_at < now())::text, 'true')
  FROM public.frameworks_relayout_journal WHERE database_name = %s;`,
		relayoutJournalDDL, relayoutLiteral(r.Database), relayoutLiteral(string(RelayoutPlanned)), relayoutLiteral(r.Database)))
	if err != nil {
		return nil, fmt.Errorf("acquire relayout lease for %s: %w", r.Database, err)
	}
	owner, expired, _ := strings.Cut(holder, "|")
	switch {
	case owner == "" || owner == r.LeaseOwner:
		return r.claimLease(ctx, "lease_owner IS NULL OR lease_owner = "+relayoutLiteral(r.LeaseOwner))
	case expired == "true":
		return r.takeLeaseFrom(ctx, owner, "lease_expires_at < now()")
	}
	record, err := r.Record(ctx)
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("relayout of %s is leased by %s until %s", r.Database, record.LeaseOwner, record.LeaseExpiresAt)
}

// TakeOver moves an unexpired lease from staleOwner to this owner. Callers use it only once they have proved the stale
// owner's local process no longer exists. That process may have left remote work running, such as a restore or a
// dump on a tserver, so its sessions are ended first. The update matches the stale owner exactly, so a lease another
// invocation took in the meantime is left alone.
func (r *YugabyteRelayout) TakeOver(ctx context.Context, staleOwner string) (*RelayoutRecord, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	return r.takeLeaseFrom(ctx, staleOwner, "true")
}

// takeLeaseFrom ends every session the previous owner's invocation still has on a live tserver, proves none remains,
// and only then moves the lease, provided previous still holds it and condition still holds. A lost process cannot
// issue new work, but a client it started on a tserver keeps running after its SSH connection drops; every such client
// carries the owner's application name. Until the move succeeds the journal keeps naming the previous owner, so a
// failed attempt is retried against the same one.
func (r *YugabyteRelayout) takeLeaseFrom(ctx context.Context, previous, condition string) (*RelayoutRecord, error) {
	if err := r.fenceOwner(ctx, previous); err != nil {
		return nil, fmt.Errorf("%w\nthe lease of %s stays with %s until its work is gone", err, r.Database, previous)
	}
	return r.claimLease(ctx, fmt.Sprintf("lease_owner = %s AND (%s)", relayoutLiteral(previous), condition))
}

// fenceOwner ends everything owner's invocation still runs. It revokes owner and stops the scripts registered under its
// application name on every host that can run them, whether or not that host's tserver is live, because a script
// such as a restore checksumming its dump outlives a stopped tserver and would connect once YSQL returns. A host that
// cannot be reached fails the fence. Only then does it end owner's database sessions, which exist only on live
// tservers, and prove none remains. Revocation keeps any script of owner that starts later from running its body.
func (r *YugabyteRelayout) fenceOwner(ctx context.Context, owner string) error {
	application := RelayoutApplicationName(owner)
	if err := r.stopWorkers(ctx, application); err != nil {
		return err
	}
	live, err := r.liveNodes(ctx)
	if err != nil {
		return err
	}
	sessions := *r
	sessions.Nodes = live
	if err = sessions.endSessionsWhere(ctx, "application_name = "+relayoutLiteral(application)); err != nil {
		return err
	}
	return sessions.requireNoSessionsWhere(ctx, fmt.Sprintf("relayout owner %s", owner), "application_name = "+relayoutLiteral(application))
}

// stopWorkers revokes application and stops, on every node, the scripts registered under it, proving none remains.
func (r *YugabyteRelayout) stopWorkers(ctx context.Context, application string) error {
	outputs, err := r.onNodes(func(node YugabyteNode) (string, error) {
		out, err := node.Shell(ctx, relayoutStopWorkersScript(application))
		if err != nil {
			return "", fmt.Errorf("stop relayout workers on %s: %w", node.Name(), err)
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	for i, out := range outputs {
		remaining := ""
		for line := range strings.SplitSeq(out, "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "remaining="); ok {
				remaining = value
			}
		}
		if remaining != "0" {
			return fmt.Errorf("relayout workers of %s still run on %s after being stopped (remaining=%q)", application, r.nodes()[i].Name(), remaining)
		}
	}
	return nil
}

// ReleaseAfterFailure gives up the lease after a step failed or was cancelled. Such a step can leave its scripts
// running on a host, so successorOwner takes the lease over first, exactly as a later invocation would: it revokes
// this owner, stops its scripts, and ends its sessions. Only then is the lease released. successorOwner must be a
// fresh owner in the same form as LeaseOwner, so that a crash between the takeover and the release leaves a lease a
// later invocation recognizes and takes over in turn. When the takeover cannot prove the work gone, the lease keeps
// naming this owner and the next invocation's takeover fences it instead.
func (r *YugabyteRelayout) ReleaseAfterFailure(ctx context.Context, successorOwner string) error {
	if successorOwner == "" || successorOwner == r.LeaseOwner {
		return fmt.Errorf("releasing the lease of %s after a failure needs a fresh successor owner", r.Database)
	}
	successor := r.as(successorOwner)
	if _, err := successor.TakeOver(ctx, r.LeaseOwner); err != nil {
		return fmt.Errorf("%w\nthe lease of %s stays with %s so the next invocation ends that work before acting", err, r.Database, r.LeaseOwner)
	}
	return successor.Release(ctx)
}

// applicationNamedNode is a node whose connections and scripts can carry another relayout identity.
type applicationNamedNode interface {
	WithApplicationName(name string) YugabyteNode
}

// as returns this relayout acting as owner: its nodes carry owner's application name where they support one.
func (r *YugabyteRelayout) as(owner string) *YugabyteRelayout {
	next := *r
	next.LeaseOwner = owner
	rename := func(node YugabyteNode) YugabyteNode {
		if named, ok := node.(applicationNamedNode); ok {
			return named.WithApplicationName(RelayoutApplicationName(owner))
		}
		return node
	}
	next.Nodes = nil
	next.Primary = nil
	for _, node := range r.Nodes {
		renamed := rename(node)
		next.Nodes = append(next.Nodes, renamed)
		if node == r.Primary {
			next.Primary = renamed
		}
	}
	if next.Primary == nil {
		next.Primary = rename(r.Primary)
	}
	return &next
}

// claimLease sets this owner on the journal row when condition holds.
func (r *YugabyteRelayout) claimLease(ctx context.Context, condition string) (*RelayoutRecord, error) {
	out, err := r.admin(ctx, fmt.Sprintf(`UPDATE public.frameworks_relayout_journal
   SET lease_owner = %s, lease_expires_at = now() + make_interval(secs => %d), updated_at = now()
 WHERE database_name = %s AND (%s)
RETURNING database_name`, relayoutLiteral(r.LeaseOwner), int(r.leaseTTL().Seconds()), relayoutLiteral(r.Database), condition))
	if err != nil {
		return nil, fmt.Errorf("acquire relayout lease for %s: %w", r.Database, err)
	}
	record, err := r.Record(ctx)
	if err != nil {
		return nil, err
	}
	if out != r.Database {
		return nil, fmt.Errorf("relayout of %s is leased by %s until %s", r.Database, record.LeaseOwner, record.LeaseExpiresAt)
	}
	return record, nil
}

var errRelayoutLeaseLost = errors.New("the lease expired or another owner holds it")

// Hold keeps the lease alive while a step runs by renewing it every sixth of its TTL. A renewal that cannot reach the
// database, such as during an SSH interruption, is retried. The returned context is cancelled with the cause when the
// journal shows the lease is no longer ours, or when the lease nears expiry without a successful renewal: a timer
// independent of the renewal attempts fires one renewal interval before the earliest moment the lease can lapse (the
// start of the last successful renewal plus the TTL), so no attempt stuck in flight can keep the step running past
// it. Each attempt is also cut off at that deadline. The returned stop function must be called when the step ends.
func (r *YugabyteRelayout) Hold(ctx context.Context) (context.Context, func()) {
	holdCtx, cancel := context.WithCancelCause(ctx)
	stop := make(chan struct{})
	finished := make(chan struct{})
	ttl := r.leaseTTL()
	interval := ttl / 6
	var mu sync.Mutex
	lastRenewal := time.Now()
	deadline := func() time.Time { return lastRenewal.Add(ttl - interval) }
	var expiry *time.Timer
	arm := func() {
		expiry = time.AfterFunc(time.Until(deadline()), func() {
			mu.Lock()
			since := time.Since(lastRenewal)
			mu.Unlock()
			cancel(fmt.Errorf("relayout lease for %s not renewed for %s; stopping before it can lapse", r.Database, since.Round(time.Millisecond)))
		})
	}
	mu.Lock()
	arm()
	mu.Unlock()
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-holdCtx.Done():
				return
			case <-ticker.C:
			}
			started := time.Now()
			mu.Lock()
			attemptDeadline := started.Add(2 * interval)
			if leaseDeadline := deadline(); leaseDeadline.Before(attemptDeadline) {
				attemptDeadline = leaseDeadline
			}
			mu.Unlock()
			renewCtx, cancelRenew := context.WithDeadline(holdCtx, attemptDeadline)
			err := r.renew(renewCtx)
			cancelRenew()
			switch {
			case err == nil:
				mu.Lock()
				if expiry.Stop() {
					lastRenewal = started
					arm()
				}
				mu.Unlock()
			case errors.Is(err, errRelayoutLeaseLost):
				cancel(fmt.Errorf("relayout lease for %s lost: %w", r.Database, err))
				return
			case holdCtx.Err() != nil:
				return
			}
		}
	}()
	return holdCtx, func() {
		close(stop)
		<-finished
		mu.Lock()
		expiry.Stop()
		mu.Unlock()
		cancel(nil)
	}
}

func (r *YugabyteRelayout) renew(ctx context.Context) error {
	out, err := r.admin(ctx, fmt.Sprintf(`UPDATE public.frameworks_relayout_journal
   SET lease_expires_at = now() + make_interval(secs => %d), updated_at = now()
 WHERE database_name = %s AND lease_owner = %s AND lease_expires_at > now()
RETURNING database_name`, int(r.leaseTTL().Seconds()), relayoutLiteral(r.Database), relayoutLiteral(r.LeaseOwner)))
	if err != nil {
		return err
	}
	if out != r.Database {
		return errRelayoutLeaseLost
	}
	return nil
}

// RelayoutsInProgress lists databases whose relayout has prepared or fenced them and neither finished nor rolled
// back, with their state. From prepare on, the shadow's schema must keep matching the source; later the canonical name
// may be missing, renamed, or held by a copy under construction, and a superuser connection (migrations, provisioning)
// is not fenced. Anything that creates or migrates service databases must wait for it.
func RelayoutsInProgress(ctx context.Context, node YugabyteNode) ([]string, error) {
	exists, err := node.Query(ctx, relayoutAdminDatabase, "SELECT to_regclass('public.frameworks_relayout_journal') IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("read relayout journal: %w", err)
	}
	if strings.TrimSpace(exists) != "t" {
		return nil, nil
	}
	out, err := node.Query(ctx, relayoutAdminDatabase, fmt.Sprintf(`SELECT database_name || ' (' || state || ')' FROM public.frameworks_relayout_journal
 WHERE state NOT IN (%s, %s, %s) OR source_oid IS NOT NULL ORDER BY 1`,
		relayoutLiteral(string(RelayoutPlanned)), relayoutLiteral(string(RelayoutRolledBack)), relayoutLiteral(string(RelayoutFinished))))
	if err != nil {
		return nil, fmt.Errorf("read relayout journal: %w", err)
	}
	var inProgress []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			inProgress = append(inProgress, line)
		}
	}
	return inProgress, nil
}

// requireLease renews the lease synchronously right before a statement that renames, drops, creates, revokes, or
// grants. Hold renews in the background, but a process paused past its lease (a suspended laptop, a stopped VM) could
// otherwise issue its next statement before Hold notices another owner took over.
func (r *YugabyteRelayout) requireLease(ctx context.Context) error {
	if err := r.renew(ctx); err != nil {
		if errors.Is(err, errRelayoutLeaseLost) {
			return fmt.Errorf("relayout lease for %s lost before a destructive step: %w", r.Database, err)
		}
		return fmt.Errorf("renew relayout lease for %s: %w", r.Database, err)
	}
	return nil
}

// notePendingCopy records, under this owner's live lease, the copy database about to be created, or clears it.
func (r *YugabyteRelayout) notePendingCopy(ctx context.Context, name string) error {
	value := "NULL"
	if name != "" {
		value = relayoutLiteral(name)
	}
	out, err := r.admin(ctx, fmt.Sprintf(`UPDATE public.frameworks_relayout_journal SET pending_copy = %s, updated_at = now()
 WHERE database_name = %s AND lease_owner = %s AND lease_expires_at > now()
RETURNING database_name`, value, relayoutLiteral(r.Database), relayoutLiteral(r.LeaseOwner)))
	if err != nil {
		return fmt.Errorf("record pending copy for %s: %w", r.Database, err)
	}
	if out != r.Database {
		return fmt.Errorf("record pending copy for %s: %w", r.Database, errRelayoutLeaseLost)
	}
	return nil
}

// Release gives up the lease held by this owner.
func (r *YugabyteRelayout) Release(ctx context.Context) error {
	_, err := r.admin(ctx, fmt.Sprintf(`UPDATE public.frameworks_relayout_journal SET lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
 WHERE database_name = %s AND lease_owner = %s`, relayoutLiteral(r.Database), relayoutLiteral(r.LeaseOwner)))
	return err
}

// Record reads the journal row, returning nil when no relayout was ever started for the database.
func (r *YugabyteRelayout) Record(ctx context.Context) (*RelayoutRecord, error) {
	exists, err := r.admin(ctx, "SELECT to_regclass('public.frameworks_relayout_journal') IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("read relayout journal: %w", err)
	}
	if exists != "t" {
		return nil, nil
	}
	columns := []string{"state", "lease_owner", "lease_expires_at::text", "source_acl", "dump_path", "dump_sha256", "dump_host", "source_oid", "source_comment", "pending_copy", "receipt::text"}
	encoded := make([]string, len(columns))
	for i, column := range columns {
		encoded[i] = relayoutBase64Column(column)
	}
	out, err := r.admin(ctx, fmt.Sprintf("SELECT %s FROM public.frameworks_relayout_journal WHERE database_name = %s",
		strings.Join(encoded, ", "), relayoutLiteral(r.Database)))
	if err != nil {
		return nil, fmt.Errorf("read relayout journal: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	fields := strings.Split(out, "|")
	if len(fields) != len(columns) {
		return nil, fmt.Errorf("relayout journal row for %s has %d fields, want %d", r.Database, len(fields), len(columns))
	}
	values := make([]string, len(fields))
	for i, field := range fields {
		decoded, decodeErr := base64.StdEncoding.DecodeString(field)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode relayout journal %s: %w", columns[i], decodeErr)
		}
		values[i] = string(decoded)
	}
	record := &RelayoutRecord{
		Database: r.Database, State: RelayoutState(values[0]), LeaseOwner: values[1], LeaseExpiresAt: values[2],
		SourceACL: values[3], DumpPath: values[4], DumpSHA256: values[5], DumpHost: values[6],
		SourceOID: values[7], SourceComment: values[8], PendingCopy: values[9],
	}
	if values[10] != "" {
		var receipt RelayoutReceipt
		if err = json.Unmarshal([]byte(values[10]), &receipt); err != nil {
			return nil, fmt.Errorf("decode relayout receipt for %s: %w", r.Database, err)
		}
		record.Receipt = &receipt
	}
	return record, nil
}

// advance moves the journal to a new state only while this owner holds a live lease and the row is in one of the
// expected states.
func (r *YugabyteRelayout) advance(ctx context.Context, from []RelayoutState, to RelayoutState, set map[string]string) error {
	assignments := []string{"state = " + relayoutLiteral(string(to)), "updated_at = now()"}
	keys := make([]string, 0, len(set))
	for column := range set {
		keys = append(keys, column)
	}
	sort.Strings(keys)
	for _, column := range keys {
		assignments = append(assignments, column+" = "+set[column])
	}
	states := make([]string, len(from))
	for i, state := range from {
		states[i] = relayoutLiteral(string(state))
	}
	out, err := r.admin(ctx, fmt.Sprintf(`UPDATE public.frameworks_relayout_journal SET %s
 WHERE database_name = %s AND lease_owner = %s AND lease_expires_at > now() AND state IN (%s)
RETURNING state`, strings.Join(assignments, ", "), relayoutLiteral(r.Database), relayoutLiteral(r.LeaseOwner), strings.Join(states, ", ")))
	if err != nil {
		return fmt.Errorf("record relayout state %s: %w", to, err)
	}
	if out != string(to) {
		return fmt.Errorf("relayout journal for %s is not in %v under this lease; refusing to record %s", r.Database, from, to)
	}
	return nil
}

// record updates journal columns without changing the state, under this owner's live lease. Fence writes the source
// identity with it before revoking access, so a crash between revocation and the state change still leaves the
// original ACL, OID, and comment recorded.
func (r *YugabyteRelayout) record(ctx context.Context, from []RelayoutState, set map[string]string) error {
	assignments := []string{"updated_at = now()"}
	keys := make([]string, 0, len(set))
	for column := range set {
		keys = append(keys, column)
	}
	sort.Strings(keys)
	for _, column := range keys {
		assignments = append(assignments, column+" = "+set[column])
	}
	states := make([]string, len(from))
	for i, state := range from {
		states[i] = relayoutLiteral(string(state))
	}
	out, err := r.admin(ctx, fmt.Sprintf(`UPDATE public.frameworks_relayout_journal SET %s
 WHERE database_name = %s AND lease_owner = %s AND lease_expires_at > now() AND state IN (%s)
RETURNING database_name`, strings.Join(assignments, ", "), relayoutLiteral(r.Database), relayoutLiteral(r.LeaseOwner), strings.Join(states, ", ")))
	if err != nil {
		return fmt.Errorf("record relayout journal for %s: %w", r.Database, err)
	}
	if out != r.Database {
		return fmt.Errorf("relayout journal for %s is not in %v under this lease; refusing to record the source identity", r.Database, from)
	}
	return nil
}

func (r *YugabyteRelayout) requireState(record *RelayoutRecord, allowed ...RelayoutState) error {
	if record == nil {
		return fmt.Errorf("no relayout journal for %s", r.Database)
	}
	if !slices.Contains(allowed, record.State) {
		return fmt.Errorf("relayout of %s is %s; this step requires %v", r.Database, record.State, allowed)
	}
	return nil
}

func (r *YugabyteRelayout) databaseExists(ctx context.Context, name string) (bool, error) {
	out, err := r.admin(ctx, "SELECT count(*) FROM pg_database WHERE datname = "+relayoutLiteral(name))
	return out == "1", err
}

func (r *YugabyteRelayout) databaseACL(ctx context.Context, name string) (string, error) {
	out, err := r.admin(ctx, fmt.Sprintf("SELECT coalesce(datacl::text, %s) FROM pg_database WHERE datname = %s", relayoutLiteral(relayoutDefaultACL), relayoutLiteral(name)))
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("database %s does not exist", name)
	}
	return out, nil
}

func (r *YugabyteRelayout) terminateSessions(ctx context.Context, database string) error {
	return r.endSessionsWhere(ctx, "datname = "+relayoutLiteral(database))
}

// endSessionsWhere terminates, on every node, each session pg_stat_activity matches with predicate, except the
// terminating session itself.
func (r *YugabyteRelayout) endSessionsWhere(ctx context.Context, predicate string) error {
	_, err := r.onNodes(func(node YugabyteNode) (string, error) {
		if _, err := node.Query(ctx, relayoutAdminDatabase, fmt.Sprintf(
			"SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE (%s) AND pid <> pg_backend_pid()", predicate)); err != nil {
			return "", fmt.Errorf("terminate sessions on %s: %w", node.Name(), err)
		}
		return "", nil
	})
	return err
}

// relayoutSessionQuery describes every session on a database, one line each, so a session that outlives termination
// can be identified. Relayout's own connections carry the relayoutApplicationName application name.
const relayoutSessionQuery = `SELECT concat_ws(' ', 'pid=' || pid, 'type=' || coalesce(backend_type, ''), 'user=' || coalesce(usename, ''),
       'app=' || coalesce(application_name, ''), 'client=' || coalesce(client_addr::text, 'local'), 'state=' || coalesce(state, ''),
       'backend_start=' || coalesce(backend_start::text, ''), 'query=' || left(regexp_replace(coalesce(query, ''), '\s+', ' ', 'g'), 160))
  FROM pg_stat_activity WHERE (%s) AND pid <> pg_backend_pid() ORDER BY pid`

// requireNoSessions proves, in two consecutive rounds a settle delay apart, that no tserver serves a session on
// database. A terminated backend stays visible until it exits, so busy rounds are retried until the drain timeout;
// callers revoke access first, so a lingering session cannot be a new connection. The error names every remaining
// session.
func (r *YugabyteRelayout) requireNoSessions(ctx context.Context, database string) error {
	return r.requireNoSessionsWhere(ctx, database, "datname = "+relayoutLiteral(database))
}

// requireNoSessionsWhere proves no node serves a session predicate matches, as requireNoSessions does for a database;
// subject names what the sessions belong to in the error.
func (r *YugabyteRelayout) requireNoSessionsWhere(ctx context.Context, subject, predicate string) error {
	settle := r.SettleDelay
	if settle <= 0 {
		settle = relayoutDefaultSettle
	}
	deadline := time.Now().Add(max(time.Duration(relayoutSessionWaitRounds)*settle, relayoutSessionDrainTimeout))
	zeroRounds := 0
	var lastBusy []string
	for round := 0; ; round++ {
		if round > 0 {
			if time.Now().After(deadline) {
				return fmt.Errorf("%s still has sessions after %d checks:\n%s", subject, round, strings.Join(lastBusy, "\n"))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(settle):
			}
		}
		var busy []string
		outputs, err := r.onNodes(func(node YugabyteNode) (string, error) {
			out, err := node.Query(ctx, relayoutAdminDatabase, fmt.Sprintf(relayoutSessionQuery, predicate))
			if err != nil {
				return "", fmt.Errorf("list sessions of %s on %s: %w", subject, node.Name(), err)
			}
			return out, nil
		})
		if err != nil {
			return err
		}
		for i, out := range outputs {
			for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
				if strings.TrimSpace(line) != "" {
					busy = append(busy, r.nodes()[i].Name()+": "+line)
				}
			}
		}
		if len(busy) > 0 {
			zeroRounds = 0
			lastBusy = busy
			continue
		}
		zeroRounds++
		if zeroRounds == 2 {
			return nil
		}
	}
}

// revokeAccess removes every database privilege recorded in acl and every configured role, then terminates sessions.
func (r *YugabyteRelayout) revokeAccess(ctx context.Context, database, acl string) error {
	statements, err := databaseACLRevokeStatements(database, acl, r.Roles)
	if err != nil {
		return err
	}
	if err = r.requireLease(ctx); err != nil {
		return err
	}
	if _, err = r.admin(ctx, strings.Join(statements, ";\n")+";"); err != nil {
		return fmt.Errorf("revoke access to %s: %w", database, err)
	}
	return r.terminateSessions(ctx, database)
}

// connectableRoles lists the roles other than superusers that may connect to database; PUBLIC is reported as
// "PUBLIC".
func (r *YugabyteRelayout) connectableRoles(ctx context.Context, database string) ([]string, error) {
	out, err := r.admin(ctx, fmt.Sprintf(`SELECT coalesce(d.datacl::text, %s) || '|' || (SELECT string_agg(rolname, ',' ORDER BY rolname) FROM pg_roles WHERE rolsuper)
  FROM pg_database d WHERE d.datname = %s`, relayoutLiteral(relayoutDefaultACL), relayoutLiteral(database)))
	if err != nil {
		return nil, err
	}
	acl, superusers, ok := strings.Cut(out, "|")
	if !ok {
		return nil, fmt.Errorf("database %s does not exist", database)
	}
	grantees, err := databaseACLConnectGrantees(acl)
	if err != nil {
		return nil, err
	}
	allowed := strings.Split(superusers, ",")
	var roles []string
	for _, grantee := range grantees {
		switch {
		case grantee == "":
			roles = append(roles, "PUBLIC")
		case !slices.Contains(allowed, grantee):
			roles = append(roles, grantee)
		}
	}
	return roles, nil
}

// requireFenced proves no role except a superuser may connect to database, ends every session still on it, and proves
// none remains. Only superusers can hold a session on a fenced database, and YugabyteDB opens such sessions itself:
// building an index leaves idle superuser connections from every tserver on the database being indexed, which never
// close on their own.
func (r *YugabyteRelayout) requireFenced(ctx context.Context, database string) error {
	roles, err := r.connectableRoles(ctx, database)
	if err != nil {
		return err
	}
	if len(roles) > 0 {
		return fmt.Errorf("%s is not fenced: %s may connect", database, strings.Join(roles, ", "))
	}
	if err = r.terminateSessions(ctx, database); err != nil {
		return err
	}
	return r.requireNoSessions(ctx, database)
}

// requireLiveTopology proves the relayout nodes are exactly the live tservers. Sessions are local to a tserver, so a
// tserver missing from Nodes could keep serving a writer that no session check sees. Each node is matched to its
// yb_servers() entry by the addresses and names the node itself reports.
func (r *YugabyteRelayout) requireLiveTopology(ctx context.Context) error {
	out, err := r.admin(ctx, "SELECT host FROM yb_servers() ORDER BY 1")
	if err != nil {
		return fmt.Errorf("list live tservers: %w", err)
	}
	servers := strings.Fields(out)
	claimedBy := map[string][]string{}
	var unmatched []string
	identitiesByNode := make([]map[string]bool, len(r.nodes()))
	if _, err = r.onNodes(func(node YugabyteNode) (string, error) {
		identities, identityErr := relayoutNodeIdentities(ctx, node)
		for i, candidate := range r.nodes() {
			if candidate == node {
				identitiesByNode[i] = identities
			}
		}
		return "", identityErr
	}); err != nil {
		return err
	}
	for i, node := range r.nodes() {
		identities := identitiesByNode[i]
		matched := false
		for _, server := range servers {
			if identities[server] {
				claimedBy[server] = append(claimedBy[server], node.Name())
				matched = true
			}
		}
		if !matched {
			unmatched = append(unmatched, node.Name())
		}
	}
	var problems []string
	for _, server := range servers {
		switch owners := claimedBy[server]; {
		case len(owners) == 0:
			problems = append(problems, fmt.Sprintf("live tserver %s is not among the relayout nodes", server))
		case len(owners) > 1:
			problems = append(problems, fmt.Sprintf("live tserver %s matches several nodes (%s)", server, strings.Join(owners, ", ")))
		}
	}
	for _, node := range unmatched {
		problems = append(problems, fmt.Sprintf("node %s is not a live tserver", node))
	}
	if len(problems) > 0 {
		return fmt.Errorf("relayout nodes do not match the live tservers %v:\n%s", servers, strings.Join(problems, "\n"))
	}
	return nil
}

// requireNoConnectionManager refuses when any tserver routes YSQL through the YSQL Connection Manager. It pools
// server connections by database OID and does not evict them when a database is renamed (yugabyte-db #33018), so
// pooled clients would keep failing against the renamed original after cutover. A connection through it reports
// yb_is_client_ysqlconnmgr as on.
func (r *YugabyteRelayout) requireNoConnectionManager(ctx context.Context) error {
	outputs, err := r.onNodes(func(node YugabyteNode) (string, error) {
		out, err := node.Query(ctx, relayoutAdminDatabase, "SELECT coalesce(current_setting('yb_is_client_ysqlconnmgr', true), '')")
		if err != nil {
			return "", fmt.Errorf("read connection manager setting on %s: %w", node.Name(), err)
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	var routed []string
	for i, out := range outputs {
		if strings.TrimSpace(out) == "on" {
			routed = append(routed, r.nodes()[i].Name())
		}
	}
	if len(routed) > 0 {
		return fmt.Errorf("YSQL Connection Manager is enabled on %s; it keeps pooled connections on a renamed database (yugabyte-db #33018), so a relayout cannot run",
			strings.Join(routed, ", "))
	}
	return nil
}

// relayoutNodeIdentities returns the addresses and host names a node answers to: those its operating system reports
// and the server address of its own local YSQL connection.
func relayoutNodeIdentities(ctx context.Context, node YugabyteNode) (map[string]bool, error) {
	out, err := node.Shell(ctx, "hostname 2>/dev/null || true\nhostname -f 2>/dev/null || true\nhostname -I 2>/dev/null || true\nhostname -i 2>/dev/null || true\n")
	if err != nil {
		return nil, fmt.Errorf("read identities of %s: %w", node.Name(), err)
	}
	server, err := node.Query(ctx, relayoutAdminDatabase, "SELECT host(inet_server_addr())")
	if err != nil {
		return nil, fmt.Errorf("read YSQL server address of %s: %w", node.Name(), err)
	}
	identities := map[string]bool{}
	for _, field := range strings.Fields(out + " " + server) {
		identities[field] = true
	}
	return identities, nil
}

// relayoutCopyMarker is the database comment that proves a relayout created a copy database. Copies are only ever
// replaced or dropped when they carry it, so an unrelated database that happens to use the name is never touched.
func relayoutCopyMarker(kind, database string) string {
	return fmt.Sprintf("frameworks relayout %s of %s", kind, database)
}

const (
	relayoutCopyShadow    = "shadow"
	relayoutCopyPreflight = "preflight"
)

// requireReplaceableCopy refuses when name exists without the marker of a relayout copy of this database. An unmarked
// database is accepted only when the journal records that this relayout was creating it and it holds no relation,
// which is what a crash between CREATE DATABASE and COMMENT ON DATABASE leaves.
func (r *YugabyteRelayout) requireReplaceableCopy(ctx context.Context, name, kind string) error {
	exists, err := r.databaseExists(ctx, name)
	if err != nil || !exists {
		return err
	}
	marker, err := r.admin(ctx, "SELECT coalesce(shobj_description(oid, 'pg_database'), '') FROM pg_database WHERE datname = "+relayoutLiteral(name))
	if err != nil {
		return err
	}
	if marker == relayoutCopyMarker(kind, r.Database) {
		return nil
	}
	if marker == "" {
		record, recordErr := r.Record(ctx)
		if recordErr != nil {
			return recordErr
		}
		if record != nil && record.PendingCopy == name {
			relations, countErr := r.Primary.Query(ctx, name, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%'`)
			if countErr != nil {
				return fmt.Errorf("inspect interrupted copy %s: %w", name, countErr)
			}
			if strings.TrimSpace(relations) == "0" {
				return nil
			}
		}
	}
	return fmt.Errorf("database %s exists but was not created as the relayout %s of %s; refusing to replace it", name, kind, r.Database)
}

// requireRelaidCopy proves the database called name is this relayout's copy, not the original: its OID differs from
// the recorded source and it still carries the shadow marker, which cutover replaces only after access is restored.
func (r *YugabyteRelayout) requireRelaidCopy(ctx context.Context, name string, record *RelayoutRecord) error {
	out, err := r.admin(ctx, fmt.Sprintf("SELECT oid::text || '|' || %s FROM pg_database WHERE datname = %s",
		relayoutBase64Column("shobj_description(oid, 'pg_database')"), relayoutLiteral(name)))
	if err != nil {
		return err
	}
	oid, encoded, ok := strings.Cut(out, "|")
	if !ok || oid == "" {
		return fmt.Errorf("database %s does not exist", name)
	}
	marker, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode %s comment: %w", name, err)
	}
	if oid == record.SourceOID {
		return fmt.Errorf("%s is the original database (oid %s), not the relaid copy; a rollback may have returned it, so inspect the cluster", name, oid)
	}
	if string(marker) != relayoutCopyMarker(relayoutCopyShadow, r.Database) {
		return fmt.Errorf("%s (oid %s) is neither the original nor this relayout's copy; refusing to touch it", name, oid)
	}
	return nil
}

// dropCopy drops a relayout copy database, refusing when name exists without the copy marker.
func (r *YugabyteRelayout) dropCopy(ctx context.Context, name, kind string) error {
	if err := r.requireReplaceableCopy(ctx, name, kind); err != nil {
		return err
	}
	return r.dropDatabaseIfExists(ctx, name)
}

// restoreSourceComment replaces the copy marker with the original database's recorded comment once the shadow holds
// the canonical name. A database that no longer carries the marker already has its final comment.
func (r *YugabyteRelayout) restoreSourceComment(ctx context.Context, record *RelayoutRecord) error {
	marked, err := r.admin(ctx, fmt.Sprintf("SELECT count(*) FROM pg_database WHERE datname = %s AND shobj_description(oid, 'pg_database') = %s",
		relayoutLiteral(r.Database), relayoutLiteral(relayoutCopyMarker(relayoutCopyShadow, r.Database))))
	if err != nil {
		return fmt.Errorf("read relayout marker on %s: %w", r.Database, err)
	}
	if marked != "1" {
		return nil
	}
	comment := "NULL"
	if record.SourceComment != "" {
		comment = relayoutLiteral(record.SourceComment)
	}
	if _, err = r.admin(ctx, fmt.Sprintf("COMMENT ON DATABASE %s IS %s", relayoutIdentifier(r.Database), comment)); err != nil {
		return fmt.Errorf("restore comment on %s: %w", r.Database, err)
	}
	return nil
}

// dumpNode resolves the node holding the recorded staged dump.
func (r *YugabyteRelayout) dumpNode(record *RelayoutRecord) (YugabyteNode, error) {
	if record.DumpHost == "" {
		return r.Primary, nil
	}
	for _, node := range r.nodes() {
		if node.Name() == record.DumpHost {
			return node, nil
		}
	}
	return nil, fmt.Errorf("staged dump %s is on %s, which is not a relayout node", record.DumpPath, record.DumpHost)
}

// stagedDumpLocation resolves the recorded staged dump to a path under this relayout's dump directory and the node
// holding it, refusing a path outside the directory.
func (r *YugabyteRelayout) stagedDumpLocation(record *RelayoutRecord) (YugabyteNode, error) {
	if record.DumpPath == "" {
		return nil, nil
	}
	root := path.Clean(path.Join(r.DumpDir, r.Database))
	dump := path.Clean(record.DumpPath)
	if !path.IsAbs(record.DumpPath) || !strings.HasPrefix(dump, root+"/") {
		return nil, fmt.Errorf("staged dump %s is outside %s; refusing to remove it (pass the --dump-dir prepare used)", record.DumpPath, root)
	}
	return r.dumpNode(record)
}

// removeStagedDump deletes the recorded staged dump from the node holding it.
func (r *YugabyteRelayout) removeStagedDump(ctx context.Context, record *RelayoutRecord) error {
	if record.DumpPath == "" {
		return nil
	}
	node, err := r.stagedDumpLocation(record)
	if err != nil {
		return err
	}
	dump := path.Clean(record.DumpPath)
	if _, err = node.Shell(ctx, "rm -rf "+shellQuote(dump)); err != nil {
		return fmt.Errorf("remove staged dump %s on %s: %w", dump, node.Name(), err)
	}
	return nil
}

// Fence records the source ACL on first entry, revokes every connect path, terminates sessions on every tserver,
// and proves none remain. Services keep running: from here until access is restored they cannot connect, and their
// in-flight transactions were rolled back when their sessions ended. The revocation is what keeps the source
// unchanged, because a revoked role cannot open a session and CONNECT is checked only when a session starts.
func (r *YugabyteRelayout) Fence(ctx context.Context) error {
	record, err := r.Record(ctx)
	if err != nil {
		return err
	}
	if err = r.requireState(record, RelayoutPrepared, RelayoutFenced); err != nil {
		return err
	}
	if err = r.requireLiveTopology(ctx); err != nil {
		return err
	}
	if err = r.requireNoConnectionManager(ctx); err != nil {
		return err
	}
	fenceFrom := []RelayoutState{RelayoutPrepared, RelayoutFenced}
	// A recorded identity is the pre-fence truth even when the state never advanced, because an interrupted fence
	// leaves access revoked: re-reading the live ACL would record the revoked one.
	sourceACL, sourceOID, sourceComment := record.SourceACL, record.SourceOID, record.SourceComment
	if sourceACL == "" || sourceOID == "" {
		current, aclErr := r.databaseACL(ctx, r.Database)
		if aclErr != nil {
			return aclErr
		}
		if current == "{}" {
			return fmt.Errorf("%s grants no database privileges, so its access cannot be recorded for restoration", r.Database)
		}
		identity, identityErr := r.admin(ctx, fmt.Sprintf("SELECT oid::text || '|' || %s FROM pg_database WHERE datname = %s",
			relayoutBase64Column("shobj_description(oid, 'pg_database')"), relayoutLiteral(r.Database)))
		if identityErr != nil {
			return identityErr
		}
		oid, encodedComment, ok := strings.Cut(identity, "|")
		if !ok || oid == "" {
			return fmt.Errorf("database %s does not exist", r.Database)
		}
		comment, decodeErr := base64.StdEncoding.DecodeString(encodedComment)
		if decodeErr != nil {
			return fmt.Errorf("decode %s comment: %w", r.Database, decodeErr)
		}
		sourceACL, sourceOID, sourceComment = current, oid, string(comment)
		// Record before revoking: the next attempt must restore these grants even if this one dies mid-fence.
		if err = r.record(ctx, fenceFrom, map[string]string{
			"source_acl": relayoutLiteral(sourceACL), "source_oid": relayoutLiteral(sourceOID), "source_comment": relayoutLiteral(sourceComment),
		}); err != nil {
			return err
		}
	} else if err = r.requireOriginal(ctx, r.Database, record); err != nil {
		return err
	}
	if err = r.revokeAccess(ctx, r.Database, sourceACL); err != nil {
		return err
	}
	// The journal says fenced as soon as access is revoked, so a failed session drain below can still be undone by
	// cutover --rollback. Every later step re-checks the fence before acting.
	if err = r.advance(ctx, fenceFrom, RelayoutFenced,
		map[string]string{
			"source_acl": relayoutLiteral(sourceACL), "source_oid": relayoutLiteral(sourceOID), "source_comment": relayoutLiteral(sourceComment),
			"dump_path": "NULL", "dump_sha256": "NULL", "receipt": "NULL",
		}); err != nil {
		return err
	}
	if err = r.requireFenced(ctx, r.Database); err != nil {
		return fmt.Errorf("%w\naccess to %s is revoked; run fence again once these sessions are gone, or cutover --rollback to restore access", err, r.Database)
	}
	return nil
}

// requireOriginal proves the database called name is the original the journal fenced, by its OID, which a rename
// preserves. It guards every rename and drop of the original so an unrelated database under its name is never touched.
func (r *YugabyteRelayout) requireOriginal(ctx context.Context, name string, record *RelayoutRecord) error {
	if record.SourceOID == "" {
		return fmt.Errorf("relayout journal for %s records no source database identity; fence it again", r.Database)
	}
	oid, err := r.admin(ctx, "SELECT oid::text FROM pg_database WHERE datname = "+relayoutLiteral(name))
	if err != nil {
		return err
	}
	if oid != record.SourceOID {
		return fmt.Errorf("database %s (oid %q) is not the original %s fenced by this relayout (oid %s); refusing to rename or drop it",
			name, oid, r.Database, record.SourceOID)
	}
	return nil
}

// UnmanagedTables lists source tables that neither the layout nor the runtime table set accounts for. Their placement
// cannot be resolved when the schema is rewritten, so a relayout refuses to start while any exist.
func (r *YugabyteRelayout) UnmanagedTables(ctx context.Context, layout *DatabaseLayout) ([]string, error) {
	owners, err := r.tableOwners(ctx, r.Database)
	if err != nil {
		return nil, err
	}
	var unmanaged []string
	for table := range owners {
		if _, placed := layout.Placement(table); placed {
			continue
		}
		if isRelayoutRuntimeTable(table) {
			continue
		}
		unmanaged = append(unmanaged, table)
	}
	sort.Strings(unmanaged)
	return unmanaged, nil
}

func (r *YugabyteRelayout) tableOwners(ctx context.Context, database string) (map[string]string, error) {
	out, err := r.Primary.Query(ctx, database, "SELECT schemaname || '.' || tablename, tableowner FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY 1")
	if err != nil {
		return nil, fmt.Errorf("list %s tables: %w", database, err)
	}
	owners := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		table, owner, ok := strings.Cut(line, "|")
		if !ok {
			return nil, fmt.Errorf("unexpected table row %q", line)
		}
		owners[table] = owner
	}
	return owners, nil
}

// Prepare builds the shadow while services keep running: it creates the shadow colocated with the source's owner,
// restores the source's schema-only dump rewritten for the layout, including every index, constraint, and trigger, and
// refuses unless the shadow schema equals the source schema. No data is copied yet. From prepared on, migrations and
// provisioning refuse to run, so the source schema cannot move away from the shadow's before cutover copies the data.
func (r *YugabyteRelayout) Prepare(ctx context.Context, layout *DatabaseLayout) error {
	record, err := r.Record(ctx)
	if err != nil {
		return err
	}
	prepareFrom := []RelayoutState{RelayoutPlanned, RelayoutPrepared, RelayoutRolledBack, RelayoutFinished}
	if err = r.requireState(record, prepareFrom...); err != nil {
		return err
	}
	if record.SourceOID != "" {
		return fmt.Errorf("an earlier fence of %s revoked its access without finishing; run cutover --rollback first", r.Database)
	}
	if err = r.requireLiveTopology(ctx); err != nil {
		return err
	}
	if err = r.requireNoConnectionManager(ctx); err != nil {
		return err
	}
	if err = r.requireReplaceableCopy(ctx, r.ShadowName(), relayoutCopyShadow); err != nil {
		return err
	}
	blockers, err := r.sourceBlockers(ctx, layout)
	if err != nil {
		return err
	}
	if len(blockers) > 0 {
		return fmt.Errorf("%s cannot be relaid out:\n%s", r.Database, strings.Join(blockers, "\n"))
	}
	if err = r.buildCopySchema(ctx, layout, r.ShadowName(), relayoutCopyShadow, "schema", &RelayoutCopyTimings{}); err != nil {
		return err
	}
	return r.advance(ctx, prepareFrom, RelayoutPrepared, map[string]string{
		"dump_path": "NULL", "dump_sha256": "NULL", "dump_host": "NULL", "receipt": "NULL",
	})
}

// Copy loads the fenced source's data into the prepared shadow. It first proves the source schema still equals the
// shadow's, then stages a checksummed data-only dump and loads it with triggers and foreign-key checks skipped. A copy
// resumed after an interruption empties the shadow's tables before loading again.
func (r *YugabyteRelayout) Copy(ctx context.Context) error {
	record, err := r.Record(ctx)
	if err != nil {
		return err
	}
	copyFrom := []RelayoutState{RelayoutFenced, RelayoutDumped}
	if err = r.requireState(record, copyFrom...); err != nil {
		return err
	}
	if err = r.requireLiveTopology(ctx); err != nil {
		return err
	}
	shadow := r.ShadowName()
	exists, err := r.databaseExists(ctx, shadow)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("the prepared shadow %s is missing; run cutover --rollback, then prepare again", shadow)
	}
	if err = r.requireReplaceableCopy(ctx, shadow, relayoutCopyShadow); err != nil {
		return err
	}
	for _, database := range []string{r.Database, shadow} {
		if err = r.requireFenced(ctx, database); err != nil {
			return err
		}
	}
	if err = r.requireSameSchema(ctx, shadow, &RelayoutCopyTimings{}); err != nil {
		return fmt.Errorf("%w\nthe schema of %s changed after prepare; run cutover --rollback, then prepare again", err, r.Database)
	}
	if record.DumpHost != "" && record.DumpHost != r.Primary.Name() {
		if err = r.removeStagedDump(ctx, record); err != nil {
			return err
		}
	}
	dump, err := r.stageDump(ctx, "relayout", relayoutDataSections)
	if err != nil {
		return err
	}
	if err = r.advance(ctx, copyFrom, RelayoutDumped, map[string]string{
		"dump_path": relayoutLiteral(dump.Dir), "dump_sha256": relayoutLiteral(dump.manifestSHA256()),
		"dump_host": relayoutLiteral(r.Primary.Name()), "receipt": "NULL",
	}); err != nil {
		return err
	}
	if record.State == RelayoutDumped {
		if err = r.emptyCopy(ctx, shadow); err != nil {
			return err
		}
	}
	if err = r.loadData(ctx, shadow, dump, &RelayoutCopyTimings{}); err != nil {
		return err
	}
	return r.advance(ctx, []RelayoutState{RelayoutDumped}, RelayoutRestored, nil)
}

// relayoutDump is a set of section dumps staged on the primary node.
type relayoutDump struct {
	Dir      string
	Sections []string
	SHA256   map[string]string
}

func (d *relayoutDump) path(section string) string { return path.Join(d.Dir, section+".sql") }

// manifestSHA256 fingerprints every staged section so a receipt names the exact dump it verified.
func (d *relayoutDump) manifestSHA256() string {
	hash := sha256.New()
	for _, section := range d.Sections {
		fmt.Fprintf(hash, "%s %s\n", section, d.SHA256[section])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// RelayoutCopyTimings records how long each phase of building a copy took. Schema phases run before the fence; the
// data dump, the data load, and the schema comparison run while services are fenced.
type RelayoutCopyTimings struct {
	SchemaDump time.Duration
	PreData    time.Duration
	PostData   time.Duration
	DataDump   time.Duration
	Data       time.Duration
	Compare    time.Duration
}

// stageDump replaces the label directory under the dump root with fresh dumps of the given sections and their
// checksums.
func (r *YugabyteRelayout) stageDump(ctx context.Context, label string, sections []string) (*relayoutDump, error) {
	dir := path.Join(r.DumpDir, r.Database, label)
	var script strings.Builder
	fmt.Fprintf(&script, "set -eu\numask 077\n%s\ndump_bin=\"$(fw_yb_bin ysql_dump)\"\nrm -rf %s\nmkdir -p %s\n",
		YugabyteBinaryResolverShell, shellQuote(dir), shellQuote(dir))
	// ysql_dump reports problems it does not treat as fatal on stderr, so any stderr output fails the section.
	files := make([]string, len(sections))
	for i, section := range sections {
		file := path.Join(dir, section+".sql")
		files[i] = section + ".sql"
		fmt.Fprintf(&script, `if ! PGAPPNAME=%s "$dump_bin" -h %s -p %d -U yugabyte -d %s --section=%s -f %s 2> %s; then
  echo "ysql_dump --section=%s failed:" >&2; tail -c 1800 %s >&2; exit 5
fi
if [ -s %s ]; then
  echo "ysql_dump --section=%s wrote to stderr:" >&2; tail -c 1800 %s >&2; exit 4
fi
mv %s %s
`, r.applicationName(), shellQuote(r.YSQLHost), r.YSQLPort, shellQuote(r.Database), section, shellQuote(file+".partial"), shellQuote(file+".stderr"),
			section, shellQuote(file+".stderr"), shellQuote(file+".stderr"), section, shellQuote(file+".stderr"),
			shellQuote(file+".partial"), shellQuote(file))
	}
	fmt.Fprintf(&script, "cd %s\nsha256sum %s\n", shellQuote(dir), strings.Join(files, " "))
	out, err := r.Primary.Shell(ctx, script.String())
	if err != nil {
		return nil, fmt.Errorf("dump %s on %s: %w", r.Database, r.Primary.Name(), err)
	}
	dump := &relayoutDump{Dir: dir, Sections: sections, SHA256: map[string]string{}}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && sha256HexPattern.MatchString(fields[0]) {
			dump.SHA256[strings.TrimSuffix(fields[1], ".sql")] = fields[0]
		}
	}
	for _, section := range sections {
		if dump.SHA256[section] == "" {
			return nil, fmt.Errorf("dump of %s produced no checksum for %s (output %q)", r.Database, section, out)
		}
	}
	return dump, nil
}

// createCopy creates target colocated with the source's owner, connection limit, and the copy marker, replacing an
// earlier copy of the same kind, and revokes access to it.
func (r *YugabyteRelayout) createCopy(ctx context.Context, target, kind string) error {
	source, err := r.admin(ctx, "SELECT pg_get_userbyid(datdba) || '|' || datconnlimit FROM pg_database WHERE datname = "+relayoutLiteral(r.Database))
	if err != nil {
		return err
	}
	owner, connectionLimit, ok := strings.Cut(source, "|")
	if !ok || owner == "" {
		return fmt.Errorf("database %s does not exist", r.Database)
	}
	if _, err = strconv.Atoi(connectionLimit); err != nil {
		return fmt.Errorf("database %s reports connection limit %q", r.Database, connectionLimit)
	}
	if err = r.dropCopy(ctx, target, kind); err != nil {
		return err
	}
	if err = r.notePendingCopy(ctx, target); err != nil {
		return err
	}
	if err = r.requireLease(ctx); err != nil {
		return err
	}
	if _, err = r.admin(ctx, fmt.Sprintf("CREATE DATABASE %s WITH OWNER = %s COLOCATION = true CONNECTION LIMIT = %s;\nCOMMENT ON DATABASE %s IS %s;",
		relayoutIdentifier(target), relayoutIdentifier(owner), connectionLimit, relayoutIdentifier(target), relayoutLiteral(relayoutCopyMarker(kind, r.Database)))); err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	if err = r.notePendingCopy(ctx, ""); err != nil {
		return err
	}
	targetACL, err := r.databaseACL(ctx, target)
	if err != nil {
		return err
	}
	return r.revokeAccess(ctx, target, targetACL)
}

// buildCopySchema creates target and restores the source's schema into it, rewritten for the layout, then refuses
// unless the two schemas are equal. The schema dump is staged under label and removed once restored.
func (r *YugabyteRelayout) buildCopySchema(ctx context.Context, layout *DatabaseLayout, target, kind, label string, timings *RelayoutCopyTimings) error {
	if err := r.createCopy(ctx, target, kind); err != nil {
		return err
	}
	started := time.Now()
	dump, err := r.stageDump(ctx, label, relayoutSchemaSections)
	timings.SchemaDump = time.Since(started)
	if err != nil {
		return err
	}
	for _, section := range relayoutSchemaSections {
		started = time.Now()
		content, readErr := r.readStaged(ctx, dump.path(section), dump.SHA256[section])
		if readErr != nil {
			return readErr
		}
		rewritten, rewriteErr := RewriteDumpForLayout(layout, content, isRelayoutRuntimeTable)
		if rewriteErr != nil {
			return fmt.Errorf("rewrite %s %s for the %s layout: %w", r.Database, section, layout.Database, rewriteErr)
		}
		file := path.Join(dump.Dir, section+".colocated.sql")
		sum, writeErr := r.writeStaged(ctx, file, rewritten)
		if writeErr != nil {
			return writeErr
		}
		if err = r.restoreStaged(ctx, target, file, sum, ""); err != nil {
			return err
		}
		switch section {
		case "pre-data":
			timings.PreData = time.Since(started)
		case "post-data":
			timings.PostData = time.Since(started)
		}
	}
	if err = r.requireSameSchema(ctx, target, &RelayoutCopyTimings{}); err != nil {
		return err
	}
	if _, err = r.Primary.Shell(ctx, "rm -rf "+shellQuote(dump.Dir)); err != nil {
		return fmt.Errorf("remove schema dump %s: %w", dump.Dir, err)
	}
	return nil
}

// requireSameSchema refuses unless target's schema equals the source's.
func (r *YugabyteRelayout) requireSameSchema(ctx context.Context, target string, timings *RelayoutCopyTimings) error {
	started := time.Now()
	differences, err := r.schemaDifferences(ctx, r.Database, target)
	timings.Compare = time.Since(started)
	if err != nil {
		return err
	}
	if len(differences) > 0 {
		return fmt.Errorf("copy %s schema differs from %s:\n%s", target, r.Database, strings.Join(truncateLines(differences, 40), "\n"))
	}
	return nil
}

// loadData restores a staged data dump into target with triggers and foreign-key checks skipped.
func (r *YugabyteRelayout) loadData(ctx context.Context, target string, dump *relayoutDump, timings *RelayoutCopyTimings) error {
	started := time.Now()
	for _, section := range relayoutDataSections {
		if err := r.restoreStaged(ctx, target, dump.path(section), dump.SHA256[section], relayoutReplicaRoleOptions); err != nil {
			return err
		}
	}
	timings.Data = time.Since(started)
	return nil
}

// emptyCopy truncates every table of a copy database, so a data load interrupted part way can run again.
func (r *YugabyteRelayout) emptyCopy(ctx context.Context, target string) error {
	if err := r.requireLease(ctx); err != nil {
		return err
	}
	if _, err := r.Primary.Query(ctx, target, `SELECT 'TRUNCATE ' || string_agg(format('%I.%I', schemaname, tablename), ', ')
  FROM pg_tables
 WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
HAVING count(*) > 0
\gexec`); err != nil {
		return fmt.Errorf("empty %s before reloading: %w", target, err)
	}
	return nil
}

// readStaged returns a staged file's exact bytes after proving they still match the recorded checksum. The node checks
// the checksum before sending, and the content travels base64-encoded, because runners trim surrounding whitespace
// from stdout and a dump ends in newlines.
func (r *YugabyteRelayout) readStaged(ctx context.Context, file, wantSHA string) (string, error) {
	out, err := r.Primary.Shell(ctx, fmt.Sprintf(`set -eu
actual="$(sha256sum %s | cut -d ' ' -f 1)"
if [ "$actual" != %s ]; then
  echo "staged %s checksum is $actual, recorded %s" >&2
  exit 3
fi
base64 < %s | tr -d '\n'
`, shellQuote(file), shellQuote(wantSHA), path.Base(file), wantSHA, shellQuote(file)))
	if err != nil {
		return "", fmt.Errorf("read staged %s: %w", file, err)
	}
	content, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return "", fmt.Errorf("decode staged %s: %w", file, err)
	}
	sum := sha256.Sum256(content)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return "", fmt.Errorf("staged %s has checksum %s after transfer, recorded %s", file, got, wantSHA)
	}
	return string(content), nil
}

// writeStaged writes content to file on the primary node through a randomly delimited heredoc and proves the node
// holds exactly those bytes.
func (r *YugabyteRelayout) writeStaged(ctx context.Context, file, content string) (string, error) {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	delimiter, err := relayoutRandomToken("FW_RELAYOUT_FILE_", 12)
	if err != nil {
		return "", err
	}
	out, err := r.Primary.Shell(ctx, fmt.Sprintf("set -eu\numask 077\ncat > %s <<'%s'\n%s%s\nsha256sum %s | cut -d ' ' -f 1\n",
		shellQuote(file), delimiter, content, delimiter, shellQuote(file)))
	if err != nil {
		return "", fmt.Errorf("write staged %s: %w", file, err)
	}
	sum := sha256.Sum256([]byte(content))
	want := hex.EncodeToString(sum[:])
	if got := strings.TrimSpace(out); got != want {
		return "", fmt.Errorf("staged %s has checksum %q after writing, want %s", file, got, want)
	}
	return want, nil
}

// restoreStaged restores a staged file into database after re-checking its checksum, stopping at the first error,
// with pgOptions as the session's PGOPTIONS. Dump files set client_min_messages to warning, so stderr output is a
// warning or error and fails the restore, with one exception: YugabyteDB warns that COPY into a table with a user
// trigger runs as one transaction instead of batches. The shadow has its triggers when data loads, and the rows land
// the same either way, so that warning alone is accepted.
func (r *YugabyteRelayout) restoreStaged(ctx context.Context, database, file, wantSHA, pgOptions string) error {
	stderr := file + ".restore.stderr"
	if _, err := r.Primary.Shell(ctx, fmt.Sprintf(`set -eu
umask 077
%s
actual="$(sha256sum %s | cut -d ' ' -f 1)"
if [ "$actual" != %s ]; then
  echo "staged %s checksum is $actual, recorded %s" >&2
  exit 3
fi
if ! PGAPPNAME=%s PGOPTIONS=%s "$(fw_yb_bin ysqlsh)" -X -h %s -p %d -U yugabyte -d %s -v ON_ERROR_STOP=1 -q -f %s >/dev/null 2> %s; then
  tail -c 1800 %s >&2; exit 5
fi
if grep -v -e 'WARNING:  ROWS_PER_TRANSACTION is not supported on table with non RI trigger$' \
     -e '^DETAIL:  Defaulting to using one transaction for the entire copy\.$' \
     -e '^HINT:  Set rows_per_transaction option to .0. to disable batching and remove this warning\.$' %s > %s; then
  echo "restore wrote to stderr:" >&2; tail -c 1800 %s >&2; exit 4
fi
`, YugabyteBinaryResolverShell, shellQuote(file), shellQuote(wantSHA), path.Base(file), wantSHA,
		r.applicationName(), shellQuote(pgOptions), shellQuote(r.YSQLHost), r.YSQLPort, shellQuote(database), shellQuote(file), shellQuote(stderr),
		shellQuote(stderr), shellQuote(stderr), shellQuote(stderr+".unexpected"), shellQuote(stderr+".unexpected"))); err != nil {
		return fmt.Errorf("restore %s into %s: %w", file, database, err)
	}
	return nil
}

// sourceBlockers lists reasons the source cannot be copied faithfully: tables whose placement the layout cannot
// resolve and database-level settings, which a new database does not inherit.
func (r *YugabyteRelayout) sourceBlockers(ctx context.Context, layout *DatabaseLayout) ([]string, error) {
	if !layout.Colocated() {
		return []string{fmt.Sprintf("the %s layout is not colocated", layout.Database)}, nil
	}
	var blockers []string
	unmanaged, err := r.UnmanagedTables(ctx, layout)
	if err != nil {
		return nil, err
	}
	if len(unmanaged) > 0 {
		blockers = append(blockers, "tables outside the layout: "+strings.Join(unmanaged, ", "))
	}
	settings, err := r.admin(ctx, "SELECT count(*) FROM pg_db_role_setting s JOIN pg_database d ON d.oid = s.setdatabase WHERE d.datname = "+relayoutLiteral(r.Database))
	if err != nil {
		return nil, err
	}
	if settings != "0" {
		blockers = append(blockers, "database-level settings exist (pg_db_role_setting); a relayout does not copy them")
	}
	// The copy is created with CREATE DATABASE from template1 and restored from a dump without --create or
	// --use_tablespaces, so it takes template1's encoding and locale and places every relation in pg_default. A
	// source that differs would change silently, so it is refused instead.
	attributes, err := r.admin(ctx, fmt.Sprintf(`SELECT concat_ws('|',
         CASE WHEN s.encoding <> t.encoding THEN 'encoding ' || pg_encoding_to_char(s.encoding) || ' differs from template1 ' || pg_encoding_to_char(t.encoding) END,
         CASE WHEN s.datcollate <> t.datcollate THEN 'collation ' || s.datcollate || ' differs from template1 ' || t.datcollate END,
         CASE WHEN s.datctype <> t.datctype THEN 'ctype ' || s.datctype || ' differs from template1 ' || t.datctype END,
         CASE WHEN s.dattablespace <> (SELECT oid FROM pg_tablespace WHERE spcname = 'pg_default') THEN 'the database uses tablespace ' || (SELECT spcname FROM pg_tablespace WHERE oid = s.dattablespace) END)
  FROM pg_database s, pg_database t
 WHERE s.datname = %s AND t.datname = 'template1'`, relayoutLiteral(r.Database)))
	if err != nil {
		return nil, err
	}
	for _, attribute := range strings.Split(attributes, "|") {
		if attribute != "" {
			blockers = append(blockers, attribute+"; a relayout copy would not keep it")
		}
	}
	// Shared system catalogs live in pg_global, so only user relations are counted.
	tablespaces, err := r.Primary.Query(ctx, r.Database, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE c.reltablespace <> 0 AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_toast%'`)
	if err != nil {
		return nil, fmt.Errorf("list %s tablespace placement: %w", r.Database, err)
	}
	if strings.TrimSpace(tablespaces) != "0" {
		blockers = append(blockers, fmt.Sprintf("%s relation(s) use a non-default tablespace; a relayout restores every relation into pg_default", strings.TrimSpace(tablespaces)))
	}
	return blockers, nil
}

// RelayoutEnvironment is a read-only readiness report for the cluster and the source database.
type RelayoutEnvironment struct {
	ReachableNodes []string
	DumpDirFreeKB  int64
	Blockers       []string
}

// CheckEnvironment proves, without writing anything, that every tserver accepts local superuser YSQL, that the
// primary node has the client binaries and a writable dump location, and that the source can be copied.
func (r *YugabyteRelayout) CheckEnvironment(ctx context.Context, layout *DatabaseLayout) (*RelayoutEnvironment, error) {
	environment := &RelayoutEnvironment{}
	for _, node := range r.nodes() {
		if _, err := node.Query(ctx, relayoutAdminDatabase, "SELECT 1"); err != nil {
			environment.Blockers = append(environment.Blockers, fmt.Sprintf("tserver %s: local superuser YSQL failed: %v", node.Name(), err))
			continue
		}
		environment.ReachableNodes = append(environment.ReachableNodes, node.Name())
	}
	if len(environment.ReachableNodes) == len(r.nodes()) {
		if err := r.requireLiveTopology(ctx); err != nil {
			environment.Blockers = append(environment.Blockers, err.Error())
		}
		if err := r.requireNoConnectionManager(ctx); err != nil {
			environment.Blockers = append(environment.Blockers, err.Error())
		}
	}
	for _, copyDatabase := range []struct{ name, kind string }{{r.ShadowName(), relayoutCopyShadow}, {r.PreflightName(), relayoutCopyPreflight}} {
		if err := r.requireReplaceableCopy(ctx, copyDatabase.name, copyDatabase.kind); err != nil {
			environment.Blockers = append(environment.Blockers, err.Error())
		}
	}
	out, err := r.Primary.Shell(ctx, fmt.Sprintf(`set -u
%s
missing=""
for binary in ysql_dump ysqlsh; do fw_yb_bin "$binary" >/dev/null 2>&1 || missing="$missing $binary"; done
for binary in sha256sum df du cat; do command -v "$binary" >/dev/null 2>&1 || missing="$missing $binary"; done
echo "missing=$missing"
target=%s
while [ ! -d "$target" ]; do target="$(dirname "$target")"; done
if [ -w "$target" ]; then echo "writable=yes"; else echo "writable=no $target"; fi
df -Pk "$target" | awk 'NR == 2 { print "free_kb=" $4 }'
`, YugabyteBinaryResolverShell, shellQuote(path.Join(r.DumpDir, r.Database))))
	if err != nil {
		environment.Blockers = append(environment.Blockers, fmt.Sprintf("primary %s: environment probe failed: %v", r.Primary.Name(), err))
	}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "missing":
			if strings.TrimSpace(value) != "" {
				environment.Blockers = append(environment.Blockers, fmt.Sprintf("primary %s lacks binaries:%s", r.Primary.Name(), value))
			}
		case "writable":
			if value != "yes" {
				environment.Blockers = append(environment.Blockers, fmt.Sprintf("primary %s cannot write the dump directory (%s)", r.Primary.Name(), strings.TrimPrefix(value, "no ")))
			}
		case "free_kb":
			if kilobytes, parseErr := strconv.ParseInt(strings.TrimSpace(value), 10, 64); parseErr == nil {
				environment.DumpDirFreeKB = kilobytes
			}
		}
	}
	exists, err := r.databaseExists(ctx, r.Database)
	if err != nil {
		return nil, err
	}
	if !exists {
		environment.Blockers = append(environment.Blockers, fmt.Sprintf("database %s does not exist", r.Database))
		return environment, nil
	}
	blockers, err := r.sourceBlockers(ctx, layout)
	if err != nil {
		return nil, err
	}
	environment.Blockers = append(environment.Blockers, blockers...)
	return environment, nil
}

// RelayoutPreflight is the result of rehearsing a relayout copy against the live source.
type RelayoutPreflight struct {
	Environment      *RelayoutEnvironment
	DumpBytes        int64
	Tables           int
	Timings          RelayoutCopyTimings
	ChecksumDuration time.Duration
	// WindowChecks is the time cutover spends inside the window on checks rather than data: live topology, the
	// connection manager, session drains before copying, verifying, and each rename, placement, and capability
	// probes. Preflight runs the same checks against its scratch copy.
	WindowChecks   time.Duration
	PlacementDrift []string
	ProbeFailures  []string
}

const (
	// relayoutWindowTopologyChecks counts cutover's live-topology checks: on entry, then in fence and copy.
	relayoutWindowTopologyChecks = 3
	// relayoutWindowSessionProofs counts the times cutover proves a database has no sessions while services are
	// fenced: fence, source and shadow before the copy, and once before each rename.
	relayoutWindowSessionProofs = 5
)

// Preflight rehearses the whole copy without downtime, in the order a relayout runs it: it builds a colocated scratch
// database's schema from the live source, then dumps and loads the data and compares the schemas as cutover would
// while fenced, verifies the result, measures every phase, and drops the scratch database and dump even on failure.
// The source is never fenced or changed. A relayout in progress blocks it.
func (r *YugabyteRelayout) Preflight(ctx context.Context, layout *DatabaseLayout, runtimeRole string, probes []string) (report *RelayoutPreflight, err error) {
	record, err := r.Record(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.requireState(record, RelayoutPlanned, RelayoutPrepared, RelayoutRolledBack, RelayoutFinished); err != nil {
		return nil, err
	}
	report = &RelayoutPreflight{}
	if report.Environment, err = r.CheckEnvironment(ctx, layout); err != nil {
		return nil, err
	}
	if len(report.Environment.Blockers) > 0 {
		return report, fmt.Errorf("%s is not ready:\n%s", r.Database, strings.Join(report.Environment.Blockers, "\n"))
	}
	scratch := r.PreflightName()
	if err = r.requireReplaceableCopy(ctx, scratch, relayoutCopyPreflight); err != nil {
		return report, err
	}
	stagedDir := path.Join(r.DumpDir, r.Database, "preflight")
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		if dropErr := r.dropCopy(cleanupCtx, scratch, relayoutCopyPreflight); dropErr != nil {
			err = errors.Join(err, fmt.Errorf("drop preflight database %s: %w", scratch, dropErr))
		}
		if _, removeErr := r.Primary.Shell(cleanupCtx, "rm -rf "+shellQuote(stagedDir)); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove preflight dump %s: %w", stagedDir, removeErr))
		}
	}()

	if err = r.buildCopySchema(ctx, layout, scratch, relayoutCopyPreflight, "preflight/schema", &report.Timings); err != nil {
		return report, err
	}
	started := time.Now()
	dump, err := r.stageDump(ctx, "preflight/data", relayoutDataSections)
	report.Timings.DataDump = time.Since(started)
	if err != nil {
		return report, err
	}
	if size, sizeErr := r.Primary.Shell(ctx, "du -sk "+shellQuote(dump.Dir)+" | cut -f 1"); sizeErr == nil {
		if kilobytes, parseErr := strconv.ParseInt(strings.TrimSpace(size), 10, 64); parseErr == nil {
			report.DumpBytes = kilobytes * 1024
		}
	}
	if err = r.requireSameSchema(ctx, scratch, &report.Timings); err != nil {
		return report, err
	}
	if err = r.loadData(ctx, scratch, dump, &report.Timings); err != nil {
		return report, err
	}
	started = time.Now()
	placement, err := r.Primary.Query(ctx, scratch, YugabyteRelationPlacementQuery)
	if err != nil {
		return report, fmt.Errorf("read %s placement: %w", scratch, err)
	}
	relations, err := ParseYugabyteRelationPlacements(placement)
	if err != nil {
		return report, err
	}
	colocated, err := r.Primary.Query(ctx, scratch, "SELECT yb_is_database_colocated()")
	if err != nil {
		return report, err
	}
	report.PlacementDrift = YugabytePlacementDrift(layout, strings.TrimSpace(colocated) == "t", relations)
	for i, probe := range probes {
		if _, probeErr := r.Primary.Query(ctx, scratch, fmt.Sprintf("SET ROLE %s;\n%s;\nRESET ROLE;", relayoutIdentifier(runtimeRole), probe)); probeErr != nil {
			report.ProbeFailures = append(report.ProbeFailures, fmt.Sprintf("probe %d as %s: %v", i+1, runtimeRole, probeErr))
		}
	}
	report.WindowChecks = time.Since(started)
	started = time.Now()
	evidence, err := r.CollectEvidence(ctx, scratch)
	report.ChecksumDuration = time.Since(started)
	if err != nil {
		return report, err
	}
	started = time.Now()
	if err = r.rehearseWindowChecks(ctx, scratch); err != nil {
		return report, err
	}
	report.WindowChecks += time.Since(started)
	report.Tables = len(evidence.Tables)
	if len(report.PlacementDrift) > 0 || len(report.ProbeFailures) > 0 {
		problems := append(append([]string{}, report.PlacementDrift...), report.ProbeFailures...)
		return report, fmt.Errorf("rehearsal copy of %s is not usable:\n%s", r.Database, strings.Join(problems, "\n"))
	}
	return report, nil
}

// EstimatedDowntime is how long services are fenced from the rehearsed database: comparing schemas, staging and
// loading the data dump, checksumming both databases once before the renames, and the checks cutover runs inside
// the window. The schema phases run before the fence and are not part of it.
func (p *RelayoutPreflight) EstimatedDowntime() time.Duration {
	t := p.Timings
	return t.Compare + t.DataDump + t.Data + 2*p.ChecksumDuration + p.WindowChecks
}

// rehearseWindowChecks runs the checks cutover makes inside the window, as often as it makes them, with the fenced
// scratch copy standing in for the source and the shadow, so preflight measures their cost on this cluster.
func (r *YugabyteRelayout) rehearseWindowChecks(ctx context.Context, scratch string) error {
	for range relayoutWindowTopologyChecks {
		if err := r.requireLiveTopology(ctx); err != nil {
			return err
		}
	}
	if err := r.requireNoConnectionManager(ctx); err != nil {
		return err
	}
	for range relayoutWindowSessionProofs {
		if err := r.requireFenced(ctx, scratch); err != nil {
			return err
		}
	}
	return nil
}

func (r *YugabyteRelayout) dropDatabaseIfExists(ctx context.Context, name string) error {
	exists, err := r.databaseExists(ctx, name)
	if err != nil || !exists {
		return err
	}
	if err = r.terminateSessions(ctx, name); err != nil {
		return err
	}
	if err = r.requireNoSessions(ctx, name); err != nil {
		return err
	}
	if err = r.requireLease(ctx); err != nil {
		return err
	}
	if _, err = r.admin(ctx, "DROP DATABASE "+relayoutIdentifier(name)); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return nil
}

func (r *YugabyteRelayout) introspect(ctx context.Context, database string) ([]string, error) {
	out, err := r.Primary.Query(ctx, database, relayoutIntrospectionQuery)
	if err != nil {
		return nil, fmt.Errorf("introspect %s: %w", database, err)
	}
	privileges, err := r.Primary.Query(ctx, database, relayoutPrivilegeQuery)
	if err != nil {
		return nil, fmt.Errorf("read %s privileges: %w", database, err)
	}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSpace(out)+"\n"+strings.TrimSpace(privileges), "\n") {
		if strings.TrimSpace(line) == "" || slices.ContainsFunc(relayoutGrantLinePrefixes, func(prefix string) bool { return strings.HasPrefix(line, prefix) }) {
			continue
		}
		lines = append(lines, normalizeRelayoutViewLine(normalizeYugabyteIndexKeyOrder(normalizeArrayCasts(line))))
	}
	sort.Strings(lines)
	return lines, nil
}

var (
	// A multi-column hash key prints as ((a, b) HASH); a single parenthesized expression key such as ((1) HASH) has no
	// comma and keeps its parentheses, matching its range-sharded ((1) ASC) form.
	yugabyteHashKeyGroupPattern = regexp.MustCompile(`\(\(([^()]+,[^()]+)\) HASH([,)])`)
	yugabyteKeyOrderPattern     = regexp.MustCompile(` (?:HASH|ASC)([,)])`)
)

// normalizeYugabyteIndexKeyOrder removes HASH and ASC key markers from index definitions. Colocated tables are range
// sharded, so the same logical index prints its leading key as HASH in a distributed database and as ASC in a
// colocated one. DESC stays significant, and constraint definitions carry no sharding markers.
func normalizeYugabyteIndexKeyOrder(line string) string {
	if !strings.HasPrefix(line, "idx|") {
		return line
	}
	line = yugabyteHashKeyGroupPattern.ReplaceAllString(line, "($1$2")
	return yugabyteKeyOrderPattern.ReplaceAllString(line, "$1")
}

// normalizeArrayCasts rewrites (ARRAY[a, b])::T[] as ARRAY[(a)::T, (b)::T]. A CHECK (col IN ('a', 'b')) created from
// DDL is stored with the whole-array cast, and ysql_dump prints that form; restoring it parses the cast onto each
// element, so the restored copy prints the per-element form. Both mean the same array.
func normalizeArrayCasts(line string) string {
	const head = "(ARRAY["
	var out strings.Builder
	rest := line
	for {
		start := strings.Index(rest, head)
		if start < 0 {
			out.WriteString(rest)
			return out.String()
		}
		elements, typeName, consumed, ok := parseArrayCast(rest[start+len(head):])
		if !ok {
			out.WriteString(rest[:start+len(head)])
			rest = rest[start+len(head):]
			continue
		}
		out.WriteString(rest[:start])
		out.WriteString("ARRAY[")
		for i, element := range elements {
			if i > 0 {
				out.WriteString(", ")
			}
			out.WriteString("(" + element + ")::" + typeName)
		}
		out.WriteString("]")
		rest = rest[start+len(head)+consumed:]
	}
}

// relayoutIntrospectionQuery is the schema introspection query with full view definitions, one line each, in place
// of their md5 digests. View definitions carry the same array casts as constraints, so they are compared after
// normalization, and a schema difference shows the definitions themselves. Definitions are printed unprettied: the
// pretty form drops the parentheses that tell a whole-array cast from a per-element one.
var relayoutIntrospectionQuery = func() string {
	const digest = "md5(pg_get_viewdef(c.oid, true))"
	if !strings.Contains(PostgresSchemaIntrospectionQuery, digest) {
		panic("schema introspection query no longer digests view definitions with " + digest)
	}
	// Baseline convergence skips the ledgers, whose rows differ between an upgraded and a fresh database. A relayout
	// compares a database with its own copy, so the ledgers' columns, indexes, and constraints must match as well.
	const ledgers = "('_migrations', '_schema_baseline')"
	if !strings.Contains(PostgresSchemaIntrospectionQuery, ledgers) {
		panic("schema introspection query no longer excludes the ledgers as " + ledgers)
	}
	query := strings.ReplaceAll(PostgresSchemaIntrospectionQuery, ledgers, "('')")
	return strings.Replace(query, digest, `replace(pg_get_viewdef(c.oid, false), E'\n', ' ')`, 1)
}()

// normalizeRelayoutViewLine normalizes array casts and whitespace in the definition of a
// view|schema|name|kind|definition|owner line.
func normalizeRelayoutViewLine(line string) string {
	if !strings.HasPrefix(line, "view|") {
		return line
	}
	fields := strings.SplitN(line, "|", 5)
	if len(fields) < 5 {
		return line
	}
	ownerAt := strings.LastIndex(fields[4], "|")
	if ownerAt < 0 {
		return line
	}
	definition := strings.Join(strings.Fields(normalizeArrayCasts(fields[4][:ownerAt])), " ")
	return strings.Join(fields[:4], "|") + "|" + definition + fields[4][ownerAt:]
}

// parseArrayCast reads "e1, e2])::type[]" and returns the elements, the element type, and the bytes consumed. Commas
// and brackets inside quoted literals or nested parentheses do not split elements.
func parseArrayCast(s string) ([]string, string, int, bool) {
	var elements []string
	depth, elementStart := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			for i++; i < len(s); i++ {
				if s[i] != '\'' {
					continue
				}
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				break
			}
		case '(', '[':
			depth++
		case ')':
			depth--
		case ']':
			if depth > 0 {
				depth--
				continue
			}
			elements = append(elements, strings.TrimSpace(s[elementStart:i]))
			suffix := s[i+1:]
			if !strings.HasPrefix(suffix, ")::") {
				return nil, "", 0, false
			}
			typeEnd := strings.Index(suffix, "[]")
			if typeEnd < 0 {
				return nil, "", 0, false
			}
			typeName := suffix[len(")::"):typeEnd]
			if typeName == "" || strings.ContainsAny(typeName, "()[],'") {
				return nil, "", 0, false
			}
			return elements, typeName, i + 1 + typeEnd + len("[]"), true
		case ',':
			if depth == 0 {
				elements = append(elements, strings.TrimSpace(s[elementStart:i]))
				elementStart = i + 1
			}
		}
	}
	return nil, "", 0, false
}

func (r *YugabyteRelayout) schemaDifferences(ctx context.Context, source, shadow string) ([]string, error) {
	sourceLines, err := r.introspect(ctx, source)
	if err != nil {
		return nil, err
	}
	shadowLines, err := r.introspect(ctx, shadow)
	if err != nil {
		return nil, err
	}
	return diffSortedLines(sourceLines, shadowLines), nil
}

func diffSortedLines(source, shadow []string) []string {
	inShadow := make(map[string]int, len(shadow))
	for _, line := range shadow {
		inShadow[line]++
	}
	var differences []string
	for _, line := range source {
		if inShadow[line] > 0 {
			inShadow[line]--
			continue
		}
		differences = append(differences, "- "+line)
	}
	for _, line := range shadow {
		if inShadow[line] > 0 {
			inShadow[line]--
			differences = append(differences, "+ "+line)
		}
	}
	sort.Strings(differences)
	return differences
}

func truncateLines(lines []string, limit int) []string {
	if len(lines) <= limit {
		return lines
	}
	return append(append([]string{}, lines[:limit]...), fmt.Sprintf("... %d more", len(lines)-limit))
}

// CollectEvidence fingerprints one database: schema digest, per-table row counts and checksums, sequence state,
// trigger enablement, constraint validation, and database colocation.
func (r *YugabyteRelayout) CollectEvidence(ctx context.Context, database string) (*RelayoutEvidence, error) {
	schema, err := r.introspect(ctx, database)
	if err != nil {
		return nil, err
	}
	out, err := r.Primary.Query(ctx, database, relayoutEvidenceSQL)
	if err != nil {
		return nil, fmt.Errorf("collect %s evidence: %w", database, err)
	}
	digest := sha256.Sum256([]byte(strings.Join(schema, "\n")))
	evidence, err := parseRelayoutEvidence(database, out)
	if err != nil {
		return nil, err
	}
	evidence.SchemaDigest = hex.EncodeToString(digest[:])
	return evidence, nil
}

func parseRelayoutEvidence(database, output string) (*RelayoutEvidence, error) {
	evidence := &RelayoutEvidence{
		Database: database, Tables: map[string]string{}, Sequences: map[string]string{},
		Triggers: map[string]string{}, Constraints: map[string]string{}, Attributes: map[string]string{},
	}
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "|")
		want := map[string]int{"colocated": 2, "internal_triggers_disabled": 2, "trigger": 3, "constraint": 3, "attribute": 3, "sequence": 4, "table": 4}[fields[0]]
		if want == 0 || len(fields) != want {
			return nil, fmt.Errorf("unexpected %s evidence row %q", database, line)
		}
		switch fields[0] {
		case "colocated":
			evidence.Colocated = fields[1] == "t"
		case "internal_triggers_disabled":
			count, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("parse %s internal trigger count: %w", database, err)
			}
			evidence.InternalTriggersDisabled = count
		case "trigger":
			evidence.Triggers[fields[1]] = fields[2]
		case "constraint":
			evidence.Constraints[fields[1]] = fields[2]
		case "attribute":
			evidence.Attributes[fields[1]] = fields[2]
		case "sequence":
			evidence.Sequences[fields[1]] = fields[2] + "|" + fields[3]
		case "table":
			evidence.Tables[fields[1]] = fields[2] + "|" + fields[3]
		}
	}
	return evidence, nil
}

// CompareRelayoutEvidence lists every data-integrity difference between a source and its shadow. Colocation is
// expected to differ and is checked against the layout separately.
func CompareRelayoutEvidence(source, shadow *RelayoutEvidence) []string {
	var differences []string
	if source.SchemaDigest != shadow.SchemaDigest {
		differences = append(differences, fmt.Sprintf("schema digest %s differs from %s", shadow.SchemaDigest, source.SchemaDigest))
	}
	for _, group := range []struct {
		kind           string
		source, shadow map[string]string
	}{
		{"table", source.Tables, shadow.Tables},
		{"sequence", source.Sequences, shadow.Sequences},
		{"trigger", source.Triggers, shadow.Triggers},
		{"constraint", source.Constraints, shadow.Constraints},
		{"attribute", source.Attributes, shadow.Attributes},
	} {
		for name, want := range group.source {
			got, ok := group.shadow[name]
			switch {
			case !ok:
				differences = append(differences, fmt.Sprintf("%s %s is missing", group.kind, name))
			case got != want:
				differences = append(differences, fmt.Sprintf("%s %s is %s, source is %s", group.kind, name, got, want))
			}
		}
		for name := range group.shadow {
			if _, ok := group.source[name]; !ok {
				differences = append(differences, fmt.Sprintf("%s %s is not in the source", group.kind, name))
			}
		}
	}
	if shadow.InternalTriggersDisabled != source.InternalTriggersDisabled {
		differences = append(differences, fmt.Sprintf("%d internal triggers are disabled, source has %d", shadow.InternalTriggersDisabled, source.InternalTriggersDisabled))
	}
	sort.Strings(differences)
	return differences
}

// Differences lists why a receipt does not prove a faithful copy in the declared layout.
func (receipt *RelayoutReceipt) Differences() []string {
	differences := CompareRelayoutEvidence(&receipt.Source, &receipt.ShadowEvidence)
	for _, drift := range receipt.PlacementDrift {
		differences = append(differences, "placement: "+drift)
	}
	for _, failure := range receipt.ProbeFailures {
		differences = append(differences, "capability: "+failure)
	}
	return differences
}

// DatabaseLayoutSHA256 hashes the embedded layout file of a logical database.
func DatabaseLayoutSHA256(database string) (string, error) {
	data, err := dbsql.Content.ReadFile(path.Join("layout", database+".yaml"))
	if err != nil {
		return "", fmt.Errorf("read %s layout: %w", database, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (r *YugabyteRelayout) collectReceipt(ctx context.Context, layout *DatabaseLayout, record *RelayoutRecord, runtimeRole string, probes []string) (*RelayoutReceipt, error) {
	shadow := r.ShadowName()
	source, err := r.CollectEvidence(ctx, r.Database)
	if err != nil {
		return nil, err
	}
	shadowEvidence, err := r.CollectEvidence(ctx, shadow)
	if err != nil {
		return nil, err
	}
	placement, err := r.Primary.Query(ctx, shadow, YugabyteRelationPlacementQuery)
	if err != nil {
		return nil, fmt.Errorf("read %s placement: %w", shadow, err)
	}
	relations, err := ParseYugabyteRelationPlacements(placement)
	if err != nil {
		return nil, err
	}
	layoutSHA, err := DatabaseLayoutSHA256(layout.Database)
	if err != nil {
		return nil, err
	}
	receipt := &RelayoutReceipt{
		Database: r.Database, Shadow: shadow, DumpSHA256: record.DumpSHA256, LayoutSHA256: layoutSHA,
		Source: *source, ShadowEvidence: *shadowEvidence,
		PlacementDrift:   YugabytePlacementDrift(layout, shadowEvidence.Colocated, relations),
		CapabilityProbes: len(probes), VerifiedAt: time.Now().UTC(),
	}
	for i, probe := range probes {
		if _, probeErr := r.Primary.Query(ctx, shadow, fmt.Sprintf("SET ROLE %s;\n%s;\nRESET ROLE;", relayoutIdentifier(runtimeRole), probe)); probeErr != nil {
			receipt.ProbeFailures = append(receipt.ProbeFailures, fmt.Sprintf("probe %d as %s: %v", i+1, runtimeRole, probeErr))
		}
	}
	return receipt, nil
}

// Verify proves the restored shadow is a faithful copy in the declared layout and persists the receipt.
func (r *YugabyteRelayout) Verify(ctx context.Context, layout *DatabaseLayout, runtimeRole string, probes []string) (*RelayoutReceipt, error) {
	return r.verify(ctx, layout, runtimeRole, probes, true)
}

// verify proves and persists the receipt. proveFence is false only when cutover verifies straight after the copy it
// ran in the same invocation, which proved the topology and both fences moments earlier; each rename proves again.
func (r *YugabyteRelayout) verify(ctx context.Context, layout *DatabaseLayout, runtimeRole string, probes []string, proveFence bool) (*RelayoutReceipt, error) {
	record, err := r.Record(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.requireState(record, RelayoutRestored, RelayoutVerified); err != nil {
		return nil, err
	}
	if proveFence {
		if err = r.requireLiveTopology(ctx); err != nil {
			return nil, err
		}
		for _, database := range []string{r.Database, r.ShadowName()} {
			if err = r.requireFenced(ctx, database); err != nil {
				return nil, err
			}
		}
	}
	receipt, err := r.collectReceipt(ctx, layout, record, runtimeRole, probes)
	if err != nil {
		return nil, err
	}
	if differences := receipt.Differences(); len(differences) > 0 {
		return receipt, fmt.Errorf("shadow %s does not match %s:\n%s", r.ShadowName(), r.Database, strings.Join(truncateLines(differences, 40), "\n"))
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if err = r.advance(ctx, []RelayoutState{RelayoutRestored, RelayoutVerified}, RelayoutVerified,
		map[string]string{"receipt": relayoutLiteral(string(encoded)) + "::jsonb"}); err != nil {
		return nil, err
	}
	return receipt, nil
}

// renameDatabase renames from to to once no tserver serves a session on from.
func (r *YugabyteRelayout) renameDatabase(ctx context.Context, from, to string) error {
	if err := r.terminateSessions(ctx, from); err != nil {
		return err
	}
	if err := r.requireNoSessions(ctx, from); err != nil {
		return err
	}
	if err := r.requireLease(ctx); err != nil {
		return err
	}
	if _, err := r.admin(ctx, fmt.Sprintf("ALTER DATABASE %s RENAME TO %s", relayoutIdentifier(from), relayoutIdentifier(to))); err != nil {
		return fmt.Errorf("rename %s to %s: %w", from, to, err)
	}
	return nil
}

// restoreAccess applies the recorded source ACL to database and proves every recorded connect grantee can connect.
func (r *YugabyteRelayout) restoreAccess(ctx context.Context, database, acl string) error {
	_, isDefault, err := parseDatabaseACL(acl)
	if err != nil {
		return err
	}
	owner, err := r.admin(ctx, "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = "+relayoutLiteral(database))
	if err != nil {
		return err
	}
	if owner == "" {
		return fmt.Errorf("database %s does not exist", database)
	}
	var grants []relayoutACLGrant
	if !isDefault {
		out, queryErr := r.admin(ctx, fmt.Sprintf(relayoutACLGrantsQuery, relayoutLiteral(acl)))
		if queryErr != nil {
			return fmt.Errorf("expand recorded ACL of %s: %w", database, queryErr)
		}
		if grants, err = parseRelayoutACLGrants(out); err != nil {
			return err
		}
	}
	statements := databaseACLGrantStatements(database, owner, isDefault, grants)
	if err = r.requireLease(ctx); err != nil {
		return err
	}
	if _, err = r.admin(ctx, strings.Join(statements, ";\n")+";"); err != nil {
		return fmt.Errorf("restore access to %s: %w", database, err)
	}
	if isDefault {
		// A default ACL leaves nothing to compare against, so prove the effective privileges it stands for.
		for _, grantee := range []struct{ role, privileges string }{
			{owner, "CREATE, CONNECT, TEMPORARY"},
			{"public", "CONNECT, TEMPORARY"},
		} {
			missing, queryErr := r.admin(ctx, fmt.Sprintf("SELECT string_agg(p, ', ') FROM unnest(ARRAY[%s]) p WHERE NOT has_database_privilege(%s, %s, p)",
				"'"+strings.ReplaceAll(grantee.privileges, ", ", "', '")+"'", relayoutLiteral(grantee.role), relayoutLiteral(database)))
			if queryErr != nil {
				return queryErr
			}
			if missing != "" {
				return fmt.Errorf("%s did not regain %s on %s", grantee.role, missing, database)
			}
		}
	} else {
		mismatches, queryErr := r.admin(ctx, fmt.Sprintf(relayoutACLMismatchQuery, relayoutLiteral(acl), relayoutLiteral(database)))
		if queryErr != nil {
			return fmt.Errorf("compare restored ACL of %s: %w", database, queryErr)
		}
		if mismatches != "0" {
			return fmt.Errorf("restored ACL of %s differs from the recorded %s in %s privilege(s)", database, acl, mismatches)
		}
	}
	grantees, err := databaseACLConnectGrantees(acl)
	if err != nil {
		return err
	}
	for _, grantee := range grantees {
		role := grantee
		if role == "" {
			role = "public"
		}
		out, queryErr := r.admin(ctx, fmt.Sprintf("SELECT has_database_privilege(%s, %s, 'CONNECT')", relayoutLiteral(role), relayoutLiteral(database)))
		if queryErr != nil {
			return queryErr
		}
		if out != "t" {
			return fmt.Errorf("%s did not regain CONNECT on %s", role, database)
		}
	}
	return nil
}

// Cutover runs the whole window services spend fenced, in one invocation so no operator pause lengthens it: from a
// prepared shadow it fences the source, copies the data, verifies the copy, renames the source aside and the shadow
// into place, and restores access. Each step resumes from the journal. When verification ran in this invocation it is
// not repeated; a receipt from an earlier invocation is recomputed and the renames refuse when the source or shadow
// changed since. It resumes from renamed_old or renamed_new by observing which databases exist, so an interruption
// between the two renames never loses the canonical name. Access is restored only once the shadow holds the canonical
// name.
func (r *YugabyteRelayout) Cutover(ctx context.Context, layout *DatabaseLayout, runtimeRole string, probes []string) error {
	record, err := r.Record(ctx)
	if err != nil {
		return err
	}
	if err = r.requireState(record, RelayoutPrepared, RelayoutFenced, RelayoutDumped, RelayoutRestored, RelayoutVerified,
		RelayoutRenamedOld, RelayoutRenamedNew, RelayoutAccessRestored); err != nil {
		return err
	}
	if record.State != RelayoutAccessRestored {
		if err = r.requireLiveTopology(ctx); err != nil {
			return err
		}
	}
	if record.State == RelayoutPrepared {
		// Checked again inside the fence; this check refuses a drifted schema while services still have access.
		if err = r.requireSameSchema(ctx, r.ShadowName(), &RelayoutCopyTimings{}); err != nil {
			return fmt.Errorf("%w\nthe schema of %s changed after prepare; run prepare again (services were not interrupted)", err, r.Database)
		}
	}
	if record.State == RelayoutPrepared || record.State == RelayoutFenced {
		if err = r.Fence(ctx); err != nil {
			return err
		}
	}
	copied := false
	if record.State == RelayoutPrepared || record.State == RelayoutFenced || record.State == RelayoutDumped {
		if err = r.Copy(ctx); err != nil {
			return err
		}
		if record, err = r.Record(ctx); err != nil {
			return err
		}
		copied = true
	}
	state := record.State

	reverify := true
	if state == RelayoutRestored {
		if _, err = r.verify(ctx, layout, runtimeRole, probes, !copied); err != nil {
			return err
		}
		if record, err = r.Record(ctx); err != nil {
			return err
		}
		state, reverify = record.State, false
	}

	if state == RelayoutVerified {
		if err = r.renameSourceAside(ctx, layout, record, runtimeRole, probes, reverify); err != nil {
			return err
		}
		if err = r.advance(ctx, []RelayoutState{RelayoutVerified}, RelayoutRenamedOld, nil); err != nil {
			return err
		}
		state = RelayoutRenamedOld
	}

	if state == RelayoutRenamedOld {
		if err = r.renameShadowIntoPlace(ctx, record); err != nil {
			return err
		}
		if err = r.advance(ctx, []RelayoutState{RelayoutRenamedOld}, RelayoutRenamedNew, nil); err != nil {
			return err
		}
		state = RelayoutRenamedNew
	}

	if state == RelayoutRenamedNew {
		if err = r.requireRelaidCopy(ctx, r.Database, record); err != nil {
			return err
		}
		if err = r.restoreAccess(ctx, r.Database, record.SourceACL); err != nil {
			return err
		}
		if err = r.advance(ctx, []RelayoutState{RelayoutRenamedNew}, RelayoutAccessRestored, nil); err != nil {
			return err
		}
	}
	return r.restoreSourceComment(ctx, record)
}

// renameSourceAside renames the source to its pre-relayout name, first recomputing the receipt when reverify is set
// because it was verified by an earlier invocation. A missing source next to an existing pre-relayout database means
// an earlier run renamed it before the journal recorded the step.
func (r *YugabyteRelayout) renameSourceAside(ctx context.Context, layout *DatabaseLayout, record *RelayoutRecord, runtimeRole string, probes []string, reverify bool) error {
	pre := r.PreRelayoutName()
	sourceExists, err := r.databaseExists(ctx, r.Database)
	if err != nil {
		return err
	}
	if !sourceExists {
		preExists, preErr := r.databaseExists(ctx, pre)
		if preErr != nil {
			return preErr
		}
		if !preExists {
			return fmt.Errorf("neither %s nor %s exists; inspect the cluster before resuming", r.Database, pre)
		}
		return r.requireOriginal(ctx, pre, record)
	}
	if err = r.requireOriginal(ctx, r.Database, record); err != nil {
		return err
	}
	if reverify {
		if err = r.reverify(ctx, layout, record, runtimeRole, probes); err != nil {
			return err
		}
	}
	return r.renameDatabase(ctx, r.Database, pre)
}

// renameShadowIntoPlace renames the shadow to the canonical name. A missing shadow next to an existing canonical
// database means an earlier run renamed it before the journal recorded the step, which holds only when the canonical
// database is the relaid copy.
func (r *YugabyteRelayout) renameShadowIntoPlace(ctx context.Context, record *RelayoutRecord) error {
	shadow := r.ShadowName()
	shadowExists, err := r.databaseExists(ctx, shadow)
	if err != nil {
		return err
	}
	canonicalExists, err := r.databaseExists(ctx, r.Database)
	if err != nil {
		return err
	}
	switch {
	case shadowExists && canonicalExists:
		return fmt.Errorf("both %s and %s exist; refusing to rename", shadow, r.Database)
	case shadowExists:
		return r.renameDatabase(ctx, shadow, r.Database)
	case !canonicalExists:
		return fmt.Errorf("neither %s nor %s exists; inspect the cluster before resuming", shadow, r.Database)
	}
	return r.requireRelaidCopy(ctx, r.Database, record)
}

func (r *YugabyteRelayout) reverify(ctx context.Context, layout *DatabaseLayout, record *RelayoutRecord, runtimeRole string, probes []string) error {
	if record.Receipt == nil {
		return fmt.Errorf("relayout of %s has no verification receipt", r.Database)
	}
	for _, database := range []string{r.Database, r.ShadowName()} {
		if err := r.requireFenced(ctx, database); err != nil {
			return err
		}
	}
	fresh, err := r.collectReceipt(ctx, layout, record, runtimeRole, probes)
	if err != nil {
		return err
	}
	differences := fresh.Differences()
	for _, difference := range CompareRelayoutEvidence(&record.Receipt.Source, &fresh.Source) {
		differences = append(differences, "source changed since verify: "+difference)
	}
	for _, difference := range CompareRelayoutEvidence(&record.Receipt.ShadowEvidence, &fresh.ShadowEvidence) {
		differences = append(differences, "shadow changed since verify: "+difference)
	}
	if record.Receipt.LayoutSHA256 != fresh.LayoutSHA256 {
		differences = append(differences, "layout file changed since verify")
	}
	if record.Receipt.DumpSHA256 != record.DumpSHA256 {
		differences = append(differences, "receipt was recorded for a different dump")
	}
	if len(differences) > 0 {
		return fmt.Errorf("cutover re-verification of %s failed; run verify again:\n%s", r.Database, strings.Join(truncateLines(differences, 40), "\n"))
	}
	return nil
}

// Rollback returns the original database to its canonical name with its recorded access, drops the relaid copy, and
// removes the staged dump. It finds the original by its recorded OID under whichever relayout name it holds, so a
// rollback that was interrupted, or a rename whose journal write was lost, resumes from what the cluster shows. It
// refuses once a relaid database may have served writes: the journal says access_restored or finished, or the relaid
// copy under the canonical name allows a role other than a superuser to connect. It is also how access is restored after a
// fence that revoked it but could not finish. Dead tservers do not block it, because it only renames, drops, and grants.
func (r *YugabyteRelayout) Rollback(ctx context.Context) error {
	record, err := r.Record(ctx)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("no relayout journal for %s", r.Database)
	}
	switch record.State {
	case RelayoutAccessRestored, RelayoutFinished:
		return fmt.Errorf("relayout of %s is %s; services may have written to the relaid database, so roll forward instead", r.Database, record.State)
	case RelayoutRolledBack:
		// A completed rollback can only have its staged dump left to remove.
		return r.removeRolledBackDump(ctx, record)
	case RelayoutPlanned:
		if record.SourceOID == "" {
			return fmt.Errorf("relayout of %s was never fenced; there is nothing to roll back", r.Database)
		}
	case RelayoutPrepared:
		if record.SourceOID == "" {
			// Services never lost access: only the prepared shadow has to go.
			if err = r.dropCopy(ctx, r.ShadowName(), relayoutCopyShadow); err != nil {
				return err
			}
			return r.advance(ctx, []RelayoutState{RelayoutPrepared}, RelayoutRolledBack, map[string]string{"receipt": "NULL", "pending_copy": "NULL"})
		}
	case RelayoutFenced, RelayoutDumped, RelayoutRestored, RelayoutVerified, RelayoutRenamedOld, RelayoutRenamedNew:
	default:
		return fmt.Errorf("relayout of %s is in unknown state %s", r.Database, record.State)
	}
	if record.SourceOID == "" || record.SourceACL == "" {
		return fmt.Errorf("relayout journal for %s records no source identity to roll back to", r.Database)
	}
	// Prove the dump can be removed before changing anything, so a wrong --dump-dir cannot stop a rollback halfway.
	if _, err = r.stagedDumpLocation(record); err != nil {
		return err
	}
	live, err := r.liveNodes(ctx)
	if err != nil {
		return err
	}
	rb := *r
	rb.Nodes = live
	if err = rb.returnOriginal(ctx, record); err != nil {
		return err
	}
	if err = rb.dropCopy(ctx, r.ShadowName(), relayoutCopyShadow); err != nil {
		return err
	}
	if err = rb.restoreAccess(ctx, r.Database, record.SourceACL); err != nil {
		return err
	}
	if err = rb.advance(ctx, []RelayoutState{record.State}, RelayoutRolledBack, map[string]string{
		"receipt": "NULL", "source_acl": "NULL", "source_oid": "NULL", "source_comment": "NULL", "pending_copy": "NULL",
	}); err != nil {
		return err
	}
	return rb.removeRolledBackDump(ctx, record)
}

// returnOriginal renames the original, found by its recorded OID, back to the canonical name. A relaid copy holding
// the canonical name is first renamed to the shadow name, but only while no role other than a superuser may connect
// to it.
func (r *YugabyteRelayout) returnOriginal(ctx context.Context, record *RelayoutRecord) error {
	names := []string{r.Database, r.PreRelayoutName(), r.ShadowName()}
	oids := map[string]string{}
	original := ""
	for _, name := range names {
		oid, err := r.admin(ctx, "SELECT oid::text FROM pg_database WHERE datname = "+relayoutLiteral(name))
		if err != nil {
			return err
		}
		if oid == "" {
			continue
		}
		oids[name] = oid
		if oid == record.SourceOID {
			original = name
		}
	}
	if original == "" {
		return fmt.Errorf("the original %s (oid %s) exists under none of %s; inspect the cluster", r.Database, record.SourceOID, strings.Join(names, ", "))
	}
	if original == r.Database {
		return nil
	}
	if _, taken := oids[r.Database]; taken {
		if err := r.requireRelaidCopy(ctx, r.Database, record); err != nil {
			return err
		}
		roles, err := r.connectableRoles(ctx, r.Database)
		if err != nil {
			return err
		}
		if len(roles) > 0 {
			return fmt.Errorf("the relaid %s already grants CONNECT to %s, so services may have written to it; roll forward with cutover instead",
				r.Database, strings.Join(roles, ", "))
		}
		if _, occupied := oids[r.ShadowName()]; occupied {
			return fmt.Errorf("both the relaid %s and %s exist; inspect the cluster", r.Database, r.ShadowName())
		}
		if err = r.renameDatabase(ctx, r.Database, r.ShadowName()); err != nil {
			return err
		}
	}
	return r.renameDatabase(ctx, original, r.Database)
}

// removeRolledBackDump removes the staged dump a rollback left and clears it from the journal. The original database
// is already back, so a failure here leaves nothing unsafe and the next rollback retries only this.
func (r *YugabyteRelayout) removeRolledBackDump(ctx context.Context, record *RelayoutRecord) error {
	if record.DumpPath == "" {
		return nil
	}
	if err := r.removeStagedDump(ctx, record); err != nil {
		return fmt.Errorf("%w\n%s is back under its name with its access restored; run cutover --rollback again to remove the staged dump", err, r.Database)
	}
	return r.record(ctx, []RelayoutState{RelayoutRolledBack}, map[string]string{
		"dump_path": "NULL", "dump_sha256": "NULL", "dump_host": "NULL",
	})
}

// liveNodes returns the relayout nodes whose tserver is live. Only a live tserver can hold a session, so rollback
// checks sessions there alone and a dead tserver does not block restoring access; every live tserver must still be
// matched by exactly one readable node.
func (r *YugabyteRelayout) liveNodes(ctx context.Context) ([]YugabyteNode, error) {
	out, err := r.admin(ctx, "SELECT host FROM yb_servers() ORDER BY 1")
	if err != nil {
		return nil, fmt.Errorf("list live tservers: %w", err)
	}
	servers := strings.Fields(out)
	claimedBy := map[string][]string{}
	var live []YugabyteNode
	for _, node := range r.nodes() {
		identities, identityErr := relayoutNodeIdentities(ctx, node)
		if identityErr != nil {
			continue
		}
		matched := false
		for _, server := range servers {
			if identities[server] {
				claimedBy[server] = append(claimedBy[server], node.Name())
				matched = true
			}
		}
		if matched {
			live = append(live, node)
		}
	}
	for _, server := range servers {
		if owners := claimedBy[server]; len(owners) != 1 {
			return nil, fmt.Errorf("live tserver %s is matched by %d readable relayout nodes %v, want exactly one", server, len(owners), owners)
		}
	}
	return live, nil
}

// Finish drops the pre-relayout database and the staged dump after a completed cutover.
func (r *YugabyteRelayout) Finish(ctx context.Context) error {
	record, err := r.Record(ctx)
	if err != nil {
		return err
	}
	if record != nil && record.State == RelayoutFinished {
		return nil
	}
	if err = r.requireState(record, RelayoutAccessRestored); err != nil {
		return err
	}
	if err = r.restoreSourceComment(ctx, record); err != nil {
		return err
	}
	pre := r.PreRelayoutName()
	preExists, err := r.databaseExists(ctx, pre)
	if err != nil {
		return err
	}
	if preExists {
		if err = r.requireOriginal(ctx, pre, record); err != nil {
			return err
		}
		if err = r.dropDatabaseIfExists(ctx, pre); err != nil {
			return err
		}
	}
	if err = r.removeStagedDump(ctx, record); err != nil {
		return err
	}
	return r.advance(ctx, []RelayoutState{RelayoutAccessRestored}, RelayoutFinished, map[string]string{
		"dump_path": "NULL", "dump_sha256": "NULL", "dump_host": "NULL",
		"source_acl": "NULL", "source_oid": "NULL", "source_comment": "NULL", "pending_copy": "NULL",
	})
}

type databaseACLItem struct {
	Grantee    string
	Privileges string
}

// relayoutACLGrant is one privilege of a recorded ACL as aclexplode reports it. Grantability belongs to each
// privilege, so an item such as role=C*c grants CREATE with grant option and CONNECT without it.
type relayoutACLGrant struct {
	// Grantee is a quote_ident role name, or PUBLIC.
	Grantee   string
	Privilege string
	Grantable bool
}

// relayoutACLGrantsQuery expands a recorded aclitem array into one row per privilege, in ACL order.
const relayoutACLGrantsQuery = `SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(a.grantee)) END,
       a.privilege_type, a.is_grantable
  FROM aclexplode(%s::aclitem[]) WITH ORDINALITY AS a(grantor, grantee, privilege_type, is_grantable, position)
 ORDER BY a.position`

// relayoutACLMismatchQuery counts privileges that differ between a recorded ACL and a database's current ACL for the
// grantees the recorded ACL names, comparing grantability per privilege and ignoring grantors.
const relayoutACLMismatchQuery = `WITH recorded AS (
  SELECT grantee, privilege_type, is_grantable FROM aclexplode(%s::aclitem[])
), current_acl AS (
  SELECT a.grantee, a.privilege_type, a.is_grantable
    FROM pg_database d, aclexplode(d.datacl) a
   WHERE d.datname = %s AND a.grantee IN (SELECT grantee FROM recorded)
)
SELECT (SELECT count(*) FROM (SELECT * FROM recorded EXCEPT SELECT * FROM current_acl) missing)
     + (SELECT count(*) FROM (SELECT * FROM current_acl EXCEPT SELECT * FROM recorded) extra)`

func parseRelayoutACLGrants(out string) ([]relayoutACLGrant, error) {
	var grants []relayoutACLGrant
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 3 || (fields[2] != "t" && fields[2] != "f") {
			return nil, fmt.Errorf("unexpected ACL privilege row %q", line)
		}
		switch fields[1] {
		case "CREATE", "TEMPORARY", "CONNECT":
		default:
			return nil, fmt.Errorf("ACL privilege row %q names an unsupported database privilege", line)
		}
		grants = append(grants, relayoutACLGrant{Grantee: fields[0], Privilege: fields[1], Grantable: fields[2] == "t"})
	}
	return grants, nil
}

// parseDatabaseACL parses pg_database.datacl text. relayoutDefaultACL stands for a NULL datacl, which grants the owner
// every privilege and PUBLIC CONNECT and TEMPORARY. Quoted role names are rejected rather than guessed.
func parseDatabaseACL(acl string) ([]databaseACLItem, bool, error) {
	acl = strings.TrimSpace(acl)
	if acl == "" || acl == relayoutDefaultACL {
		return nil, true, nil
	}
	if !strings.HasPrefix(acl, "{") || !strings.HasSuffix(acl, "}") {
		return nil, false, fmt.Errorf("database ACL %q is not an aclitem array", acl)
	}
	body := acl[1 : len(acl)-1]
	if body == "" {
		return nil, false, nil
	}
	if strings.ContainsAny(body, `"\`) {
		return nil, false, fmt.Errorf("database ACL %q contains quoted role names, which relayout does not restore", acl)
	}
	var items []databaseACLItem
	for _, raw := range strings.Split(body, ",") {
		grant, _, ok := strings.Cut(raw, "/")
		if !ok {
			return nil, false, fmt.Errorf("database ACL item %q has no grantor", raw)
		}
		grantee, privileges, ok := strings.Cut(grant, "=")
		if !ok {
			return nil, false, fmt.Errorf("database ACL item %q has no privileges", raw)
		}
		item := databaseACLItem{Grantee: grantee}
		for _, privilege := range privileges {
			switch privilege {
			case 'C', 'T', 'c':
				item.Privileges += string(privilege)
			case '*':
				// Grantability is restored per privilege from aclexplode; revocation and connect checks ignore it.
			default:
				return nil, false, fmt.Errorf("database ACL item %q has unknown privilege %q", raw, privilege)
			}
		}
		items = append(items, item)
	}
	return items, false, nil
}

// databaseACLGrantStatements rebuilds a recorded ACL on database from its per-privilege grants: one statement per
// grantee for the plain privileges and one WITH GRANT OPTION statement for the grantable ones. A default ACL is the
// state of a database nobody has granted on: the owner holds every privilege and PUBLIC holds CONNECT and TEMPORARY.
// Fencing revoked both, so restoring it grants them back explicitly.
func databaseACLGrantStatements(database, owner string, isDefault bool, grants []relayoutACLGrant) []string {
	target := relayoutIdentifier(database)
	statements := []string{"REVOKE ALL ON DATABASE " + target + " FROM PUBLIC"}
	if isDefault {
		return append(statements,
			"GRANT ALL ON DATABASE "+target+" TO "+relayoutIdentifier(owner),
			"GRANT CONNECT, TEMPORARY ON DATABASE "+target+" TO PUBLIC")
	}
	order := []string{"CREATE", "TEMPORARY", "CONNECT"}
	type grantee struct {
		plain, grantable map[string]bool
	}
	var names []string
	byGrantee := map[string]*grantee{}
	for _, grant := range grants {
		entry, ok := byGrantee[grant.Grantee]
		if !ok {
			entry = &grantee{plain: map[string]bool{}, grantable: map[string]bool{}}
			byGrantee[grant.Grantee] = entry
			names = append(names, grant.Grantee)
		}
		if grant.Grantable {
			entry.grantable[grant.Privilege] = true
		} else {
			entry.plain[grant.Privilege] = true
		}
	}
	for _, name := range names {
		entry := byGrantee[name]
		for _, set := range []struct {
			privileges map[string]bool
			suffix     string
		}{{entry.plain, ""}, {entry.grantable, " WITH GRANT OPTION"}} {
			var privileges []string
			for _, privilege := range order {
				if set.privileges[privilege] {
					privileges = append(privileges, privilege)
				}
			}
			if len(privileges) > 0 {
				statements = append(statements, fmt.Sprintf("GRANT %s ON DATABASE %s TO %s%s", strings.Join(privileges, ", "), target, name, set.suffix))
			}
		}
	}
	return statements
}

func databaseACLRevokeStatements(database, acl string, roles []string) ([]string, error) {
	items, _, err := parseDatabaseACL(acl)
	if err != nil {
		return nil, err
	}
	grantees := map[string]struct{}{}
	for _, role := range roles {
		if strings.TrimSpace(role) != "" {
			grantees[role] = struct{}{}
		}
	}
	for _, item := range items {
		if item.Grantee != "" {
			grantees[item.Grantee] = struct{}{}
		}
	}
	names := make([]string, 0, len(grantees))
	for grantee := range grantees {
		names = append(names, grantee)
	}
	sort.Strings(names)
	target := relayoutIdentifier(database)
	statements := []string{"REVOKE ALL ON DATABASE " + target + " FROM PUBLIC"}
	for _, grantee := range names {
		statements = append(statements, "REVOKE ALL ON DATABASE "+target+" FROM "+relayoutIdentifier(grantee))
	}
	return statements, nil
}

// databaseACLConnectGrantees returns every grantee holding CONNECT, with "" standing for PUBLIC.
func databaseACLConnectGrantees(acl string) ([]string, error) {
	items, isDefault, err := parseDatabaseACL(acl)
	if err != nil {
		return nil, err
	}
	if isDefault {
		return []string{""}, nil
	}
	var grantees []string
	for _, item := range items {
		if strings.ContainsRune(item.Privileges, 'c') {
			grantees = append(grantees, item.Grantee)
		}
	}
	return grantees, nil
}
