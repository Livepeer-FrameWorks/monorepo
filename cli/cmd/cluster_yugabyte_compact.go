package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

const (
	yugabyteCompactDefaultTimeout = time.Hour
	// yugabyteCompactHeartbeat is how often a running compaction reports that it is still waiting.
	yugabyteCompactHeartbeat = 30 * time.Second
)

func newYugabyteCompactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compact",
		Short: "Fully compact the tables of one database to drop deleted rows and tombstones",
		Long: `Run a full DocDB compaction of the tables of one YugabyteDB service database, one table at
a time. Deleted and updated rows stay on disk as tombstones and old versions until a compaction
rewrites the files that hold them; a table that churns through deletes can stay large and slow to
scan long after its rows are gone. A full compaction rewrites every SST file of every replica of
every tablet of the table and keeps only live data.

Table ids are resolved from the database catalog and confirmed with the masters before anything
runs. Without --table every table and materialized view in the database is compacted, largest
first; each table's secondary indexes are compacted with it. A colocated database keeps its
colocated tables in one shared tablet, which is compacted once for all of them.

A full compaction reads and rewrites the whole table on every node that holds a replica, which
costs disk IO, CPU, and temporarily up to the table's size in extra disk space. Run it outside peak
hours. Rows deleted more recently than the tservers' history retention (15 minutes by default) are
kept by the compaction.`,
		Example: `  frameworks cluster yugabyte compact --database foghorn_eu --dry-run
  frameworks cluster yugabyte compact --database foghorn_eu --table foghorn.domain_event_outbox
  frameworks cluster yugabyte compact --database periscope --timeout 3h`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runYugabyteCompact(cmd, rc.Manifest)
		},
	}
	cmd.Flags().String("database", "", "physical database to compact, such as quartermaster or foghorn_eu")
	cmd.Flags().StringArray("table", nil, "table to compact as schema.table, or a table name only one schema holds; repeatable (default: every table)")
	cmd.Flags().Duration("timeout", yugabyteCompactDefaultTimeout, "longest wait for one table's compaction before stopping")
	cmd.Flags().Bool("dry-run", false, "list the tables with their tablets and SST size without compacting")
	return cmd
}

type yugabyteCompactOptions struct {
	Database string
	Tables   []string
	Timeout  time.Duration
	DryRun   bool
}

func yugabyteCompactOptionsFromFlags(cmd *cobra.Command) (yugabyteCompactOptions, error) {
	var opts yugabyteCompactOptions
	var err error
	if opts.Database, err = cmd.Flags().GetString("database"); err != nil {
		return opts, err
	}
	opts.Database = strings.TrimSpace(opts.Database)
	if opts.Database == "" {
		return opts, errors.New("--database is required")
	}
	if opts.Tables, err = cmd.Flags().GetStringArray("table"); err != nil {
		return opts, err
	}
	for _, table := range opts.Tables {
		if strings.TrimSpace(table) == "" {
			return opts, errors.New("--table must not be empty")
		}
	}
	if opts.Timeout, err = cmd.Flags().GetDuration("timeout"); err != nil {
		return opts, err
	}
	if opts.Timeout < time.Second {
		return opts, errors.New("--timeout must be at least 1s")
	}
	if opts.DryRun, err = cmd.Flags().GetBool("dry-run"); err != nil {
		return opts, err
	}
	return opts, nil
}

