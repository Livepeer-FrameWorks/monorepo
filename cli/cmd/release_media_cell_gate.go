package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/ssh"
)

// A cell whose Foghorn replicas all enforce signed placement attests that to
// Commodore, and from then on refuses every stream of a tenant whose current
// authority is still on the policy-free schema 1. Commodore publishes schema-2
// authority for the tenants targeting the cell once it attests. A Foghorn
// upgrade therefore takes the cells one at a time, and before the next cell
// starts, waits until the cell it just upgraded holds no schema-1 tenant, so a
// cell Commodore does not converge stops the rollout with one cell affected.
const (
	mediaCellGateTimeout  = 15 * time.Minute
	mediaCellGateInterval = 10 * time.Second
	// mediaCellAttestationSettle is how long a cell that does not yet attest
	// enforcement is given to do so: Foghorn's replica liveness window (60 s),
	// after which an upgraded cell's attestation reflects only its new
	// replicas, plus a heartbeat and the report to Commodore.
	mediaCellAttestationSettle = 90 * time.Second
)

// mediaCellAuthority is Commodore's view of one cell.
type mediaCellAuthority struct {
	// Tracked reports that Commodore records cell placement attestations.
	Tracked bool
	// Ready reports that the cell attests schema-2 placement enforcement.
	Ready bool
	// LegacyTenants counts the tenants targeting the cell whose current
	// authority is on schema 1 while the cell enforces.
	LegacyTenants int64
}

type mediaCellGate struct {
	out      io.Writer
	read     func(ctx context.Context, cell string) (mediaCellAuthority, error)
	timeout  time.Duration
	interval time.Duration
	settle   time.Duration
}

// await returns once cell holds no schema-1 tenant and either attests
// enforcement or has had the settle time since its upgrade finished without
// attesting. It fails after the gate's timeout.
func (g *mediaCellGate) await(ctx context.Context, cell string, upgraded time.Time) error {
	deadline := time.Now().Add(g.timeout)
	last := ""
	report := func(status string) {
		if status != last {
			fmt.Fprintf(g.out, "  cell %s: %s\n", cell, status)
			last = status
		}
	}
	for {
		state, err := g.read(ctx, cell)
		var status string
		switch {
		case err != nil:
			status = fmt.Sprintf("media authority state could not be read: %v", err)
		case !state.Tracked:
			report("Commodore records no cell placement attestation; nothing to wait for")
			return nil
		case state.LegacyTenants > 0:
			status = fmt.Sprintf("%d tenant authority(ies) still on schema 1; the cell refuses their streams until Commodore publishes schema 2", state.LegacyTenants)
		case state.Ready:
			report("attests placement enforcement and holds no schema-1 tenant")
			return nil
		case time.Since(upgraded) >= g.settle:
			report("does not attest placement enforcement, so it refuses no schema-1 tenant")
			return nil
		default:
			status = "waiting for the cell's placement attestation to reach Commodore"
		}
		report(status)
		if !time.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("cell %s: media authority state could not be read within %s: %w; later cells were not upgraded; rerun once `frameworks cluster doctor` reads the Media authority convergence check", cell, g.timeout, err)
			}
			return fmt.Errorf("cell %s: %s after %s; later cells were not upgraded. List the tenants with `frameworks cluster diagnose media-authority`, then rerun once the cell holds none", cell, status, g.timeout)
		}
		timer := time.NewTimer(g.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("cell %s: stopped waiting for its tenants' schema-2 authority: %w", cell, ctx.Err())
		case <-timer.C:
		}
	}
}

// upgradeRolloutWaves orders the replicas of a service upgrade (other than
// Yugabyte's) and returns the hook run after each wave. A Foghorn upgrade with
// a cell gate takes its cells one at a time through gatedMediaCellWaves.
func upgradeRolloutWaves(ctx context.Context, manifest *inventory.Manifest, serviceName, deployName string, hosts []string, gate *mediaCellGate) ([]rolloutWave, func(rolloutWave) error) {
	waves := upgradeReplicaWaves(manifest, serviceName, deployName, hosts)
	if deployName != "foghorn" || gate == nil {
		return waves, nil
	}
	waves, after := gatedMediaCellWaves(waves, gate,
		func(host string) string { return rolloutCell(manifest, host) },
		func(host string) string { return foghornReplicaCell(manifest, serviceName, host) })
	return waves, func(wave rolloutWave) error { return after(ctx, wave) }
}

