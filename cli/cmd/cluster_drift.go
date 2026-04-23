package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

const (
	driftClusterOK           = "ok"
	driftClusterMissing      = "missing"
	driftClusterStopped      = "stopped"
	driftClusterWrongMode    = "wrong_mode"
	driftClusterWrongVersion = "wrong_version"
)

type clusterDriftEntry struct {
	Host    string `json:"host"`
	Service string `json:"service"`
	Status  string `json:"status"`
	Mode    string `json:"mode,omitempty"`
	Version string `json:"version,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type clusterDriftSummary struct {
	Total       int `json:"total"`
	Divergences int `json:"divergences"`
}

type clusterDriftReport struct {
	Cluster         string              `json:"cluster"`
	Channel         string              `json:"channel"`
	PlatformVersion string              `json:"platform_version"`
	Entries         []clusterDriftEntry `json:"entries"`
	Summary         clusterDriftSummary `json:"summary"`
}

func newClusterDriftCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drift",
		Short: "Observed-state survey: services across cluster hosts",
		Long: `Survey each enabled service on its configured host and report divergence
from the manifest: missing, stopped, wrong_mode, or wrong_version. Reuses
pkg/detect's multi-method probe (inventory / docker / systemd / port / health).

Exits non-zero on any divergence, so CI can gate on it.`,
		Example: `  frameworks cluster drift
  frameworks cluster drift --manifest /etc/frameworks/cluster.yaml
  frameworks cluster drift --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runClusterDrift(cmd, rc)
		},
	}
	return cmd
}