func runYugabyteCompact(cmd *cobra.Command, manifest *inventory.Manifest) error {
	opts, err := yugabyteCompactOptionsFromFlags(cmd)
	if err != nil {
		return err
	}
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled || !pg.IsYugabyte() {
		return errors.New("compact requires infrastructure.postgres with engine yugabyte")
	}
	declared := false
	for _, candidate := range yugabyteSchemaDatabases(pg.Databases, manifest) {
		if candidate.Name == opts.Database {
			declared = true
			break
		}
	}
	if !declared {
		return fmt.Errorf("database %q is not declared in the manifest", opts.Database)
	}
	ctx := cmd.Context()
	pool := fwssh.NewPool(45*time.Second, stringFlag(cmd, "ssh-key").Value)
	defer pool.Close()
	hosts := postgresCandidateHosts(manifest, pg)
	primary, ok := firstHealthyYugabyteHost(ctx, pool, hosts, pg)
	if !ok {
		return errors.New("no yugabyte tserver with healthy local YSQL")
	}
	compaction := &provisioner.YugabyteCompaction{MasterAddresses: pg.MasterAddresses(manifest.MeshAddress), Database: opts.Database}
	for _, host := range hosts {
		runner, connectErr := pool.Get(&fwssh.ConnectionConfig{Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second})
		if connectErr != nil {
			if host.ExternalIP == primary.ExternalIP && host.Name == primary.Name {
				return fmt.Errorf("ssh connect %s: %w", host.Name, connectErr)
			}
			ux.Warn(cmd.OutOrStdout(), fmt.Sprintf("%s is not reachable over SSH; its replicas are not measured: %v", host.Name, connectErr))
			continue
		}
		node := &provisioner.SSHYugabyteNode{NodeName: firstNonEmpty(host.Name, host.ExternalIP), Runner: runner, Port: pg.EffectivePort()}
		compaction.TServers = append(compaction.TServers, provisioner.YugabyteCompactionTServer{Node: node})
		if host.ExternalIP == primary.ExternalIP && host.Name == primary.Name {
			compaction.Primary = node
		}
	}
	if compaction.Primary == nil {
		return fmt.Errorf("healthy tserver %s is not among the manifest nodes", primary.Name)
	}
	inProgress, err := provisioner.RelayoutsInProgress(ctx, compaction.Primary)
	if err != nil {
		return err
	}
	for _, entry := range inProgress {
		if strings.HasPrefix(entry, opts.Database+" (") {
			return fmt.Errorf("relayout in progress for %s; finish or roll it back before compacting", entry)
		}
	}
	return compactYugabyteDatabase(ctx, cmd.OutOrStdout(), compaction, opts)
}

// yugabyteCompactor is the part of provisioner.YugabyteCompaction the command drives.
type yugabyteCompactor interface {
	Resolve(ctx context.Context, requested []string) ([]provisioner.YugabyteCompactionTarget, error)
	Measure(ctx context.Context, targets []provisioner.YugabyteCompactionTarget) (provisioner.YugabyteCompactionMeasurement, error)
	Compact(ctx context.Context, target provisioner.YugabyteCompactionTarget, timeout time.Duration) (string, error)
}

