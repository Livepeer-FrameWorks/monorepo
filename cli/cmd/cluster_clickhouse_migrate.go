package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newClusterClickHouseCmd groups ClickHouse-specific cluster operations.
func newClusterClickHouseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clickhouse",
		Short: "ClickHouse cluster operations (data migration)",
	}
	cmd.AddCommand(newClickHouseMigrateCmd())
	return cmd
}

// newClickHouseMigrateCmd is the data-migration command family. It moves periscope
// data from a source (old) ClickHouse node into the new Replicated cluster via the
// `remote()` table function over the WireGuard mesh, using the explicit migration
// catalog (no engine/name inference) and authoritative system.tables partition
// metadata.
func newClickHouseMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate periscope data into the new Replicated ClickHouse cluster",
		Long: `Move periscope data from a source ClickHouse node to the new Replicated
cluster. Subcommands run in order:

  backfill  bulk copy into the new node (idempotent replace; refreshable MVs stopped)
  sync      idempotent per-partition re-copy of the growing tail (staging + REPLACE)
  verify    FINAL count + content-hash parity (new vs source) + catalog coverage
  cutover   final sync, stop ingest, flip the write endpoint, drain Kafka lag

The source is supplied with --from (a manifest host key, e.g. yuga-eu-1; use
--from-port if it runs on a non-default native port); the destination is the
manifest's ClickHouse coordinator node.`,
	}
	cmd.PersistentFlags().String("from", "", "source ClickHouse host key to migrate FROM (e.g. yuga-eu-1)")
	cmd.PersistentFlags().Int("from-port", 0, "source ClickHouse native port (default: same as destination; set when source/dest run on swapped ports, e.g. a same-host docker drill)")
	cutover := newCHMigrateSubCmd("cutover", "Final sync + start refreshable MVs (requires ingest stopped)", runCHMigrateCutover)
	cutover.Flags().Bool("ingest-stopped", false, "confirm periscope-ingest is already stopped (Kafka buffering); required to run cutover")
	cmd.AddCommand(
		newCHMigrateSubCmd("backfill", "Idempotent bulk copy into the new node (refreshable MVs stopped)", runCHMigrateBackfill),
		newCHMigrateSubCmd("sync", "Idempotent per-partition re-copy of the tail", runCHMigrateSync),
		newCHMigrateSubCmd("verify", "FINAL count + content-hash parity + catalog coverage", runCHMigrateVerify),
		cutover,
	)
	return cmd
}

func newCHMigrateSubCmd(use, short string, run func(*cobra.Command, *chMigrateCtx) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			mctx, err := newCHMigrateCtx(cmd, rc)
			if err != nil {
				return err
			}
			return run(cmd, mctx)
		},
	}
}

// chMigrateCtx holds the resolved source/destination connection state shared by
// all migrate subcommands.
type chMigrateCtx struct {
	rc        *resolvedCluster
	db        string
	src       provisioner.RemoteSource // old node, as a remote() source
	dstRunner ssh.Runner               // SSH runner on the new (coordinator) node
	dstPort   int
	user      string
	pass      string
}

