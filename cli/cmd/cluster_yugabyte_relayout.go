package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"frameworks/cli/internal/releases"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"

	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/spf13/cobra"
)

const relayoutDefaultDumpDir = "/var/lib/frameworks/relayout"

// newClusterYugabyteCmd groups YugabyteDB physical layout operations.
func newClusterYugabyteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "yugabyte",
		Short: "YugabyteDB physical layout operations",
	}
	cmd.AddCommand(newYugabyteRelayoutCmd())
	return cmd
}

func newYugabyteRelayoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relayout",
		Short: "Move an existing database into its declared colocated layout",
		Long: `Move one existing YugabyteDB service database into the layout declared in
pkg/database/sql/layout. Colocation is fixed when a database is created, so the
database is cloned into a shadow database created with the layout and renamed
into place. Each step records its effect in a leased journal in the yugabyte
admin database, and a step re-run after an interruption resumes from that record.

  plan       read-only: tservers, client binaries, dump space, placement,
             the services that use the database, journal state, and blockers
  preflight  no downtime: rehearse the whole copy from the live database into
             a scratch database, verify it, report timings and the expected
             window, then drop it
  prepare    no downtime: build the shadow with the layout and the source's
             complete schema (indexes, constraints, triggers), and require its
             schema to equal the source
  cutover    the window: revoke access and end every session, copy the data,
             verify it, rename the source aside and the shadow into place, and
             restore access; --rollback returns the source instead
  finish     drop the pre-relayout database and the staged dump

Services keep running. From cutover's fence until access is restored they
cannot connect to the database and see it as briefly unavailable; they
reconnect by name afterwards and reach the relaid database. Migrations and
provisioning refuse from prepare until finish or rollback. Run the command
from a session that survives disconnects, such as tmux on a mesh host.`,
		Example: `  frameworks cluster yugabyte relayout plan --database quartermaster
  frameworks cluster yugabyte relayout preflight --database quartermaster
  frameworks cluster yugabyte relayout prepare --database quartermaster
  frameworks cluster yugabyte relayout cutover --database quartermaster --yes
  frameworks cluster yugabyte relayout finish --database quartermaster --yes`,
	}
	cmd.PersistentFlags().String("database", "", "physical database to relay out, such as quartermaster or foghorn_eu")
	cmd.PersistentFlags().String("dump-dir", relayoutDefaultDumpDir, "absolute directory on the Yugabyte node for staged dumps")
	cmd.PersistentFlags().Bool("dry-run", false, "print the step's actions without connecting")

	cutover := newRelayoutSubCmd("cutover", "Fence, copy the data, verify, rename the shadow into place, and restore access", runRelayoutCutover)
	cutover.Flags().Bool("rollback", false, "return the original database to its name and access instead of cutting over")
	cutover.Flags().Bool("yes", false, "confirm fencing the database and the renames")
	finish := newRelayoutSubCmd("finish", "Drop the pre-relayout database and the staged dump", runRelayoutFinish)
	finish.Flags().Bool("yes", false, "confirm dropping the pre-relayout database")
	cmd.AddCommand(
		newRelayoutSubCmd("plan", "Check readiness and show placement, affected services, and blockers (read-only)", runRelayoutPlan),
		newRelayoutSubCmd("preflight", "Rehearse the copy into a scratch database without downtime and report timings", runRelayoutPreflight),
		newRelayoutSubCmd("prepare", "Build the shadow's schema in the declared layout while services run", runRelayoutPrepare),
		cutover,
		finish,
	)
	return cmd
}

type relayoutRun func(ctx context.Context, cmd *cobra.Command, relayout *relayoutContext) error

func newRelayoutSubCmd(use, short string, run relayoutRun) *cobra.Command {
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
			relayout, err := newRelayoutContext(cmd, rc)
			if err != nil {
				return err
			}
			defer relayout.pool.Close()
			return run(cmd.Context(), cmd, relayout)
		},
	}
}

