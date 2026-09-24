package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"

	infra "github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/spf13/cobra"
)

// Release rollouts order hosts and replicas by criticality:
//
//   - CONTROL: the platform authority services and the data stores every
//     service depends on. One host at a time, the first one alone as a canary.
//   - MEDIA: the per-cell media plane. One host per cell at a time, cells in
//     parallel, so every cell keeps its remaining replicas serving.
//   - OTHER: analytics, event plumbing, webapps, and observability. A restart
//     degrades a feature but no request path, so these hosts converge together
//     (bounded).
//
// A host takes the most critical tier of anything it runs; a service not in
// the tables below is CONTROL, so a new service rolls one host at a time until
// it is classified. Waves run strictly in order and a failed wave starts no
// later wave, leaving every later host untouched.
type rolloutTier int

const (
	rolloutTierOther rolloutTier = iota
	rolloutTierMedia
	rolloutTierControl
)

func (t rolloutTier) String() string {
	switch t {
	case rolloutTierControl:
		return "control"
	case rolloutTierMedia:
		return "media"
	default:
		return "other"
	}
}

// rolloutOtherHostConcurrency bounds how many OTHER hosts converge at once.
const rolloutOtherHostConcurrency = 8

// rolloutServiceTiers classifies deploy names. Bridge is CONTROL: it is the
// API front door for every tenant, so all of its replicas restarting together
// is a full API outage. Privateer runs on every mesh host and is the subject of
// host convergence, so it never decides a host's tier.
var rolloutServiceTiers = map[string]rolloutTier{
	"quartermaster":    rolloutTierControl,
	"navigator":        rolloutTierControl,
	"commodore":        rolloutTierControl,
	"purser":           rolloutTierControl,
	"bridge":           rolloutTierControl,
	"postgres":         rolloutTierControl,
	"yugabyte":         rolloutTierControl,
	"kafka":            rolloutTierControl,
	"kafka-controller": rolloutTierControl,
	"clickhouse":       rolloutTierControl,

	"foghorn":          rolloutTierMedia,
	"chandler":         rolloutTierMedia,
	"livepeer-gateway": rolloutTierMedia,
	"livepeer-signer":  rolloutTierMedia,
	"helmsman":         rolloutTierMedia,
	"mistserver":       rolloutTierMedia,
	"redis":            rolloutTierMedia,

	"periscope-ingest":   rolloutTierOther,
	"periscope-query":    rolloutTierOther,
	"periscope-metering": rolloutTierOther,
	"decklog":            rolloutTierOther,
	"signalman":          rolloutTierOther,
	"bosun":              rolloutTierOther,
	"lookout":            rolloutTierOther,
	"skipper":            rolloutTierOther,
	"steward":            rolloutTierOther,
	"deckhand":           rolloutTierOther,
	"chartroom":          rolloutTierOther,
	"foredeck":           rolloutTierOther,
	"logbook":            rolloutTierOther,
	"kafka-mirrormaker":  rolloutTierOther,
	"nginx":              rolloutTierOther,
	"caddy":              rolloutTierOther,
	"vmagent":            rolloutTierOther,
	"vmauth":             rolloutTierOther,
	"vmalert":            rolloutTierOther,
	"alertmanager":       rolloutTierOther,
	"victoriametrics":    rolloutTierOther,
	"prometheus":         rolloutTierOther,
	"grafana":            rolloutTierOther,
	"metabase":           rolloutTierOther,
	"listmonk":           rolloutTierOther,
	"chatwoot":           rolloutTierOther,
}

func rolloutTierForDeploy(deploy string) rolloutTier {
	if tier, ok := rolloutServiceTiers[deploy]; ok {
		return tier
	}
	return rolloutTierControl
}

