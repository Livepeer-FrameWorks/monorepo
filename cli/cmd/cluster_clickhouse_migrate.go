package cmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
  cutover   final delta sync, RE-VERIFY parity, then start refreshable MVs — requires
            ingest already stopped; a failed re-verify aborts before any MV starts.
            The write/read endpoint flips are operator gitops steps cutover PRINTS.

The source is supplied with --from (a manifest host key, e.g. yuga-eu-1; use
--from-port if it runs on a non-default native port); the destination is the
manifest's ClickHouse coordinator node.`,
	}
	cmd.PersistentFlags().String("from", "", "source ClickHouse host key to migrate FROM (e.g. yuga-eu-1)")
	cmd.PersistentFlags().Int("from-port", 0, "source ClickHouse native port (default: same as destination; set when source/dest run on swapped ports, e.g. a same-host docker drill)")
	cutover := newCHMigrateSubCmd("cutover", "Final sync + re-verify parity + start refreshable MVs (requires ingest stopped)", runCHMigrateCutover)
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

// srcClientScript is chClientScript pointed at the SOURCE node (over the mesh) instead of the local destination. Used
// for DDL that must run ON the source — `SYSTEM STOP/START VIEW` — since `remote()` cannot run DDL. It runs from the
// destination node's clickhouse-client, connecting to the source host/port with the source credentials (staged to a
// 0600 file, never in argv, exactly like chClientScript).
func (m *chMigrateCtx) srcClientScript(sql string) string {
	pw := ""
	if m.src.Pass != "" {
		pw = fmt.Sprintf("CLICKHOUSE_PASSWORD=%s ", provisioner.ShellQuote(m.src.Pass))
	}
	port := m.src.Port
	if port == 0 {
		port = 9000
	}
	return fmt.Sprintf(`f="$(mktemp "${TMPDIR:-/tmp}/fw-chmig.XXXXXX.sql")" || exit 1
trap 'rm -f "$f"' EXIT
chmod 600 "$f"
cat > "$f" <<'FW_CHMIG_EOF'
%s
FW_CHMIG_EOF
%sclickhouse-client --host %s --port %d --user %s --database %s --multiquery --queries-file "$f"`,
		sql, pw, provisioner.ShellQuote(m.src.Host), port, provisioner.ShellQuote(m.src.User), provisioner.ShellQuote(m.db))
}