func runClusterDrift(cmd *cobra.Command, rc *resolvedCluster) error {
	manifest := rc.Manifest
	channel := manifest.ResolvedChannel()
	gitopsChannel, gitopsVersion := gitops.ResolveVersion(channel)

	gitopsManifest, fetchErr := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, gitopsChannel, gitopsVersion)
	if fetchErr != nil {
		return fmt.Errorf("failed to fetch gitops manifest (channel: %s): %w", channel, fetchErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := fwssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	entries := collectClusterDriftEntries(ctx, manifest, gitopsManifest, sshPool)

	rep := clusterDriftReport{
		Cluster:         fmt.Sprintf("%s-%s", manifest.Type, manifest.Profile),
		Channel:         channel,
		PlatformVersion: gitopsManifest.PlatformVersion,
		Entries:         entries,
		Summary: clusterDriftSummary{
			Total:       len(entries),
			Divergences: countClusterDriftDivergences(entries),
		},
	}

	jsonMode := output == "json"
	if jsonMode {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		renderClusterDriftText(cmd.OutOrStdout(), rep)
	}

	if rep.Summary.Divergences > 0 {
		return &ExitCodeError{Code: 1, Message: fmt.Sprintf("%d divergence(s) detected", rep.Summary.Divergences)}
	}
	return nil
}

// clusterDetector is the minimal surface collectClusterDriftEntries needs
// from pkg/detect. Split out so tests can supply a stub instead of a real
// SSH pool.
type clusterDetector interface {
	Detect(ctx context.Context, serviceName string) (*detect.ServiceState, error)
}

type clusterDetectorFactory func(host inventory.Host) clusterDetector

func collectClusterDriftEntries(ctx context.Context, manifest *inventory.Manifest, gitopsManifest *gitops.Manifest, sshPool *fwssh.Pool) []clusterDriftEntry {
	factory := func(host inventory.Host) clusterDetector {
		return detect.NewDetector(sshPool, host)
	}
	return collectClusterDriftEntriesWith(ctx, manifest, gitopsManifest, factory)
}

// clusterDriftTarget identifies one probe site. Display is the operator-
// facing label, Deploy is the name Detector.Detect receives (must match
// provisioner BaseProvisioner names), and Role distinguishes instances
// or roles that share a Deploy family name.
type clusterDriftTarget struct {
	Host          string
	Display       string
	Deploy        string
	Role          string
	DesiredMode   string
	PinnedVersion string
}

func collectClusterDriftEntriesWith(ctx context.Context, manifest *inventory.Manifest, gitopsManifest *gitops.Manifest, newDetector clusterDetectorFactory) []clusterDriftEntry {
	targets := buildClusterDriftTargets(manifest)
	entries := make([]clusterDriftEntry, 0, len(targets))
	for _, t := range targets {
		host, ok := manifest.GetHost(t.Host)
		if !ok {
			continue
		}
		detector := newDetector(host)
		state, detectErr := detector.Detect(ctx, t.Deploy)
		available := resolveDesiredVersion(t.PinnedVersion, gitopsManifest, t.Deploy)
		entries = append(entries, classifyClusterService(t.Host, t.Display, t.DesiredMode, available, state, detectErr))
	}
	return entries
}

func buildClusterDriftTargets(manifest *inventory.Manifest) []clusterDriftTarget {
	var targets []clusterDriftTarget

	appendService := func(name string, svc inventory.ServiceConfig) {
		if !svc.Enabled {
			return
		}
		hostName := svc.Host
		if hostName == "" && len(svc.Hosts) > 0 {
			hostName = svc.Hosts[0]
		}
		if hostName == "" {
			return
		}
		deployName, resolveErr := resolveDeployName(name, svc)
		if resolveErr != nil {
			return
		}
		targets = append(targets, clusterDriftTarget{
			Host: hostName, Display: name, Deploy: deployName,
			DesiredMode: svc.Mode, PinnedVersion: svc.Version,
		})
	}

	names := func(m map[string]inventory.ServiceConfig) []string {
		out := make([]string, 0, len(m))
		for n := range m {
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}
	for _, n := range names(manifest.Services) {
		appendService(n, manifest.Services[n])
	}
	for _, n := range names(manifest.Interfaces) {
		appendService(n, manifest.Interfaces[n])
	}
	for _, n := range names(manifest.Observability) {
		appendService(n, manifest.Observability[n])
	}

	// Infrastructure. Each component keeps one target per physical host so
	// multi-node clusters (yugabyte, zookeeper, kafka, redis) stay
	// reportable per-instance. Deploy names must match provisioner
	// BaseProvisioner registrations in cli/pkg/provisioner/*.go — Kafka
	// controllers and Redis instances have distinct names from the family.
	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		deploy := "postgres"
		if pg.IsYugabyte() {
			deploy = "yugabyte"
		}
		for _, h := range pg.AllHosts() {
			targets = append(targets, clusterDriftTarget{
				Host: h, Display: deploy, Deploy: deploy,
				DesiredMode: pg.Mode, PinnedVersion: pg.Version,
			})
		}
	}
	if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled && ch.Host != "" {
		targets = append(targets, clusterDriftTarget{
			Host: ch.Host, Display: "clickhouse", Deploy: "clickhouse",
			DesiredMode: ch.Mode, PinnedVersion: ch.Version,
		})
	}
	if kf := manifest.Infrastructure.Kafka; kf != nil && kf.Enabled {
		for _, br := range kf.Brokers {
			targets = append(targets, clusterDriftTarget{
				Host: br.Host, Display: "kafka:broker", Deploy: "kafka",
				DesiredMode: kf.Mode, PinnedVersion: kf.Version,
			})
		}
		for _, co := range kf.Controllers {
			targets = append(targets, clusterDriftTarget{
				Host: co.Host, Display: "kafka:controller", Deploy: "kafka-controller",
				DesiredMode: kf.Mode, PinnedVersion: kf.Version,
			})
		}
	}
	if zk := manifest.Infrastructure.Zookeeper; zk != nil && zk.Enabled {
		for _, node := range zk.Ensemble {
			targets = append(targets, clusterDriftTarget{
				Host: node.Host, Display: "zookeeper", Deploy: "zookeeper",
				DesiredMode: zk.Mode, PinnedVersion: zk.Version,
			})
		}
	}
	if rd := manifest.Infrastructure.Redis; rd != nil && rd.Enabled {
		for _, inst := range rd.Instances {
			deploy := "redis-" + inst.Name
			targets = append(targets, clusterDriftTarget{
				Host: inst.Host, Display: "redis:" + inst.Name, Deploy: deploy,
				DesiredMode: rd.Mode, PinnedVersion: rd.Version,
			})
		}
	}
	return targets
}

// resolveDesiredVersion returns the manifest pin if set, else the channel
// manifest's service version, else empty. Provisioning treats the pin as an
// override (cluster_provision.go:1517+); drift has to agree or pinned
// deployments read as drifted.
func resolveDesiredVersion(pinnedVersion string, gitopsManifest *gitops.Manifest, deployName string) string {
	if pinnedVersion != "" {
		return pinnedVersion
	}
	if gitopsManifest == nil {
		return ""
	}
	svcInfo, err := gitopsManifest.GetServiceInfo(deployName)
	if err != nil {
		return ""
	}
	return svcInfo.Version
}

// classifyClusterService turns a detect.ServiceState into a drift entry. A
// nil state or detection error reports `missing` — the explicit rule mirrors
// edge-drift's "omitted from both stacks" case. desiredMode == "" disables
// mode checking; available == "" disables version checking.
func classifyClusterService(hostName, svcName, desiredMode, available string, state *detect.ServiceState, detectErr error) clusterDriftEntry {
	entry := clusterDriftEntry{Host: hostName, Service: svcName}

	if detectErr != nil || state == nil || !state.Exists {
		entry.Status = driftClusterMissing
		if detectErr != nil {
			entry.Detail = detectErr.Error()
		}
		return entry
	}

	entry.Mode = state.Mode
	entry.Version = state.Version

	if !state.Running {
		entry.Status = driftClusterStopped
		return entry
	}
	if desiredMode != "" && state.Mode != "" && state.Mode != desiredMode {
		entry.Status = driftClusterWrongMode
		entry.Detail = fmt.Sprintf("have %s, want %s", state.Mode, desiredMode)
		return entry
	}
	if available != "" && state.Version != "" && state.Version != available {
		entry.Status = driftClusterWrongVersion
		entry.Detail = fmt.Sprintf("have %s, want %s", state.Version, available)
		return entry
	}
	entry.Status = driftClusterOK
	return entry
}

func countClusterDriftDivergences(entries []clusterDriftEntry) int {
	n := 0
	for _, e := range entries {
		if e.Status != driftClusterOK {
			n++
		}
	}
	return n
}

func renderClusterDriftText(w io.Writer, rep clusterDriftReport) {
	fmt.Fprintf(w, "Cluster: %s (channel: %s, platform: %s)\n\n", rep.Cluster, rep.Channel, rep.PlatformVersion)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tSERVICE\tSTATUS\tDETAIL")
	for _, e := range rep.Entries {
		detail := e.Detail
		if detail == "" {
			if e.Version != "" && e.Mode != "" {
				detail = fmt.Sprintf("%s (%s)", e.Version, e.Mode)
			} else if e.Version != "" {
				detail = e.Version
			} else if e.Mode != "" {
				detail = e.Mode
			} else {
				detail = "-"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Host, e.Service, e.Status, detail)
	}
	_ = tw.Flush()

	fmt.Fprintln(w)
	if rep.Summary.Divergences == 0 {
		fmt.Fprintf(w, "No drift detected (%d services)\n", rep.Summary.Total)
	} else {
		fmt.Fprintf(w, "%d divergence(s) in %d services\n", rep.Summary.Divergences, rep.Summary.Total)
	}
}