type relayoutContext struct {
	manifest  *inventory.Manifest
	pg        *inventory.PostgresConfig
	database  provisioner.SchemaDatabase
	source    string
	layout    *provisioner.DatabaseLayout
	consumers []relayoutConsumer
	hosts     []inventory.Host
	pool      *fwssh.Pool
	dumpDir   string
	dryRun    bool
}

func newRelayoutContext(cmd *cobra.Command, rc *resolvedCluster) (*relayoutContext, error) {
	manifest := rc.Manifest
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled || !pg.IsYugabyte() {
		return nil, errors.New("relayout requires infrastructure.postgres with engine yugabyte")
	}
	name, err := cmd.Flags().GetString("database")
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("--database is required")
	}
	var database *provisioner.SchemaDatabase
	for _, candidate := range yugabyteSchemaDatabases(pg.Databases, manifest) {
		if candidate.Name == name {
			found := candidate
			database = &found
			break
		}
	}
	if database == nil {
		return nil, fmt.Errorf("database %q is not declared in the manifest", name)
	}
	source := database.SourceName
	if source == "" {
		source = database.Name
	}
	layout, err := provisioner.YugabyteLayoutForDatabase(source)
	if err != nil {
		return nil, err
	}
	if !layout.Colocated() {
		return nil, fmt.Errorf("%s declares no colocated layout, so there is nothing to relay out", source)
	}
	dumpDir, err := cmd.Flags().GetString("dump-dir")
	if err != nil {
		return nil, err
	}
	dryRun, err := cmd.Flags().GetBool("dry-run")
	if err != nil {
		return nil, err
	}
	return &relayoutContext{
		manifest:  manifest,
		pg:        pg,
		database:  *database,
		source:    source,
		layout:    layout,
		consumers: relayoutConsumers(manifest, database.Name),
		hosts:     postgresCandidateHosts(manifest, pg),
		pool:      fwssh.NewPool(45*time.Second, stringFlag(cmd, "ssh-key").Value),
		dumpDir:   dumpDir,
		dryRun:    dryRun,
	}, nil
}

type relayoutConsumer struct {
	ServiceID string
	Deploy    string
	HostNames []string
}

// relayoutConsumers lists every enabled service whose database is physical, mapping a clustered service onto its
// per-cell database the way provisioning does.
func relayoutConsumers(manifest *inventory.Manifest, physical string) []relayoutConsumer {
	var consumers []relayoutConsumer
	for serviceID, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		deploy := strings.TrimSpace(svc.Deploy)
		if deploy == "" {
			deploy = serviceID
		}
		logical, owned := releases.ServiceDatabaseLookup(deploy)
		if !owned {
			continue
		}
		database := logical
		if alias := strings.ReplaceAll(serviceID, "-", "_"); strings.TrimSpace(svc.Cluster) != "" && alias != deploy && logical == deploy {
			database = alias
		}
		if database != physical {
			continue
		}
		consumers = append(consumers, relayoutConsumer{ServiceID: serviceID, Deploy: deploy, HostNames: serviceHostNames(svc)})
	}
	sort.Slice(consumers, func(i, j int) bool { return consumers[i].ServiceID < consumers[j].ServiceID })
	return consumers
}