func newCHMigrateCtx(cmd *cobra.Command, rc *resolvedCluster) (*chMigrateCtx, error) {
	manifest := rc.Manifest
	ch := manifest.Infrastructure.ClickHouse
	if ch == nil || !ch.Enabled {
		return nil, fmt.Errorf("clickhouse is not enabled in the manifest")
	}
	fromKey := strings.TrimSpace(stringFlag(cmd, "from").Value)
	if fromKey == "" {
		return nil, fmt.Errorf("--from <source host key> is required (e.g. --from yuga-eu-1)")
	}
	if _, ok := manifest.GetHost(fromKey); !ok {
		return nil, fmt.Errorf("--from host %q not found in manifest hosts", fromKey)
	}
	srcMesh := manifestMeshHostname(manifest, fromKey)
	if srcMesh == "" {
		return nil, fmt.Errorf("could not resolve mesh hostname for source host %q", fromKey)
	}

	dstHost, ok := manifest.GetHost(ch.CoordinatorHost())
	if !ok {
		return nil, fmt.Errorf("clickhouse coordinator host %q not found", ch.CoordinatorHost())
	}
	dstHost.Name = firstNonEmpty(dstHost.Name, ch.CoordinatorHost())

	sharedEnv, err := rc.SharedEnv()
	if err != nil {
		return nil, fmt.Errorf("load manifest env_files: %w", err)
	}
	user := firstNonEmpty(sharedEnv["CLICKHOUSE_USER"], "frameworks")
	pass := sharedEnv["CLICKHOUSE_PASSWORD"]
	if pass == "" {
		user = "default"
	}
	db := "periscope"
	if len(ch.Databases) > 0 {
		db = ch.Databases[0]
	}

	// Source port defaults to the destination's native port (prod: both on 9000,
	// different hosts). --from-port overrides it for a same-host drill where the
	// old and new nodes are containers on swapped host ports.
	srcPort := ch.EffectivePort()
	fromPort, err := cmd.Flags().GetInt("from-port")
	if err != nil {
		return nil, err
	}
	if fromPort > 0 {
		srcPort = fromPort
	}

	sshKey := stringFlag(cmd, "ssh-key").Value
	pool := ssh.NewPool(120*time.Second, sshKey)
	runner, err := snapshotRunner(pool, sshKey, dstHost, 10*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("connect to destination %s: %w", dstHost.Name, err)
	}

	return &chMigrateCtx{
		rc:        rc,
		db:        db,
		src:       provisioner.RemoteSource{Host: srcMesh, Port: srcPort, DB: db, User: user, Pass: pass},
		dstRunner: runner,
		dstPort:   ch.EffectivePort(),
		user:      user,
		pass:      pass,
	}, nil
}

// chClientScript builds a `clickhouse-client` invocation on the destination node.
// The SQL is staged to a 0600 temp file and run via --queries-file (not --query),
// so neither the statement nor the SOURCE credentials embedded in remote(...)
// appear in the destination's process argv. The destination password rides
// CLICKHOUSE_PASSWORD env (also not argv). The heredoc delimiter is quoted, so the
// SQL is written verbatim with no shell expansion. `trap … EXIT` removes the file
// even if the shell is interrupted (a normal trailing rm leaks the cred file on a
// signal) — same pattern as provisioner.SSHCHExecutor. The script's exit status is
// clickhouse-client's (the trap's rm doesn't alter $?). All interpolated values use
// single-quote shell quoting (provisioner.ShellQuote) — NOT Go %q, which is
// double-quote syntax where $ and backticks in a password would still expand.
func (m *chMigrateCtx) chClientScript(sql string) string {
	pw := ""
	if m.pass != "" {
		pw = fmt.Sprintf("CLICKHOUSE_PASSWORD=%s ", provisioner.ShellQuote(m.pass))
	}
	return fmt.Sprintf(`f="$(mktemp "${TMPDIR:-/tmp}/fw-chmig.XXXXXX.sql")" || exit 1
trap 'rm -f "$f"' EXIT
chmod 600 "$f"
cat > "$f" <<'FW_CHMIG_EOF'
%s
FW_CHMIG_EOF
%sclickhouse-client --host 127.0.0.1 --port %d --user %s --database %s --multiquery --queries-file "$f"`,
		sql, pw, m.dstPort, provisioner.ShellQuote(m.user), provisioner.ShellQuote(m.db))
}