// execOnSource runs DDL on the SOURCE node (via the destination's clickhouse-client) and returns trimmed stdout.
func (m *chMigrateCtx) execOnSource(ctx context.Context, sql string) (string, error) {
	res, err := m.dstRunner.RunScript(ctx, m.srcClientScript(sql))
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("clickhouse-client (source) exited %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
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

	// Stop refreshable (timer-driven) MVs, then WAIT until they settle to Disabled, so a refresh cannot APPEND into a
	// *_store mid-copy. STOP VIEW is not synchronous — an in-flight refresh runs until it finishes — so the stop alone is
	// insufficient; the bounded wait proves quiescence before the copy. Insert-trigger MVs need no handling: copyTable
	// lands rows via staging + REPLACE PARTITION / EXCHANGE, none of which fires an MV.
	fmt.Fprintln(out, "Stopping refreshable MVs on the destination...")
	for _, mv := range cat.RefreshableMVs {
		if err := execViewControl(ctx, m.exec, provisioner.StopRefreshableViewSQL(m.db, mv)); err != nil {
			return fmt.Errorf("stop refreshable view %s: %w", mv, err)
		}
	}
	fmt.Fprintln(out, "Waiting for destination refreshable MVs to quiesce (Disabled)...")
	if err := boundedWaitQuiesced(ctx, func(c context.Context) error {
		return m.checkNodeQuiesced(c, m.exec, "destination", cat.RefreshableMVs)
	}); err != nil {
		return fmt.Errorf("destination refreshable MVs did not quiesce before backfill: %w", err)
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
	// Cross-version copy: the destination is created from the NEW baseline while the source is the pre-release schema.
	plan, err := m.tablePlan(ctx, table)
	if err != nil {
		return "", fmt.Errorf("resolve copy plan: %w", err)
	}
	if !plan.sourceExists {
		// New-only table (absent on the source, e.g. artifact_node_copy_*): there is nothing to copy. It must be empty
		// on the freshly-baselined destination; assert that rather than silently skip, so unexpected pre-existing data
		// is caught.
		if err = m.assertDestinationEmpty(ctx, table); err != nil {
			return "", err
		}
		return "new table (absent on source), verified empty", nil
	}
	partKey, err := m.partitionKey(ctx, table)
	if err != nil {
		return "", fmt.Errorf("partition key: %w", err)
	}
	if partKey == "" || partKey == "tuple()" {
		// Unpartitioned → atomic full-table replace via staging.
		if err = m.execAll(ctx, provisioner.FullReplaceTableSQL(m.src, m.db, table, plan.insertCols, plan.selectExprs)); err != nil {
			return "", fmt.Errorf("full-replace: %w", err)
		}
		return "unpartitioned, full replace", nil
	}
	parts, err := m.sourcePartitions(ctx, table)
	if err != nil {
		return "", fmt.Errorf("enumerate source partitions: %w", err)
	}
	dstParts, err := m.destinationPartitions(ctx, table)
	if err != nil {
		return "", fmt.Errorf("enumerate destination partitions: %w", err)
	}
	for _, p := range parts {
		if err = m.execAll(ctx, provisioner.SyncPartitionSQL(m.src, m.db, table, p, plan.insertCols, plan.selectExprs)); err != nil {
			return "", fmt.Errorf("partition %s: %w", p, err)
		}
	}
	// Reconcile DESTINATION-ONLY partitions: a partition copied on an earlier run whose SOURCE counterpart has since
	// been removed. TTL deletion in ClickHouse runs asynchronously during background merges, so fully-expired data can
	// vanish from the source after it was copied while the destination partition remains. REPLACE PARTITION touches
	// only source partitions, so without an explicit DROP the stale destination partition survives and the documented
	// "rerun sync then verify" can never converge (verify keeps failing on the orphaned rows). Dropped on the
	// destination only (m.exec) — the source, by definition, no longer has it.
	stale := destinationOnlyPartitions(parts, dstParts)
	for _, d := range stale {
		if _, err = m.exec(ctx, provisioner.DropPartitionSQL(m.db, table, d)); err != nil {
			return "", fmt.Errorf("drop destination-only partition %s: %w", d, err)
		}
	}
	// Drop the staging sibling so no __migstage artifact lingers in the DB.
	if _, err = m.exec(ctx, provisioner.DropStagingSQL(m.db, table)); err != nil {
		return "", fmt.Errorf("drop staging: %w", err)
	}
	if len(stale) > 0 {
		return fmt.Sprintf("%d partitions (%d stale destination-only dropped)", len(parts), len(stale)), nil
	}
	return fmt.Sprintf("%d partitions", len(parts)), nil
}

// destinationOnlyPartitions returns the partition IDs present on the destination but ABSENT from the source — the stale
// partitions sync must DROP so the destination converges to the source after source-side removal (e.g. TTL expiry).
// Destination order is preserved. A pure set difference so the reconciliation is unit-testable without a live cluster.
func destinationOnlyPartitions(srcParts, dstParts []string) []string {
	srcSet := make(map[string]bool, len(srcParts))
	for _, p := range srcParts {
		srcSet[p] = true
	}
	var only []string
	for _, d := range dstParts {
		if !srcSet[d] {
			only = append(only, d)
		}
	}
	return only
}

// assertDestinationEmpty fails if a table has any rows on the destination — used for new-only tables (absent on the
// source) that the fresh baseline must not have populated before the copy.
func (m *chMigrateCtx) assertDestinationEmpty(ctx context.Context, table string) error {
	n, err := m.exec(ctx, fmt.Sprintf("SELECT count() FROM %s.%s", m.db, table))
	if err != nil {
		return fmt.Errorf("count %s: %w", table, err)
	}
	if strings.TrimSpace(n) != "0" {
		return fmt.Errorf("new-only table %s is absent on the source but already has %s row(s) on the destination — refusing (unexpected pre-existing data)", table, strings.TrimSpace(n))
	}
	return nil
}

// partitionKey reads the table's partition expression from system.tables (the
// authoritative live schema). Empty/tuple() means unpartitioned.
func (m *chMigrateCtx) partitionKey(ctx context.Context, table string) (string, error) {
	return m.exec(ctx, fmt.Sprintf(
		"SELECT partition_key FROM system.tables WHERE database = '%s' AND name = '%s'", m.db, table))
}

// tableCopyPlan is the per-table cross-version copy/verify plan derived from the SOURCE and DESTINATION schemas.
type tableCopyPlan struct {
	sourceExists bool     // false when the table is absent on the source (a new-only destination table)
	insertCols   []string // backtick-quoted destination columns present in BOTH schemas, destination order
	selectExprs  []string // matching source expressions: `col`, or CAST(`col` AS <destType>) when the type evolved
	dstHashArgs  string   // cityHash64 args over the shared columns on the DESTINATION (aggregates finalized)
	srcHashArgs  string   // cityHash64 args over the shared columns on the SOURCE, normalized to destination types
}

// tablePlan reads the source and destination column schemas and builds the copy/verify plan for one table.
func (m *chMigrateCtx) tablePlan(ctx context.Context, table string) (tableCopyPlan, error) {
	dstCols, err := m.columnsWithTypes(ctx, m.db, table, false)
	if err != nil {
		return tableCopyPlan{}, fmt.Errorf("destination columns: %w", err)
	}
	srcCols, err := m.columnsWithTypes(ctx, m.src.DB, table, true)
	if err != nil {
		return tableCopyPlan{}, fmt.Errorf("source columns: %w", err)
	}
	return buildTableCopyPlan(table, dstCols, srcCols)
}

// buildTableCopyPlan is the pure core of tablePlan: given the destination and source column schemas it computes the
// shared column set (destination order), the source SELECT expressions (a cast when a shared column's type evolved,
// e.g. DateTime -> DateTime64(3)), and the verify hash-argument lists (both sides normalized to the destination type so
// parity matches the copy). A table absent on the source (no source columns) yields sourceExists=false; a table that
// shares no columns is an error (it cannot be copied cross-version).
func buildTableCopyPlan(table string, dstCols, srcCols []chColumn) (tableCopyPlan, error) {
	if len(srcCols) == 0 {
		return tableCopyPlan{sourceExists: false}, nil
	}
	srcType := make(map[string]string, len(srcCols))
	for _, c := range srcCols {
		srcType[c.name] = c.typ
	}
	plan := tableCopyPlan{sourceExists: true}
	var dstHash, srcHash []string
	for _, c := range dstCols {
		st, ok := srcType[c.name]
		if !ok {
			continue // destination-only column: takes its DEFAULT on copy; excluded from parity
		}
		q := "`" + c.name + "`"
		plan.insertCols = append(plan.insertCols, q)

		if st == c.typ {
			plan.selectExprs = append(plan.selectExprs, q)
		} else {
			// Type evolved: cast the source value to the destination type so the copy lands the right representation.
			plan.selectExprs = append(plan.selectExprs, fmt.Sprintf("CAST(%s AS %s)", q, c.typ))
		}

		// Verify parity is computed over the SHARED columns, both normalized to the destination type: an
		// AggregateFunction column is finalized (cityHash64 can't digest a raw state); a type-evolved source column is
		// cast to the destination type to match what was copied.
		if strings.HasPrefix(c.typ, "AggregateFunction(") {
			dstHash = append(dstHash, "finalizeAggregation("+q+")")
			srcHash = append(srcHash, "finalizeAggregation("+q+")")
		} else {
			dstHash = append(dstHash, q)
			if st == c.typ {
				srcHash = append(srcHash, q)
			} else {
				srcHash = append(srcHash, fmt.Sprintf("CAST(%s AS %s)", q, c.typ))
			}
		}
	}
	if len(plan.insertCols) == 0 {
		return tableCopyPlan{}, fmt.Errorf("table %s has no columns common to the source and destination schemas", table)
	}
	plan.dstHashArgs = strings.Join(dstHash, ", ")
	plan.srcHashArgs = strings.Join(srcHash, ", ")
	return plan, nil
}

// columnsWithTypes lists a table's columns (name + type) in position order, from the destination's system.columns
// (fromRemote=false) or the source's via remote() over the mesh (fromRemote=true). A table absent on the queried node
// yields an empty slice (no rows), NOT an error.
func (m *chMigrateCtx) columnsWithTypes(ctx context.Context, db, table string, fromRemote bool) ([]chColumn, error) {
	from := "system.columns"
	if fromRemote {
		from = m.src.Remote("system", "columns")
	}
	res, err := m.exec(ctx, fmt.Sprintf(
		"SELECT name, type FROM %s WHERE database = '%s' AND table = '%s' ORDER BY position", from, db, table))
	if err != nil {
		return nil, err
	}
	var cols []chColumn
	for row := range strings.SplitSeq(res, "\n") {
		parts := strings.SplitN(row, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		cols = append(cols, chColumn{name: strings.TrimSpace(parts[0]), typ: strings.TrimSpace(parts[1])})
	}
	return cols, nil
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

// destinationPartitions lists the DESTINATION's active partition IDs for a table (local system.parts, no remote()).
// Compared against sourcePartitions during sync so a partition that only exists on the destination — copied earlier,
// then removed from the source — is detected and dropped.
func (m *chMigrateCtx) destinationPartitions(ctx context.Context, table string) ([]string, error) {
	q := fmt.Sprintf(
		"SELECT DISTINCT partition_id FROM system.parts WHERE database = '%s' AND table = '%s' AND active",
		m.db, table)
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

	// Parity is computed per table from its cross-version copy plan, over the SHARED columns only, with each side
	// normalized to the DESTINATION type (aggregate states finalized; a type-evolved source column cast to the
	// destination type) so the fingerprint matches what the copy actually lands. A table absent on the source (a
	// new-only destination table) has nothing to compare — it is asserted EMPTY instead.
	fmt.Fprintf(out, "Parity fingerprint (FINAL count + content hash, destination vs source) for %d tables...\n", len(cat.Tables))
	var mismatches, newOnly int
	for _, t := range cat.Tables {
		plan, perr := m.tablePlan(ctx, t)
		if perr != nil {
			return fmt.Errorf("plan %s: %w", t, perr)
		}
		if !plan.sourceExists {
			if err := m.assertDestinationEmpty(ctx, t); err != nil {
				return err
			}
			newOnly++
			continue
		}
		dstFP, derr := m.exec(ctx, provisioner.VerifyFingerprintSQL(m.db, t, plan.dstHashArgs))
		if derr != nil {
			return fmt.Errorf("fingerprint %s on destination: %w", t, derr)
		}
		srcFP, serr := m.exec(ctx, provisioner.VerifyRemoteFingerprintSQL(m.src, t, plan.srcHashArgs))
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
	ux.Success(out, fmt.Sprintf("Parity OK across %d tables (content-hashed over shared columns, destination-normalized; %d new-only tables verified empty); catalog covers the live schema.",
		len(cat.Tables), newOnly))
	return nil
}

// chColumn is one column's name and ClickHouse type as reported by system.columns.
type chColumn struct {
	name string
	typ  string
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

  1. Stop periscope-ingest and confirm its Kafka consumer lag is frozen.
  2. cluster clickhouse migrate cutover --from <src> --ingest-stopped
     (final delta sync, RE-VERIFY parity, then start refreshable MVs; a failed
     re-verify aborts before any MV starts).
  3. gitops: set clickhouse.write_endpoint -> new node; re-provision + resume
     periscope-ingest (--only-services periscope-ingest); watch Kafka lag drain to 0.
  4. gitops: set clickhouse.read_endpoint -> new node; re-provision periscope-query.
  5. Decommission the old ClickHouse; drop the endpoint overrides.`))
		return fmt.Errorf("refusing cutover: periscope-ingest not confirmed stopped (re-run with --ingest-stopped)")
	}

	if err := m.finishCutover(cmd, runCHMigrateSync, runCHMigrateVerify, m.cutoverViewControls(cmd)); err != nil {
		return err
	}
	ux.Success(out, strings.TrimSpace(`
Final sync verified + destination refreshable MVs started (source quiesced). Repoint the endpoints, then decommission:
  DOWNTIME migration (ingest stays stopped until after applications start): flip BOTH write_endpoint AND read_endpoint
    to the new node now (safe: the source is quiesced and the final sync was re-verified), then provision applications.
  ONLINE migration (ingest resumes on the new node first): flip write_endpoint->new, resume periscope-ingest and drain
    Kafka lag to 0, then flip read_endpoint->new.
ROLLBACK: a simple repoint-back is safe ONLY before periscope-ingest has written the new node (committed Kafka offsets).
  In that window, repoint the endpoints back, restart the old node's refreshable views (SYSTEM START VIEW), and resume
  ingest on it. AFTER the new node has ingested, a plain repoint LOSES those rows — reverse-migrate (new->old) or
  rewind+replay Kafka and re-verify instead.
Do NOT decommission the old node until the release is confirmed healthy.`))
	return nil
}

// cutoverViewControls are the refreshable-MV operations finishCutover sequences over the catalogued views. Injected so
// the ordering (source-quiesce → sync → verify → dest-start) and the rollback guarantees are unit-testable without a
// live ClickHouse. stopDestAll/startDestOne act on the DESTINATION; stop/startSourceAll act on the SOURCE.
type cutoverViewControls struct {
	stopDestAll    func() error
	startDestOne   func(mv string) error
	stopSourceAll  func() error
	startSourceAll func() error
	// assertQuiesced DIRECTLY checks system.view_refreshes on BOTH nodes with a positive predicate: EVERY returned row
	// must have status == "Disabled" and EVERY required (catalogued) view must be present. Any non-Disabled status
	// (including WaitingForDependencies or an unknown future state), a missing required view, or a query error fails
	// closed — proving quiescence positively rather than accepting by omission. Extra Disabled rows (e.g. an old-schema
	// view the source legitimately still carries) are permitted, and duplicate rows are not separately rejected: both
	// are harmless because the invariant is "nothing is refreshing", not "the row set matches exactly". `SYSTEM STOP
	// VIEW` state does not survive a restart, so a restart on either node resurfaces here as a non-Disabled row.
	assertQuiesced func() error
	// waitQuiesced is assertQuiesced with a bounded retry, used right after issuing STOP VIEW to let an in-flight refresh
	// finish and settle to Disabled before proceeding.
	waitQuiesced func() error
}

// bestEffortEachView runs op over EVERY catalogued refreshable view and AGGREGATES failures (errors.Join) — a cleanup /
// quiesce / restore must never stop at the first failed view and leave the rest untouched. Returns nil when all succeed.
func bestEffortEachView(op func(mv string) error) error {
	var errs []error
	for _, mv := range provisioner.PeriscopeMigrationCatalog.RefreshableMVs {
		if err := op(mv); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", mv, err))
		}
	}
	return errors.Join(errs...)
}

// cutoverViewControls builds the production controls from the migrator's destination and source clients. The whole-set
// operations are best-effort (attempt every view, aggregate errors); only startDestOne is per-view (finishCutover
// sequences it so it can roll back on a partial failure).
func (m *chMigrateCtx) cutoverViewControls(cmd *cobra.Command) cutoverViewControls {
	eachView := func(exec func(context.Context, string) (string, error), sqlFor func(db, mv string) string) func() error {
		return func() error {
			return bestEffortEachView(func(mv string) error {
				return execViewControl(cmdContext(cmd), exec, sqlFor(m.db, mv))
			})
		}
	}
	return cutoverViewControls{
		stopDestAll: eachView(m.exec, provisioner.StopRefreshableViewSQL),
		startDestOne: func(mv string) error {
			return execViewControl(cmdContext(cmd), m.exec, provisioner.StartRefreshableViewSQL(m.db, mv))
		},
		stopSourceAll:  eachView(m.execOnSource, provisioner.StopRefreshableViewSQL),
		startSourceAll: eachView(m.execOnSource, provisioner.StartRefreshableViewSQL),
		assertQuiesced: func() error {
			probe, cancel := context.WithTimeout(cmdContext(cmd), quiesceProbeTimeout)
			defer cancel()
			return m.checkQuiesced(probe)
		},
		waitQuiesced: func() error {
			return boundedWaitQuiesced(cmdContext(cmd), m.checkQuiesced)
		},
	}
}

const (
	quiesceWaitTotal    = 60 * time.Second // whole-wait DEADLINE (not just a retry count)
	quiesceProbeTimeout = 15 * time.Second // per-probe timeout so a stalled query can't run unbounded
	quiesceWaitInterval = 2 * time.Second  // delay between probes
	viewControlTimeout  = 30 * time.Second // per STOP/START VIEW deadline; SSH's timeout only bounds connect, not the query
)

// execViewControl runs one STOP/START VIEW statement under a per-operation deadline. Without it a single hung view-control
// call blocks best-effort iteration from reaching the remaining views — and, in cutover, from reaching the source-
// restoration path — because SSH's configured timeout bounds only connection establishment, not a query that hangs after
// the session is up.
func execViewControl(baseCtx context.Context, exec func(context.Context, string) (string, error), sql string) error {
	ctx, cancel := context.WithTimeout(baseCtx, viewControlTimeout)
	defer cancel()
	_, err := exec(ctx, sql)
	return err
}

// cmdContext returns cmd.Context() or context.Background() if unset.
func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// boundedWaitQuiesced retries check under a WHOLE-WAIT deadline (quiesceWaitTotal), giving each probe a bounded context
// and using a context-aware timer between attempts — so a stalled SSH/ClickHouse query can never make the "60s wait"
// run indefinitely while both nodes are stopped and restoration is blocked.
func boundedWaitQuiesced(baseCtx context.Context, check func(context.Context) error) error {
	deadline, cancel := context.WithTimeout(baseCtx, quiesceWaitTotal)
	defer cancel()
	var last error
	for {
		probe, pcancel := context.WithTimeout(deadline, quiesceProbeTimeout)
		last = check(probe)
		pcancel()
		if last == nil {
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("refreshable views did not quiesce within %s: %w", quiesceWaitTotal, errors.Join(last, deadline.Err()))
		case <-time.After(quiesceWaitInterval):
		}
	}
}

// checkQuiesced reads system.view_refreshes on BOTH nodes under ctx. The DESTINATION must contain (at least) every
// catalogued refreshable view, all Disabled; the SOURCE (old schema — its view set may differ) must have EVERY
// refreshable view Disabled, so an UNCATALOGUED/stale running view on the old node is caught too.
func (m *chMigrateCtx) checkQuiesced(ctx context.Context) error {
	return errors.Join(
		m.checkNodeQuiesced(ctx, m.exec, "destination", provisioner.PeriscopeMigrationCatalog.RefreshableMVs),
		m.checkNodeQuiesced(ctx, m.execOnSource, "source", nil),
	)
}

// checkNodeQuiesced reads ALL refreshable views for the database on one node (no IN-list filter, so uncatalogued views
// are visible) and validates: every returned view is Disabled, and every `required` view is present.
func (m *chMigrateCtx) checkNodeQuiesced(ctx context.Context, exec func(context.Context, string) (string, error), node string, required []string) error {
	out, err := exec(ctx, fmt.Sprintf("SELECT view, status FROM system.view_refreshes WHERE database = '%s'", m.db))
	if err != nil {
		return fmt.Errorf("read %s view_refreshes: %w", node, err)
	}
	return validateQuiescedRefreshViews(node, required, parseViewStatusTSV(out))
}

// chViewStatus is one system.view_refreshes row (view name + refresh status).
type chViewStatus struct{ view, status string }

// parseViewStatusTSV parses `view<TAB>status` rows; a malformed row yields an empty status (which fails the Disabled
// requirement, i.e. fails closed).
func parseViewStatusTSV(out string) []chViewStatus {
	var rows []chViewStatus
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		row := chViewStatus{view: strings.TrimSpace(parts[0])}
		if len(parts) == 2 {
			row.status = strings.TrimSpace(parts[1])
		}
		rows = append(rows, row)
	}
	return rows
}

// validateQuiescedRefreshViews requires EVERY returned refreshable view (catalogued or not) to have status "Disabled",
// and every `required` view to be present. Any non-Disabled status (WaitingForDependencies, Scheduling, an unknown
// future state, or an empty/malformed value) fails closed — including an UNCATALOGUED running view — and a missing
// required view fails closed, so quiescence is proven positively rather than by the absence of an active-state row.
func validateQuiescedRefreshViews(node string, required []string, rows []chViewStatus) error {
	const disabled = "Disabled"
	seen := map[string]bool{}
	var problems []string
	for _, r := range rows {
		seen[r.view] = true
		if r.status != disabled {
			problems = append(problems, fmt.Sprintf("%s is %q (want %s)", r.view, r.status, disabled))
		}
	}
	for _, v := range required {
		if !seen[v] {
			problems = append(problems, fmt.Sprintf("missing required view %q", v))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s refreshable views not fully quiesced (every view must be %q): %s", node, disabled, strings.Join(problems, "; "))
	}
	return nil
}

// finishCutover performs a retry-safe, source-quiesced cutover:
//  1. Stop ALL destination refresh views — a clean slate, so a RETRIED cutover never runs the final sync with a
//     partially-started APPEND view mutating the tables being replaced.
//  2. Stop ALL source refresh views — stopping periscope-ingest does NOT stop the old node's timer-driven
//     `REFRESH EVERY … APPEND` views, which could otherwise mutate a source `*_store` table during the final
//     sync/verify and make parity race-dependent.
//  3. Bounded-WAIT until every catalogued view on both nodes is Disabled (an in-flight refresh may still be settling).
//  4. Final delta sync.
//  5. ASSERT exact-Disabled on both nodes, RE-VERIFY parity, then ASSERT exact-Disabled AGAIN — bracketing the
//     non-atomic sequential fingerprints so a view that resumed anywhere in the sync/verify window (a restart drops
//     SYSTEM STOP VIEW) is caught before the endpoint switch is authorized.
//  6. Start destination views, rolling back to all-stopped on a partial failure.
//
// Any failure after step 2 restores the source views so rollback to the old node stays usable. On success the source
// stays quiesced (it is being decommissioned). sync/verify and the view controls are injected for unit-testability.
func (m *chMigrateCtx) finishCutover(cmd *cobra.Command, sync, verify func(*cobra.Command, *chMigrateCtx) error, v cutoverViewControls) error {
	out := cmd.OutOrStdout()

	// restoreSource runs the best-effort source restoration and joins any restore error to the primary failure, so both
	// the primary error and any restore error survive in the returned chain.
	restoreSource := func(primary error) error {
		if rErr := v.startSourceAll(); rErr != nil {
			return errors.Join(primary, fmt.Errorf("ALSO failed to restore source refreshable MVs (old node left quiesced — restart them by hand): %w", rErr))
		}
		return primary
	}

	fmt.Fprintln(out, "Stopping destination refreshable MVs (clean slate for a retry-safe cutover)...")
	if err := v.stopDestAll(); err != nil {
		return fmt.Errorf("stop destination views: %w", err)
	}
	fmt.Fprintln(out, "Stopping source refreshable MVs (quiescing the old node for the final sync)...")
	if err := v.stopSourceAll(); err != nil {
		// A partial source-stop already stopped some views; restore them (best-effort) so the old node is not left
		// half-quiesced. This runs BEFORE returning — the abort path below is not yet installed at this point.
		return restoreSource(fmt.Errorf("stop source views: %w", err))
	}

	// Bounded-wait until every catalogued view on both nodes has settled to Disabled (an in-flight refresh may still be
	// finishing right after STOP VIEW).
	fmt.Fprintln(out, "Waiting for refreshable views to quiesce (Disabled) on both nodes...")
	if err := v.waitQuiesced(); err != nil {
		return restoreSource(err)
	}

	fmt.Fprintln(out, "Final delta sync (ingest stopped)...")
	if err := sync(cmd, m); err != nil {
		return restoreSource(fmt.Errorf("final sync: %w", err))
	}

	// Bracket the NON-ATOMIC (sequential per-table) parity verify with an exact-Disabled assert on BOTH sides: once
	// before, so a view that resumed during the sync is caught, and once AFTER, so a view that resumed DURING the verify
	// window (which the sequential fingerprints would not necessarily surface as a mismatch) is caught before the
	// endpoint switch is authorized. A restart on either node drops SYSTEM STOP VIEW and is caught here.
	if err := v.assertQuiesced(); err != nil {
		return restoreSource(fmt.Errorf("pre-verify quiescence check: %w", err))
	}
	fmt.Fprintln(out, "Re-verifying parity after the final sync...")
	if err := verify(cmd, m); err != nil {
		return restoreSource(fmt.Errorf("post-sync parity verification failed — refusing to start views or authorize the endpoint switch: %w", err))
	}
	if err := v.assertQuiesced(); err != nil {
		return restoreSource(fmt.Errorf("post-verify quiescence check: %w", err))
	}

	fmt.Fprintln(out, "Starting destination refreshable MVs...")
	for _, mv := range provisioner.PeriscopeMigrationCatalog.RefreshableMVs {
		if err := v.startDestOne(mv); err != nil {
			// Best-effort roll back partial startup to all-stopped (aggregated), then restore the source.
			return restoreSource(errors.Join(fmt.Errorf("start destination view %s: %w", mv, err), v.stopDestAll()))
		}
	}
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