// rolloutHostTiers returns the tier of every host that runs something the
// manifest places, from the services, interfaces, observability components,
// and data infrastructure on it. Edge hosts are MEDIA. A host that runs only
// Privateer is absent and classifies as OTHER.
func rolloutHostTiers(manifest *inventory.Manifest) map[string]rolloutTier {
	tiers := map[string]rolloutTier{}
	if manifest == nil {
		return tiers
	}
	place := func(host string, tier rolloutTier) {
		host = strings.TrimSpace(host)
		if host == "" {
			return
		}
		if current, ok := tiers[host]; !ok || tier > current {
			tiers[host] = tier
		}
	}
	for _, group := range []map[string]inventory.ServiceConfig{manifest.Services, manifest.Interfaces, manifest.Observability} {
		for id, svc := range group {
			if !svc.Enabled {
				continue
			}
			deploy := releaseDeployName(manifest, id)
			if deploy == "privateer" {
				continue
			}
			for _, host := range serviceHosts(svc) {
				place(host, rolloutTierForDeploy(deploy))
			}
		}
	}
	infraCfg := manifest.Infrastructure
	if pg := infraCfg.Postgres; pg != nil && pg.Enabled {
		for _, host := range pg.AllHosts() {
			place(host, rolloutTierControl)
		}
	}
	if ch := infraCfg.ClickHouse; ch != nil && ch.Enabled {
		for _, host := range ch.AllHosts() {
			place(host, rolloutTierControl)
		}
	}
	for _, cluster := range allKafkaClusters(manifest) {
		for _, broker := range cluster.Brokers {
			place(broker.Host, rolloutTierControl)
		}
		for _, controller := range cluster.Controllers {
			place(controller.Host, rolloutTierControl)
		}
	}
	if k := infraCfg.Kafka; k != nil && k.Enabled && k.MirrorMaker != nil && k.MirrorMaker.Enabled {
		for _, link := range k.MirrorMaker.Links {
			for _, host := range link.Hosts {
				place(host, rolloutTierOther)
			}
		}
	}
	if r := infraCfg.Redis; r != nil && r.Enabled {
		for _, inst := range r.Instances {
			place(inst.Host, rolloutTierMedia)
			for _, host := range inst.ReplicaHosts {
				place(host, rolloutTierMedia)
			}
			for _, sentinel := range inst.Sentinels {
				place(sentinel.Host, rolloutTierMedia)
			}
		}
	}
	for name, host := range manifest.Hosts {
		if slices.Contains(host.Roles, infra.NodeTypeEdge) {
			place(name, rolloutTierMedia)
		}
	}
	return tiers
}

// rolloutCell is the failure domain MEDIA work is serialized within: the
// host's cluster, or the host itself when it declares none.
func rolloutCell(manifest *inventory.Manifest, hostName string) string {
	if manifest != nil {
		if host, ok := manifest.GetHost(hostName); ok && strings.TrimSpace(host.Cluster) != "" {
			return host.Cluster
		}
	}
	return hostName
}

// rolloutWave is one step of a rollout. Each lane runs its hosts in order;
// up to Limit lanes run at once.
type rolloutWave struct {
	Name  string
	Lanes [][]string
	Limit int
}

func (w rolloutWave) hosts() []string {
	var out []string
	for _, lane := range w.Lanes {
		out = append(out, lane...)
	}
	return out
}

func (w rolloutWave) concurrency() int {
	return max(1, min(w.Limit, len(w.Lanes)))
}

// describe renders the wave for plans and progress lines.
func (w rolloutWave) describe() string {
	switch {
	case w.concurrency() == 1:
		return fmt.Sprintf("%s: %s", w.Name, strings.Join(w.hosts(), " -> "))
	case len(w.Lanes) == len(w.hosts()):
		return fmt.Sprintf("%s (%d at a time): %s", w.Name, w.concurrency(), strings.Join(w.hosts(), ", "))
	default:
		lanes := make([]string, 0, len(w.Lanes))
		for _, lane := range w.Lanes {
			lanes = append(lanes, strings.Join(lane, " -> "))
		}
		return fmt.Sprintf("%s (%d lanes in parallel): %s", w.Name, w.concurrency(), strings.Join(lanes, " | "))
	}
}

// buildHostConvergenceWaves orders the hosts of one host-convergence step:
// a canary (the first CONTROL host, else the first host of the most critical
// tier present), the remaining CONTROL hosts one at a time, MEDIA hosts one
// per cell with cells in parallel, then OTHER hosts together. Hosts keep their
// input order within a tier.
func buildHostConvergenceWaves(hosts []string, tierOf func(string) rolloutTier, cellOf func(string) string) []rolloutWave {
	byTier := map[rolloutTier][]string{}
	for _, host := range hosts {
		tier := tierOf(host)
		byTier[tier] = append(byTier[tier], host)
	}
	var waves []rolloutWave
	for _, tier := range []rolloutTier{rolloutTierControl, rolloutTierMedia, rolloutTierOther} {
		if len(byTier[tier]) > 0 {
			waves = append(waves, rolloutWave{Name: "canary", Lanes: [][]string{{byTier[tier][0]}}, Limit: 1})
			byTier[tier] = byTier[tier][1:]
			break
		}
	}
	if control := byTier[rolloutTierControl]; len(control) > 0 {
		waves = append(waves, rolloutWave{Name: "control", Lanes: [][]string{control}, Limit: 1})
	}
	if media := byTier[rolloutTierMedia]; len(media) > 0 {
		lanes := laneByCell(media, cellOf)
		waves = append(waves, rolloutWave{Name: "media", Lanes: lanes, Limit: len(lanes)})
	}
	if other := byTier[rolloutTierOther]; len(other) > 0 {
		waves = append(waves, rolloutWave{Name: "other", Lanes: singleHostLanes(other), Limit: rolloutOtherHostConcurrency})
	}
	return waves
}