// exec runs SQL on the destination node and returns trimmed stdout (TSV).
func (m *chMigrateCtx) exec(ctx context.Context, sql string) (string, error) {
	res, err := m.dstRunner.RunScript(ctx, m.chClientScript(sql))
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("clickhouse-client exited %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

func (m *chMigrateCtx) execAll(ctx context.Context, stmts []string) error {
	for _, s := range stmts {
		if _, err := m.exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", firstLine(s), err)
		}
	}
	return nil
}

// --- backfill -------------------------------------------------------------

func runCHMigrateBackfill(cmd *cobra.Command, m *chMigrateCtx) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	cat := provisioner.PeriscopeMigrationCatalog

	// Stop refreshable (timer-driven) MVs so a refresh can't APPEND into a *_store
	// mid-copy. Insert-trigger MVs need no handling: copyTable lands rows via
	// staging + REPLACE PARTITION / EXCHANGE, none of which fires an MV.
	fmt.Fprintln(out, "Stopping refreshable MVs on the destination...")
	for _, mv := range cat.RefreshableMVs {
		if _, err := m.exec(ctx, provisioner.StopRefreshableViewSQL(m.db, mv)); err != nil {
			return fmt.Errorf("stop refreshable view %s: %w", mv, err)
		}
	}

	// Backfill uses the SAME idempotent partition-replace mechanics as sync, so a
	// partial failure is safe to re-run (REPLACE/EXCHANGE overwrite; additive
	// engines never double) — there is no separate non-idempotent bulk path.
	fmt.Fprintf(out, "Backfilling %d tables from %s (idempotent replace)...\n", len(cat.Tables), m.src.Host)
	for i, t := range cat.Tables {
		status, err := m.copyTable(ctx, t)
		if err != nil {
			return fmt.Errorf("backfill %s: %w", t, err)
		}
		fmt.Fprintf(out, "  [%d/%d] %s (%s)\n", i+1, len(cat.Tables), t, status)
	}
	ux.Success(out, "Backfill complete (re-runnable). Refreshable MVs remain stopped until cutover; run `verify` next.")
	return nil
}

// --- sync -----------------------------------------------------------------

func runCHMigrateSync(cmd *cobra.Command, m *chMigrateCtx) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	cat := provisioner.PeriscopeMigrationCatalog

	fmt.Fprintf(out, "Idempotent re-sync of %d tables from %s...\n", len(cat.Tables), m.src.Host)
	for _, t := range cat.Tables {
		status, err := m.copyTable(ctx, t)
		if err != nil {
			return fmt.Errorf("sync %s: %w", t, err)
		}
		fmt.Fprintf(out, "  %s (%s)\n", t, status)
	}
	ux.Success(out, "Sync complete — re-runnable; additive tables are REPLACEd, never doubled.")
	return nil
}

// copyTable idempotently copies one table from the source into the destination:
// partitioned tables per-partition via staging + ALTER REPLACE PARTITION ID,
// unpartitioned via staging + atomic EXCHANGE. Both are re-runnable after a
// partial failure (REPLACE/EXCHANGE overwrite the destination; additive engines
// never double), and neither fires an insert-trigger MV — so backfill and sync
// share this one mechanism. Returns a short human status for the progress line.
func (m *chMigrateCtx) copyTable(ctx context.Context, table string) (string, error) {
	partKey, err := m.partitionKey(ctx, table)
	if err != nil {
		return "", fmt.Errorf("partition key: %w", err)
	}
	if partKey == "" || partKey == "tuple()" {
		// Unpartitioned → atomic full-table replace via staging.
		if err = m.execAll(ctx, provisioner.FullReplaceTableSQL(m.src, m.db, table)); err != nil {
			return "", fmt.Errorf("full-replace: %w", err)
		}
		return "unpartitioned, full replace", nil
	}
	parts, err := m.sourcePartitions(ctx, table)
	if err != nil {
		return "", fmt.Errorf("enumerate partitions: %w", err)
	}
	for _, p := range parts {
		if err = m.execAll(ctx, provisioner.SyncPartitionSQL(m.src, m.db, table, p)); err != nil {
			return "", fmt.Errorf("partition %s: %w", p, err)
		}
	}
	// Drop the staging sibling so no __migstage artifact lingers in the DB.
	if _, err = m.exec(ctx, provisioner.DropStagingSQL(m.db, table)); err != nil {
		return "", fmt.Errorf("drop staging: %w", err)
	}
	return fmt.Sprintf("%d partitions", len(parts)), nil
}

// partitionKey reads the table's partition expression from system.tables (the
// authoritative live schema). Empty/tuple() means unpartitioned.
func (m *chMigrateCtx) partitionKey(ctx context.Context, table string) (string, error) {
	return m.exec(ctx, fmt.Sprintf(
		"SELECT partition_key FROM system.tables WHERE database = '%s' AND name = '%s'", m.db, table))
}