// gatedMediaCellWaves runs the media waves of a Foghorn upgrade one cell at a
// time and returns the hook that holds the next cell until the one just
// upgraded converges. cellOf maps a replica host to its media-authority cell;
// a host without one is not gated.
func gatedMediaCellWaves(waves []rolloutWave, gate *mediaCellGate, laneCell, cellOf func(string) string) ([]rolloutWave, func(context.Context, rolloutWave) error) {
	var out []rolloutWave
	for _, wave := range waves {
		if wave.Name != rolloutTierMedia.String() {
			out = append(out, wave)
			continue
		}
		for _, lane := range wave.Lanes {
			out = append(out, rolloutWave{Name: "media cell " + laneCell(lane[0]), Lanes: [][]string{lane}, Limit: 1})
		}
	}
	after := func(ctx context.Context, wave rolloutWave) error {
		hosts := wave.hosts()
		if !strings.HasPrefix(wave.Name, "media cell ") || len(hosts) == 0 {
			return nil
		}
		cell := cellOf(hosts[0])
		if cell == "" {
			return nil
		}
		return gate.await(ctx, cell, time.Now())
	}
	return out, after
}

// foghornReplicaCell is the media-authority cell a Foghorn replica of
// serviceName on host attests for, as provisioning derives it.
func foghornReplicaCell(manifest *inventory.Manifest, serviceName, host string) string {
	cluster := strings.TrimSpace(manifest.Services[serviceName].Cluster)
	if cluster == "" {
		cluster = manifest.HostCluster(host)
	}
	return mediaAuthorityTaskControlCell(&orchestrator.Task{ClusterID: cluster}, manifest, mediaAuthorityControlCells(manifest))
}

// newMediaCellGate reads Commodore's database over SSH with the probe
// `cluster doctor` uses for the Media authority convergence check.
func newMediaCellGate(out io.Writer, rc *resolvedCluster, pool *ssh.Pool, sshKey string) *mediaCellGate {
	return &mediaCellGate{
		out: out, timeout: mediaCellGateTimeout, interval: mediaCellGateInterval, settle: mediaCellAttestationSettle,
		read: func(ctx context.Context, cell string) (mediaCellAuthority, error) {
			return readMediaCellAuthority(ctx, rc, pool, sshKey, cell)
		},
	}
}

// mediaCellAuthoritySQL reports whether Commodore records cell attestations,
// each attested cell's readiness, and the schema-1 tenants per ready cell.
const mediaCellAuthoritySQL = `SET default_transaction_read_only = on;
SET statement_timeout = '20s';
SELECT 'capabilities_table=' || (to_regclass('commodore.media_cell_placement_capabilities') IS NOT NULL)::int;
SELECT 'cell_ready:' || cell_id || '=' || (enforcement_ready AND max_schema_version >= 2)::int
  FROM commodore.media_cell_placement_capabilities;
` + legacySchemaTenantsOnReadyCellsSQL

func readMediaCellAuthority(ctx context.Context, rc *resolvedCluster, pool *ssh.Pool, sshKey, cell string) (mediaCellAuthority, error) {
	targets, err := postgresSnapshotTargets(rc)
	if err != nil {
		return mediaCellAuthority{}, fmt.Errorf("resolve database targets: %w", err)
	}
	for _, target := range targets {
		hasCommodore := false
		for _, database := range target.Databases {
			hasCommodore = hasCommodore || database == "commodore"
		}
		if !hasCommodore {
			continue
		}
		runner, err := snapshotRunner(pool, sshKey, target.Host, 60*time.Second)
		if err != nil {
			return mediaCellAuthority{}, fmt.Errorf("connect to %s: %w", target.HostName, err)
		}
		output, runErr := runner.RunScript(ctx, mediaAuthorityDatabaseScript(target, []mediaAuthorityDatabaseProbe{{Database: "commodore", SQL: mediaCellAuthoritySQL}}))
		var stdout string
		if output != nil {
			stdout = output.Stdout
		}
		state, parseErr := parseMediaCellAuthority(parseMediaAuthorityDoctorOutput(stdout)["commodore"], cell)
		if parseErr != nil {
			return mediaCellAuthority{}, fmt.Errorf("read commodore on %s: %w (%s)", target.HostName, parseErr, diagnosticCommandError(output, runErr))
		}
		return state, nil
	}
	return mediaCellAuthority{}, errors.New("no Commodore database in the manifest")
}

// parseMediaCellAuthority reads cell's state from the probe's keys. A probe
// that stopped before a count must not read as zero.
func parseMediaCellAuthority(values map[string]int64, cell string) (mediaCellAuthority, error) {
	tracked, ok := values["capabilities_table"]
	if !ok {
		return mediaCellAuthority{}, errors.New("the probe reported nothing")
	}
	if tracked == 0 {
		return mediaCellAuthority{}, nil
	}
	if _, ok := values["legacy_schema_tenants_on_ready_cells"]; !ok {
		return mediaCellAuthority{}, errors.New("the schema-1 tenant count was not reported")
	}
	return mediaCellAuthority{
		Tracked:       true,
		Ready:         values["cell_ready:"+cell] == 1,
		LegacyTenants: values["legacy_schema_tenants_cell:"+cell],
	}, nil
}