// buildComponentBatchWaves converges a low-risk host component: the first host
// of the most critical tier present runs alone as the canary, then the rest
// follow in batches of batch hosts, each batch its own wave so the caller's
// health gate runs between batches. Hosts keep their input order within a tier.
func buildComponentBatchWaves(hosts []string, tierOf func(string) rolloutTier, batch int) []rolloutWave {
	if len(hosts) == 0 {
		return nil
	}
	batch = max(batch, 1)
	ordered := make([]string, 0, len(hosts))
	for _, tier := range []rolloutTier{rolloutTierControl, rolloutTierMedia, rolloutTierOther} {
		for _, host := range hosts {
			if tierOf(host) == tier {
				ordered = append(ordered, host)
			}
		}
	}
	waves := []rolloutWave{{Name: "canary", Lanes: [][]string{{ordered[0]}}, Limit: 1}}
	for start := 1; start < len(ordered); start += batch {
		end := min(start+batch, len(ordered))
		group := ordered[start:end]
		waves = append(waves, rolloutWave{Name: fmt.Sprintf("batch %d", (start-1)/batch+1), Lanes: singleHostLanes(group), Limit: len(group)})
	}
	return waves
}

// buildReplicaWaves orders the replicas of one service upgrade by the
// service's tier. CONTROL replicas go one at a time, the first alone as the
// canary; MEDIA replicas go one per cell with cells in parallel; OTHER
// replicas go together. maxUnavailable (the service's update strategy) caps how
// many replicas are ever down at once, so it bounds the parallel lanes.
func buildReplicaWaves(tier rolloutTier, hosts []string, cellOf func(string) string, maxUnavailable int) []rolloutWave {
	if len(hosts) == 0 {
		return nil
	}
	maxUnavailable = max(maxUnavailable, 1)
	switch tier {
	case rolloutTierControl:
		waves := []rolloutWave{{Name: "canary", Lanes: [][]string{{hosts[0]}}, Limit: 1}}
		if len(hosts) > 1 {
			waves = append(waves, rolloutWave{Name: "control", Lanes: [][]string{hosts[1:]}, Limit: 1})
		}
		return waves
	case rolloutTierMedia:
		lanes := laneByCell(hosts, cellOf)
		return []rolloutWave{{Name: "media", Lanes: lanes, Limit: min(len(lanes), maxUnavailable)}}
	default:
		return []rolloutWave{{Name: "other", Lanes: singleHostLanes(hosts), Limit: min(len(hosts), maxUnavailable)}}
	}
}

// upgradeReplicaWaves orders one service upgrade's replica hosts by the
// deploy's tier, bounded by the service's effective update strategy (the
// baked-in default with the manifest's update_strategy override applied).
func upgradeReplicaWaves(manifest *inventory.Manifest, serviceName, deployName string, hosts []string) []rolloutWave {
	tier := rolloutTierForDeploy(deployName)
	if len(hosts) == 1 {
		return []rolloutWave{{Name: tier.String(), Lanes: [][]string{hosts}, Limit: 1}}
	}
	strategy := applyUpdateStrategyOverride(orchestrator.DefaultStrategyFor(deployName), updateStrategyConfigFor(manifest, serviceName))
	return buildReplicaWaves(tier, hosts, func(host string) string { return rolloutCell(manifest, host) }, strategy.MaxUnavailable)
}

// hostOutputCommand returns a command whose output streams are w, for per-host
// helpers that report through cmd.OutOrStdout and cmd.OutOrStderr.
func hostOutputCommand(parent *cobra.Command, w io.Writer) *cobra.Command {
	c := &cobra.Command{}
	c.SetOut(w)
	c.SetErr(w)
	if ctx := parent.Context(); ctx != nil {
		c.SetContext(ctx)
	}
	return c
}

func laneByCell(hosts []string, cellOf func(string) string) [][]string {
	index := map[string]int{}
	var lanes [][]string
	var cells []string
	for _, host := range hosts {
		cell := cellOf(host)
		i, ok := index[cell]
		if !ok {
			i = len(lanes)
			index[cell] = i
			lanes = append(lanes, nil)
			cells = append(cells, cell)
		}
		lanes[i] = append(lanes[i], host)
	}
	order := make([]int, len(lanes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return cells[order[a]] < cells[order[b]] })
	sorted := make([][]string, 0, len(lanes))
	for _, i := range order {
		sorted = append(sorted, lanes[i])
	}
	return sorted
}