// sourcePartitions lists the active partition ids on the SOURCE node, read via
// remote() over the mesh (so the migration is driven by what the old node holds).
func (m *chMigrateCtx) sourcePartitions(ctx context.Context, table string) ([]string, error) {
	// partition_id is the stable, shape-agnostic key (correct for tuple partitions
	// like (toYYYYMM(ts), tenant_id) where the formatted `partition` value is not a
	// usable SQL literal).
	q := fmt.Sprintf(
		"SELECT DISTINCT partition_id FROM %s WHERE database = '%s' AND table = '%s' AND active",
		m.src.Remote("system", "parts"), m.db, table)
	res, err := m.exec(ctx, q)
	if err != nil {
		return nil, err
	}
	if res == "" {
		return nil, nil
	}
	return strings.Split(res, "\n"), nil
}

// --- verify ---------------------------------------------------------------

func runCHMigrateVerify(cmd *cobra.Command, m *chMigrateCtx) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	cat := provisioner.PeriscopeMigrationCatalog

	// Coverage: every periscope data table on the destination must be in the catalog.
	// Excluded: infra/bookkeeping tables (leading underscore — _schema_baseline,
	// _migrations, and __migstage staging siblings) which are node-local and never
	// cross-host migrated.
	live, err := m.exec(ctx, fmt.Sprintf(
		"SELECT name FROM system.tables WHERE database = '%s' AND engine LIKE 'Replicated%%' AND NOT startsWith(name, '_') ORDER BY name", m.db))
	if err != nil {
		return fmt.Errorf("list destination tables: %w", err)
	}
	known := map[string]bool{}
	for _, t := range cat.Tables {
		known[t] = true
	}
	var uncatalogued []string
	for t := range strings.SplitSeq(live, "\n") {
		if t != "" && !known[t] {
			uncatalogued = append(uncatalogued, t)
		}
	}
	if len(uncatalogued) > 0 {
		return fmt.Errorf("destination has tables not in the migration catalog (would be missed): %v", uncatalogued)
	}

	// Build a per-table hash-argument list from system.columns. AggregateFunction
	// columns are wrapped in finalizeAggregation() so cityHash64 can digest the
	// finalized value (it can't digest a raw aggregate state) — so aggregate-state
	// tables (e.g. api_usage_5m, *_store) get a real content hash, not a count-only
	// check that would pass on corrupted states. Read by COLUMN TYPE, not engine.
	cols, err := m.tableColumns(ctx)
	if err != nil {
		return fmt.Errorf("read column types: %w", err)
	}

	fmt.Fprintf(out, "Parity fingerprint (FINAL count + content hash, destination vs source) for %d tables...\n", len(cat.Tables))
	var mismatches, aggHashCount int
	for _, t := range cat.Tables {
		hashArgs, finalized := columnHashArgs(cols[t])
		if hashArgs == "" {
			return fmt.Errorf("no columns found for %s — cannot fingerprint", t)
		}
		if finalized {
			aggHashCount++
		}
		dstFP, derr := m.exec(ctx, provisioner.VerifyFingerprintSQL(m.db, t, hashArgs))
		if derr != nil {
			return fmt.Errorf("fingerprint %s on destination: %w", t, derr)
		}
		srcFP, serr := m.exec(ctx, provisioner.VerifyRemoteFingerprintSQL(m.src, t, hashArgs))
		if serr != nil {
			return fmt.Errorf("fingerprint %s on source: %w", t, serr)
		}
		if dstFP != srcFP {
			mismatches++
			fmt.Fprintf(out, "  MISMATCH %s: dst=[%s] src=[%s]\n", t, oneLineTSV(dstFP), oneLineTSV(srcFP))
		}
	}
	if mismatches > 0 {
		return fmt.Errorf("%d/%d tables differ — run `sync` and re-verify", mismatches, len(cat.Tables))
	}
	ux.Success(out, fmt.Sprintf("Parity OK across %d tables (content-hashed; %d use finalizeAggregation for aggregate-state columns); catalog covers the live schema.",
		len(cat.Tables), aggHashCount))
	return nil
}

// chColumn is one column's name and ClickHouse type as reported by system.columns.
type chColumn struct {
	name string
	typ  string
}