func (c *relayoutContext) engine(ctx context.Context) (*provisioner.YugabyteRelayout, inventory.Host, error) {
	primary, ok := firstHealthyYugabyteHost(ctx, c.pool, c.hosts, c.pg)
	if !ok {
		return nil, inventory.Host{}, errors.New("no yugabyte tserver with healthy local YSQL")
	}
	owner, err := relayoutLeaseOwner()
	if err != nil {
		return nil, inventory.Host{}, err
	}
	var primaryNode provisioner.YugabyteNode
	nodes := make([]provisioner.YugabyteNode, 0, len(c.hosts))
	for _, host := range c.hosts {
		name := firstNonEmpty(host.Name, host.ExternalIP)
		runner, err := c.pool.Get(&fwssh.ConnectionConfig{
			Address:  host.ExternalIP,
			Port:     22,
			User:     host.User,
			HostName: host.Name,
			Timeout:  30 * time.Second,
		})
		if err != nil {
			return nil, inventory.Host{}, fmt.Errorf("ssh connect %s: %w", name, err)
		}
		node := &provisioner.SSHYugabyteNode{NodeName: name, Runner: runner, Port: c.pg.EffectivePort(), ApplicationName: provisioner.RelayoutApplicationName(owner)}
		nodes = append(nodes, node)
		if host.ExternalIP == primary.ExternalIP && host.Name == primary.Name {
			primaryNode = node
		}
	}
	if primaryNode == nil {
		return nil, inventory.Host{}, fmt.Errorf("healthy tserver %s is not among the manifest nodes", primary.Name)
	}
	return &provisioner.YugabyteRelayout{
		Primary:    primaryNode,
		Nodes:      nodes,
		YSQLHost:   "127.0.0.1",
		YSQLPort:   c.pg.EffectivePort(),
		Database:   c.database.Name,
		Roles:      []string{c.database.Owner, c.database.RuntimeRole},
		DumpDir:    c.dumpDir,
		LeaseOwner: owner,
	}, primary, nil
}

// withLease takes the relayout lease, keeps it renewed while step runs, and releases it afterwards.
func (c *relayoutContext) withLease(ctx context.Context, out io.Writer, step func(context.Context, *provisioner.YugabyteRelayout) error) error {
	engine, _, err := c.engine(ctx)
	if err != nil {
		return err
	}
	if _, err = engine.Acquire(ctx); err != nil {
		record, recordErr := engine.Record(ctx)
		if recordErr != nil || record == nil || !relayoutOwnerIsDeadLocalProcess(record.LeaseOwner) {
			return err
		}
		// A relayout killed on this host leaves its lease until it expires. Its process is gone, but a restore or dump it
		// started on a tserver can still run; TakeOver ends those sessions before moving the lease.
		fmt.Fprintf(out, "Taking over the relayout lease of %s from %s, whose process no longer runs on this host; ending its remaining sessions first\n", c.database.Name, record.LeaseOwner)
		if _, err = engine.TakeOver(ctx, record.LeaseOwner); err != nil {
			return err
		}
	}
	holdCtx, stop := engine.Hold(ctx)
	stepErr := relayoutStepResult(step(holdCtx, engine), holdCtx)
	stop()
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	if stepErr == nil {
		if releaseErr := engine.Release(releaseCtx); releaseErr != nil {
			fmt.Fprintf(out, "warning: release relayout lease for %s: %v\n", c.database.Name, releaseErr)
		}
		return nil
	}
	// A failed or cancelled step can leave a restore or dump running on a tserver. Its work is ended before the lease
	// is released; if that cannot be proved, the lease keeps naming this invocation so the next one ends it first.
	// The successor is a fresh owner of this process, so if cleanup itself dies, the next invocation on this host
	// recognizes the dead owner and takes the lease over at once.
	successor, err := relayoutLeaseOwner()
	if err != nil {
		fmt.Fprintf(out, "warning: the lease of %s stays with this invocation: %v\n", c.database.Name, err)
		return stepErr
	}
	if releaseErr := engine.ReleaseAfterFailure(releaseCtx, successor); releaseErr != nil {
		fmt.Fprintf(out, "warning: %v\n", releaseErr)
	}
	return stepErr
}

// relayoutStepResult reports a step's outcome together with a lost lease. A step that finishes as its lease is lost
// is not a success, because another invocation may already own the journal it wrote to.
func relayoutStepResult(stepErr error, holdCtx context.Context) error {
	leaseErr := context.Cause(holdCtx)
	if leaseErr == nil || errors.Is(leaseErr, context.Canceled) {
		return stepErr
	}
	if stepErr == nil {
		return leaseErr
	}
	return fmt.Errorf("%w (%w)", stepErr, leaseErr)
}