// compactYugabyteDatabase lists the targets with their sizes, then compacts them one at a time, largest first. It
// stops at the first table that fails or outlives its timeout, because that compaction keeps running on the tservers
// and starting the next one would run two at once.
func compactYugabyteDatabase(ctx context.Context, out io.Writer, compactor yugabyteCompactor, opts yugabyteCompactOptions) error {
	targets, err := compactor.Resolve(ctx, opts.Tables)
	if err != nil {
		return err
	}
	measurement, err := compactor.Measure(ctx, targets)
	if err != nil {
		return err
	}
	for _, unreachable := range measurement.Unreachable {
		ux.Warn(out, "not measured: "+unreachable)
	}
	provisioner.SortYugabyteCompactionTargets(targets)
	heading := fmt.Sprintf("Compaction of %s", opts.Database)
	if opts.DryRun {
		heading += " (dry run)"
	}
	ux.Heading(out, heading)
	printYugabyteCompactionTargets(out, targets)
	if opts.DryRun {
		fmt.Fprintf(out, "Dry run: nothing was compacted. Each table would be compacted in this order, one at a time, waiting up to %s each.\n", opts.Timeout)
		return nil
	}
	ux.Warn(out, "a full compaction reads and rewrites every replica of each table: expect raised disk IO and CPU on every tserver, and up to the table's size in extra disk space while it runs")

	for i, target := range targets {
		label := fmt.Sprintf("[%d/%d] %s", i+1, len(targets), target.Name)
		fmt.Fprintf(out, "%s: compacting %d tablet(s), %d replica(s), %s of SST files\n", label, target.Tablets, target.Replicas, relayoutBytes(target.SSTBytes))
		started := time.Now()
		result, compactErr := compactWithHeartbeat(ctx, out, label, started, func() (string, error) {
			return compactor.Compact(ctx, target, opts.Timeout)
		})
		elapsed := time.Since(started).Round(time.Second)
		if compactErr != nil {
			remaining := yugabyteCompactionNames(targets[i+1:])
			if ctx.Err() != nil {
				fmt.Fprintf(out, "%s: interrupted after %s. The compaction already requested keeps running on the tservers.\n", label, elapsed)
				if len(remaining) > 0 {
					fmt.Fprintf(out, "Not started: %s\n", strings.Join(remaining, ", "))
				}
				return fmt.Errorf("compaction of %s interrupted: %w", opts.Database, ctx.Err())
			}
			fmt.Fprintf(out, "%s: failed after %s: %v\n", label, elapsed, compactErr)
			fmt.Fprintln(out, "A compaction that timed out keeps running on the tservers; check its progress with --dry-run before compacting again.")
			if len(remaining) > 0 {
				fmt.Fprintf(out, "Not started: %s\n", strings.Join(remaining, ", "))
			}
			return fmt.Errorf("compact %s in %s: %w", target.Name, opts.Database, compactErr)
		}
		before := target.SSTBytes
		after := []provisioner.YugabyteCompactionTarget{target}
		if _, measureErr := compactor.Measure(ctx, after); measureErr != nil {
			fmt.Fprintf(out, "%s: done in %s (%s); SST size not re-measured: %v\n", label, elapsed, result, measureErr)
			continue
		}
		fmt.Fprintf(out, "%s: done in %s, SST files %s -> %s\n", label, elapsed, relayoutBytes(before), relayoutBytes(after[0].SSTBytes))
	}
	ux.Success(out, fmt.Sprintf("compacted %d DocDB table(s) in %s", len(targets), opts.Database))
	return nil
}

// compactWithHeartbeat runs compact and prints a line every heartbeat interval until it returns.
func compactWithHeartbeat(ctx context.Context, out io.Writer, label string, started time.Time, compact func() (string, error)) (string, error) {
	type outcome struct {
		result string
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := compact()
		done <- outcome{result, err}
	}()
	ticker := time.NewTicker(yugabyteCompactHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case finished := <-done:
			return finished.result, finished.err
		case <-ticker.C:
			if ctx.Err() == nil {
				fmt.Fprintf(out, "%s: still compacting, %s elapsed\n", label, time.Since(started).Round(time.Second))
			}
		}
	}
}

func printYugabyteCompactionTargets(out io.Writer, targets []provisioner.YugabyteCompactionTarget) {
	var total int64
	for _, target := range targets {
		total += target.SSTBytes
		fmt.Fprintf(out, "  %-48s %4d tablet(s) %4d replica(s) %6d SST file(s) %10s\n", target.Name, target.Tablets, target.Replicas, target.SSTFiles, relayoutBytes(target.SSTBytes))
		if target.Colocated {
			fmt.Fprintf(out, "    covers %d colocated relation(s): %s\n", len(target.Relations), strings.Join(target.Relations, ", "))
		}
	}
	fmt.Fprintf(out, "  %d DocDB table(s) (tables, secondary indexes, and the colocated tablet), %s of SST files across all replicas\n", len(targets), relayoutBytes(total))
}

func yugabyteCompactionNames(targets []provisioner.YugabyteCompactionTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.Name)
	}
	return names
}