func singleHostLanes(hosts []string) [][]string {
	lanes := make([][]string, 0, len(hosts))
	for _, host := range hosts {
		lanes = append(lanes, []string{host})
	}
	return lanes
}

// rolloutStoppedError reports a rollout that stopped at a failed wave. Hosts
// in NotStarted were never touched.
type rolloutStoppedError struct {
	Wave       string
	Failures   []error
	NotStarted []string
}

func (e *rolloutStoppedError) Error() string {
	msg := fmt.Sprintf("%s wave failed: %v", e.Wave, errors.Join(e.Failures...))
	if len(e.NotStarted) > 0 {
		msg += fmt.Sprintf("; not started (untouched): %s", strings.Join(e.NotStarted, ", "))
	}
	return msg
}

func (e *rolloutStoppedError) Unwrap() []error { return e.Failures }

// rolloutHostFunc converges or upgrades one host. out receives that host's
// progress; in a parallel wave it prefixes every line with the host name, and
// ctx routes Ansible's streamed output to the same writer.
type rolloutHostFunc func(ctx context.Context, host string, out io.Writer) error

// runRolloutWaves runs waves in order. A serial wave writes straight to out.
// A parallel wave writes each host's lines to out prefixed with "[host] ".
// Once any host fails, no further host starts (hosts already running finish),
// no later wave starts, and the returned *rolloutStoppedError names every host
// left untouched. afterWave, when set, gates the next wave on the hosts the
// wave just changed.
func runRolloutWaves(ctx context.Context, out io.Writer, waves []rolloutWave, run rolloutHostFunc, afterWave func(rolloutWave) error) error {
	for i, wave := range waves {
		fmt.Fprintf(out, "\n[wave %d/%d] %s\n", i+1, len(waves), wave.describe())
		started, failures := runRolloutWave(ctx, out, wave, run)
		if len(failures) == 0 && afterWave != nil {
			if err := afterWave(wave); err != nil {
				failures = append(failures, err)
			}
		}
		if len(failures) == 0 {
			continue
		}
		var notStarted []string
		for _, host := range wave.hosts() {
			if !started[host] {
				notStarted = append(notStarted, host)
			}
		}
		for _, later := range waves[i+1:] {
			notStarted = append(notStarted, later.hosts()...)
		}
		return &rolloutStoppedError{Wave: wave.Name, Failures: failures, NotStarted: notStarted}
	}
	return nil
}

func runRolloutWave(ctx context.Context, out io.Writer, wave rolloutWave, run rolloutHostFunc) (map[string]bool, []error) {
	started := map[string]bool{}
	if wave.concurrency() == 1 {
		for _, lane := range wave.Lanes {
			for _, host := range lane {
				started[host] = true
				if err := run(ctx, host, out); err != nil {
					return started, []error{fmt.Errorf("%s: %w", host, err)}
				}
			}
		}
		return started, nil
	}

	shared := &lockedWriter{w: out}
	var (
		mu       sync.Mutex
		failures []error
		wg       sync.WaitGroup
	)
	claim := func(host string) bool {
		mu.Lock()
		defer mu.Unlock()
		if len(failures) > 0 {
			return false
		}
		started[host] = true
		return true
	}
	sem := make(chan struct{}, wave.concurrency())
	for _, lane := range wave.Lanes {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, host := range lane {
				if !claim(host) {
					return
				}
				hostOut := &linePrefixWriter{w: shared, prefix: "[" + host + "] "}
				err := run(ansiblerun.WithOutputWriter(ctx, hostOut), host, hostOut)
				hostOut.Flush()
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Errorf("%s: %w", host, err))
					mu.Unlock()
					return
				}
			}
		})
	}
	wg.Wait()
	sort.Slice(failures, func(i, j int) bool { return failures[i].Error() < failures[j].Error() })
	return started, failures
}

// lockedWriter serializes writes from parallel hosts onto one writer.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// linePrefixWriter emits whole lines, each prefixed, so lines from parallel
// hosts never interleave mid-line. A trailing partial line is held until its
// newline or Flush.
type linePrefixWriter struct {
	mu     sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

func (l *linePrefixWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		line := make([]byte, 0, len(l.prefix)+i+1)
		line = append(line, l.prefix...)
		line = append(line, l.buf[:i+1]...)
		if _, err := l.w.Write(line); err != nil {
			return len(p), err
		}
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

// Flush writes a held partial line. Progress output is best effort: a failed
// write never changes a host's result.
func (l *linePrefixWriter) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) == 0 {
		return
	}
	line := append([]byte(l.prefix), l.buf...)
	line = append(line, '\n')
	l.buf = nil
	if _, err := l.w.Write(line); err != nil {
		return
	}
}