func (c *relayoutContext) capabilityProbes() []string {
	seen := map[string]struct{}{}
	var probes []string
	for _, consumer := range c.consumers {
		for _, capability := range pkgdatabase.CapabilitiesFor(consumer.Deploy, pkgdatabase.EnginePostgres) {
			if _, ok := seen[capability.Probe]; ok {
				continue
			}
			seen[capability.Probe] = struct{}{}
			probes = append(probes, capability.Probe)
		}
	}
	return probes
}

func runRelayoutPlan(ctx context.Context, cmd *cobra.Command, c *relayoutContext) error {
	out := cmd.OutOrStdout()
	ux.Heading(out, fmt.Sprintf("Relayout plan for %s", c.database.Name))
	fmt.Fprintf(out, "  layout: %s declares %s with %d colocated and %d distributed tables\n",
		c.source, c.layout.Layout, len(c.layout.ColocatedTables), len(c.layout.DistributedTables))
	fmt.Fprintln(out, "  services that cannot reach the database during cutover:")
	for _, consumer := range c.consumers {
		fmt.Fprintf(out, "    %s (%s) on %s\n", consumer.ServiceID, consumer.Deploy, strings.Join(consumer.HostNames, ", "))
	}
	if c.dryRun {
		return nil
	}
	engine, primary, err := c.engine(ctx)
	if err != nil {
		return err
	}
	environment, err := engine.CheckEnvironment(ctx, c.layout)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  tservers with local superuser YSQL: %s\n", strings.Join(environment.ReachableNodes, ", "))
	fmt.Fprintf(out, "  free space for dumps on %s: %s\n", primary.Name, relayoutBytes(environment.DumpDirFreeKB*1024))
	report, err := inspectYugabyteLayout(ctx, c.pool, c.hosts, primary, c.pg, c.database, map[string]error{})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  observed: %s database, %d tablet(s), %d tablet peer(s)\n", report.Observed, report.Tablets, report.Peers)
	for _, drift := range report.Drift {
		fmt.Fprintf(out, "    drift: %s\n", drift)
	}
	record, err := engine.Record(ctx)
	if err != nil {
		return err
	}
	if record == nil {
		fmt.Fprintln(out, "  journal: no relayout started")
	} else {
		fmt.Fprintf(out, "  journal: %s", record.State)
		if record.LeaseOwner != "" {
			fmt.Fprintf(out, " (leased by %s until %s)", record.LeaseOwner, record.LeaseExpiresAt)
		}
		fmt.Fprintln(out)
	}
	if len(environment.Blockers) > 0 {
		return fmt.Errorf("%s cannot be relaid out:\n%s", c.database.Name, strings.Join(environment.Blockers, "\n"))
	}
	if report.Observed == string(provisioner.DatabaseLayoutColocated) && len(report.Drift) == 0 {
		ux.Success(out, fmt.Sprintf("%s already matches its declared layout", c.database.Name))
		return nil
	}
	fmt.Fprintf(out, "Next: frameworks cluster yugabyte relayout preflight --database %s\n", c.database.Name)
	return nil
}

func runRelayoutPreflight(ctx context.Context, cmd *cobra.Command, c *relayoutContext) error {
	out := cmd.OutOrStdout()
	if c.dryRun {
		printRelayoutDryRun(out, "preflight", c.database.Name, c.dumpDir)
		return nil
	}
	return c.withLease(ctx, out, func(ctx context.Context, engine *provisioner.YugabyteRelayout) error {
		report, err := engine.Preflight(ctx, c.layout, c.database.RuntimeRole, c.capabilityProbes())
		if report != nil {
			printRelayoutPreflight(out, report)
		}
		if err != nil {
			return err
		}
		ux.Success(out, fmt.Sprintf("preflight copy of %s restored, matched its schema and layout, and was dropped", c.database.Name))
		fmt.Fprintf(out, "Next: frameworks cluster yugabyte relayout prepare --database %s\n", c.database.Name)
		return nil
	})
}