// tableColumns maps each periscope table to its columns in position order.
func (m *chMigrateCtx) tableColumns(ctx context.Context) (map[string][]chColumn, error) {
	res, err := m.exec(ctx, fmt.Sprintf(
		"SELECT table, name, type FROM system.columns WHERE database = '%s' ORDER BY table, position", m.db))
	if err != nil {
		return nil, err
	}
	cols := map[string][]chColumn{}
	for row := range strings.SplitSeq(res, "\n") {
		parts := strings.SplitN(row, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		cols[parts[0]] = append(cols[parts[0]], chColumn{name: parts[1], typ: parts[2]})
	}
	return cols, nil
}

// columnHashArgs builds the cityHash64 argument list for one table's columns.
// An AggregateFunction(...) column is wrapped in finalizeAggregation(col) so the
// hash digests its finalized value (cityHash64 rejects a raw aggregate state);
// SimpleAggregateFunction and all other types pass through (cityHash64 handles
// them directly). Returns the comma-joined list and whether any column needed
// finalization. Backtick-quotes names so reserved words are safe.
func columnHashArgs(cols []chColumn) (string, bool) {
	args := make([]string, 0, len(cols))
	finalized := false
	for _, c := range cols {
		ref := "`" + c.name + "`"
		if strings.HasPrefix(c.typ, "AggregateFunction(") {
			ref = "finalizeAggregation(" + ref + ")"
			finalized = true
		}
		args = append(args, ref)
	}
	return strings.Join(args, ", "), finalized
}

func oneLineTSV(s string) string { return strings.ReplaceAll(s, "\t", " ") }

// --- cutover --------------------------------------------------------------

func runCHMigrateCutover(cmd *cobra.Command, m *chMigrateCtx) error {
	out := cmd.OutOrStdout()
	stopped, err := cmd.Flags().GetBool("ingest-stopped")
	if err != nil {
		return err
	}
	if !stopped {
		// Refuse: starting refreshable MVs / doing the "final" sync while ingest is
		// still writing the old node would re-enable APPEND views on incomplete data
		// and never converge. The operator stops ingest FIRST (Kafka buffers, no
		// loss), then runs cutover once.
		fmt.Fprintln(out, strings.TrimSpace(`
Cutover requires periscope-ingest to be STOPPED first (Kafka buffers; the consumer
offset freezes; no data loss). Then re-run with --ingest-stopped. Full sequence:

  1. Stop periscope-ingest.
  2. cluster clickhouse migrate cutover --from <src> --ingest-stopped
     (runs the final delta sync, then starts refreshable MVs once).
  3. gitops: set clickhouse.write_endpoint -> new node; re-provision + resume
     periscope-ingest (--only-services periscope-ingest); watch Kafka lag drain to 0.
  4. gitops: set clickhouse.read_endpoint -> new node; re-provision periscope-query.
  5. Decommission the old ClickHouse; drop the endpoint overrides.`))
		return fmt.Errorf("refusing cutover: periscope-ingest not confirmed stopped (re-run with --ingest-stopped)")
	}

	fmt.Fprintln(out, "Final delta sync (ingest stopped)...")
	if err := runCHMigrateSync(cmd, m); err != nil {
		return fmt.Errorf("final sync: %w", err)
	}
	cat := provisioner.PeriscopeMigrationCatalog
	fmt.Fprintln(out, "Starting refreshable MVs on the destination...")
	for _, mv := range cat.RefreshableMVs {
		if _, err := m.exec(cmd.Context(), provisioner.StartRefreshableViewSQL(m.db, mv)); err != nil {
			return fmt.Errorf("start refreshable view %s: %w", mv, err)
		}
	}
	ux.Success(out, "Final sync done + refreshable MVs started. Now flip write_endpoint->new and resume "+
		"periscope-ingest (drain lag to 0), then flip read_endpoint->new; finally decommission the old node.")
	return nil
}

func firstLine(s string) string {
	if before, _, found := strings.Cut(s, "\n"); found {
		return before
	}
	if len(s) > 80 {
		return s[:80]
	}
	return s
}