func printRelayoutPreflight(out io.Writer, report *provisioner.RelayoutPreflight) {
	if environment := report.Environment; environment != nil {
		fmt.Fprintf(out, "  tservers with local superuser YSQL: %s\n", strings.Join(environment.ReachableNodes, ", "))
		fmt.Fprintf(out, "  free space for dumps: %s\n", relayoutBytes(environment.DumpDirFreeKB*1024))
		for _, blocker := range environment.Blockers {
			fmt.Fprintf(out, "  blocker: %s\n", blocker)
		}
		if report.DumpBytes > 0 && environment.DumpDirFreeKB*1024 < 2*report.DumpBytes {
			ux.Warn(out, fmt.Sprintf("free space %s is below twice the dump size %s", relayoutBytes(environment.DumpDirFreeKB*1024), relayoutBytes(report.DumpBytes)))
		}
	}
	if report.DumpBytes > 0 {
		fmt.Fprintf(out, "  dump size: %s across %d tables\n", relayoutBytes(report.DumpBytes), report.Tables)
	}
	t := report.Timings
	fmt.Fprintf(out, "  before the window (prepare): schema dump %s, tables %s, indexes and constraints %s\n",
		t.SchemaDump.Round(time.Second), t.PreData.Round(time.Second), t.PostData.Round(time.Second))
	fmt.Fprintf(out, "  inside the window (cutover): schema compare %s, data dump %s, data load %s, checksum %s per database, session and placement checks %s\n",
		t.Compare.Round(time.Second), t.DataDump.Round(time.Second), t.Data.Round(time.Second), report.ChecksumDuration.Round(time.Second),
		report.WindowChecks.Round(time.Second))
	fmt.Fprintf(out, "  estimated window in which services cannot reach the database: %s\n", report.EstimatedDowntime().Round(time.Second))
	for _, drift := range report.PlacementDrift {
		fmt.Fprintf(out, "  placement: %s\n", drift)
	}
	for _, failure := range report.ProbeFailures {
		fmt.Fprintf(out, "  capability: %s\n", failure)
	}
}

func runRelayoutPrepare(ctx context.Context, cmd *cobra.Command, c *relayoutContext) error {
	out := cmd.OutOrStdout()
	if c.dryRun {
		printRelayoutDryRun(out, "prepare", c.database.Name, c.dumpDir)
		return nil
	}
	return c.withLease(ctx, out, func(ctx context.Context, engine *provisioner.YugabyteRelayout) error {
		if err := engine.Prepare(ctx, c.layout); err != nil {
			return err
		}
		ux.Success(out, fmt.Sprintf("%s holds the schema of %s in the declared layout; services were not interrupted", engine.ShadowName(), c.database.Name))
		fmt.Fprintln(out, "Migrations and provisioning refuse until the relayout finishes or is rolled back.")
		fmt.Fprintf(out, "Next, in the window: frameworks cluster yugabyte relayout cutover --database %s --yes\n", c.database.Name)
		return nil
	})
}

func runRelayoutCutover(ctx context.Context, cmd *cobra.Command, c *relayoutContext) error {
	out := cmd.OutOrStdout()
	rollback, err := cmd.Flags().GetBool("rollback")
	if err != nil {
		return err
	}
	step := "cutover"
	if rollback {
		step = "rollback"
	}
	if c.dryRun {
		printRelayoutDryRun(out, step, c.database.Name, c.dumpDir)
		return nil
	}
	yes, err := cmd.Flags().GetBool("yes")
	if err != nil {
		return err
	}
	if !yes {
		return fmt.Errorf("%s fences or renames databases; re-run with --yes", step)
	}
	return c.withLease(ctx, out, func(ctx context.Context, engine *provisioner.YugabyteRelayout) error {
		if rollback {
			if rollbackErr := engine.Rollback(ctx); rollbackErr != nil {
				return rollbackErr
			}
			ux.Success(out, fmt.Sprintf("%s is back under its name with its recorded access; the shadow was dropped", c.database.Name))
			return nil
		}
		started := time.Now()
		if cutoverErr := engine.Cutover(ctx, c.layout, c.database.RuntimeRole, c.capabilityProbes()); cutoverErr != nil {
			return cutoverErr
		}
		ux.Success(out, fmt.Sprintf("%s now runs in its declared layout (cutover took %s); the original is kept as %s",
			c.database.Name, time.Since(started).Round(time.Second), engine.PreRelayoutName()))
		fmt.Fprintln(out, "Services reconnect to the relaid database on their own. Confirm with `frameworks cluster doctor`, then run:")
		fmt.Fprintf(out, "  frameworks cluster yugabyte relayout finish --database %s --yes\n", c.database.Name)
		return nil
	})
}

func runRelayoutFinish(ctx context.Context, cmd *cobra.Command, c *relayoutContext) error {
	out := cmd.OutOrStdout()
	if c.dryRun {
		printRelayoutDryRun(out, "finish", c.database.Name, c.dumpDir)
		return nil
	}
	yes, err := cmd.Flags().GetBool("yes")
	if err != nil {
		return err
	}
	if !yes {
		return errors.New("finish drops the pre-relayout database; re-run with --yes")
	}
	return c.withLease(ctx, out, func(ctx context.Context, engine *provisioner.YugabyteRelayout) error {
		if finishErr := engine.Finish(ctx); finishErr != nil {
			return finishErr
		}
		ux.Success(out, fmt.Sprintf("dropped %s and its staged dump", engine.PreRelayoutName()))
		return nil
	})
}

func printRelayoutDryRun(out io.Writer, step, database, dumpDir string) {
	ux.Heading(out, fmt.Sprintf("Relayout %s for %s (dry run)", step, database))
	for i, action := range relayoutDryRunSteps(step, database, dumpDir) {
		fmt.Fprintf(out, "  %d. %s\n", i+1, action)
	}
}

func relayoutDryRunSteps(step, database, dumpDir string) []string {
	names := &provisioner.YugabyteRelayout{Database: database}
	shadow, pre, scratch := names.ShadowName(), names.PreRelayoutName(), names.PreflightName()
	switch step {
	case "preflight":
		return []string{
			"check every tserver accepts local superuser YSQL, the primary has the client binaries, and the dump directory is writable",
			"refuse when " + database + " has tables outside its layout or database-level settings",
			"create " + scratch + " colocated and restore the rewritten schema-only dump of the live " + database + ", as prepare does",
			"dump the data of the live " + database + " into " + dumpDir + "/" + database + "/preflight and load it with triggers and foreign-key checks skipped, as cutover does",
			"compare its schema with " + database + ", check placement, run capability probes, and checksum it",
			"drop " + scratch + " and the preflight dump, then report timings and the estimated downtime",
		}
	case "prepare":
		return []string{
			"refuse when " + database + " has tables outside its layout or database-level settings, or a tserver runs the YSQL Connection Manager",
			"drop any previous " + shadow + ", create it colocated with the source owner, and revoke access to it",
			"ysql_dump --section=pre-data and post-data of the live " + database + " into " + dumpDir + "/" + database + "/schema",
			"restore the rewritten pre-data and post-data into " + shadow + ", stopping at the first error, then remove the schema dump",
			"refuse unless the schema of " + shadow + " equals " + database,
		}
	case "cutover":
		return []string{
			"record pg_database.datacl for " + database + " in the journal",
			"REVOKE ALL ON DATABASE " + database + " FROM PUBLIC, every recorded grantee, and the owner and runtime roles",
			"terminate sessions on " + database + " on every tserver and prove twice that none remain",
			"refuse unless the schema of " + database + " still equals " + shadow,
			"ysql_dump --section=data of " + database + " into " + dumpDir + "/" + database + "/relayout and record its sha256",
			"load it into " + shadow + " with session_replication_role=replica (no triggers, no foreign-key checks)",
			"compare schema digest, row counts and checksums, sequences, triggers, constraints, placement, and capability probes",
			"ALTER DATABASE " + database + " RENAME TO " + pre,
			"ALTER DATABASE " + shadow + " RENAME TO " + database,
			"restore the recorded database ACL on " + database + " and prove each grantee can connect",
		}
	case "rollback":
		return []string{
			"refuse once access to the relaid database was restored",
			"rename " + database + " back to " + shadow + " and " + pre + " back to " + database + " as the journal state requires",
			"DROP DATABASE " + shadow,
			"restore the recorded database ACL on " + database,
		}
	case "finish":
		return []string{
			"require a completed cutover",
			"DROP DATABASE " + pre,
			"remove the staged dump directory recorded in the journal",
		}
	}
	return nil
}

func relayoutBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value, suffix := float64(bytes), []string{"KiB", "MiB", "GiB", "TiB"}
	index := -1
	for value >= unit && index < len(suffix)-1 {
		value /= unit
		index++
	}
	return fmt.Sprintf("%.1f %s", value, suffix[index])
}

// relayoutOwnerIsDeadLocalProcess reports whether a lease owner written by relayoutLeaseOwner names a process on this
// host that no longer exists. Any doubt (another host, an unparsable owner, a live or unreadable process) is false.
func relayoutOwnerIsDeadLocalProcess(owner string) bool {
	parts := strings.Split(owner, ":")
	if len(parts) != 3 {
		return false
	}
	host, err := os.Hostname()
	if err != nil || host == "" || parts[0] != host {
		return false
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func relayoutLeaseOwner() (string, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	buf := make([]byte, 4)
	if _, err = rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate lease owner: %w", err)
	}
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), hex.EncodeToString(buf)), nil
}

// initializeOutsideRelayout runs initialize, which creates every service database missing under its canonical name,
// only after proving through host that no Yugabyte relayout is in progress: mid-relayout that name can be free on
// purpose. A vanilla Postgres target has no relayouts and initializes directly.
func initializeOutsideRelayout(ctx context.Context, sshPool *fwssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, initialize func() error) error {
	if pg != nil && pg.IsYugabyte() {
		if err := refuseDuringYugabyteRelayoutFn(ctx, sshPool, host, pg); err != nil {
			return err
		}
	}
	return initialize()
}

// refuseDuringYugabyteRelayout refuses while any database's relayout is between prepare and finish or rollback. A
// schema change would no longer match the prepared shadow, the canonical name may be missing or held by a copy under
// construction, and superuser connections are not fenced, so creating, migrating, or provisioning service databases
// now would act on the wrong database or be lost at cutover.
func refuseDuringYugabyteRelayout(ctx context.Context, sshPool *fwssh.Pool, host inventory.Host, pg *inventory.PostgresConfig) error {
	runner, err := sshPool.Get(&fwssh.ConnectionConfig{Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("ssh connect %s to read the relayout journal: %w", host.Name, err)
	}
	node := &provisioner.SSHYugabyteNode{NodeName: host.Name, Runner: runner, Port: pg.EffectivePort()}
	inProgress, err := provisioner.RelayoutsInProgress(ctx, node)
	if err != nil {
		return err
	}
	if len(inProgress) > 0 {
		return fmt.Errorf("relayout in progress for %s; finish it (cluster yugabyte relayout cutover/finish) or roll it back (cutover --rollback) before migrating or provisioning Yugabyte databases",
			strings.Join(inProgress, ", "))
	}
	return nil
}
