package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func newClusterDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Typed change-kind survey: what would `cluster apply` need to touch?",
		Long: `Compare desired (manifest + release artifacts) against observed (sha256 of
files on each host) for every service that has registered a fingerprinter.

Diff kinds: binary, env, unit, cert, infra, unknown.

Services without a registered fingerprinter report 'unknown', which means
` + "`cluster apply --confirm`" + ` falls through to the heavy ` + "`cluster provision`" + `
path for them.

Exits non-zero when any kind diff is detected, so CI can gate on it.`,
		Example: `  frameworks cluster diff
  frameworks cluster diff --only-services foghorn,bridge
  frameworks cluster diff --only-hosts regional-eu-1
  frameworks cluster diff --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runClusterDiff(cmd, rc)
		},
	}
	cmd.Flags().String("output", "text", "output format: text | json")
	cmd.Flags().StringSlice("only-hosts", nil, "limit diff to these host names (comma-separated)")
	cmd.Flags().StringSlice("only-services", nil, "limit diff to these service names (comma-separated)")
	return cmd
}

type clusterDiffEntry struct {
	Host    string                           `json:"host"`
	Service string                           `json:"service"`
	Deploy  string                           `json:"deploy,omitempty"`
	Phase   orchestrator.Phase               `json:"phase,omitempty"`
	Kinds   []orchestrator.DiffKind          `json:"kinds"`
	Details map[orchestrator.DiffKind]string `json:"details,omitempty"`
	Modeled []detect.FileKind                `json:"modeled,omitempty"`
}

type clusterDiffSummary struct {
	Total   int `json:"total"`
	Changed int `json:"changed"`
	Unknown int `json:"unknown"`
}

type clusterDiffReport struct {
	Cluster string             `json:"cluster"`
	Entries []clusterDiffEntry `json:"entries"`
	Summary clusterDiffSummary `json:"summary"`
}

func runClusterDiff(cmd *cobra.Command, rc *resolvedCluster) error {
	manifest := rc.Manifest

	outputFmt := stringFlag(cmd, "output").Value
	if outputFmt == "" {
		outputFmt = "text"
	}
	if outputFmt != "text" && outputFmt != "json" {
		return fmt.Errorf("unsupported output format %q (want text or json)", outputFmt)
	}
	onlyHosts := stringSliceFlag(cmd, "only-hosts")
	onlyServices := stringSliceFlag(cmd, "only-services")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := fwssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	sharedEnv, err := rc.SharedEnv()
	if err != nil {
		return fmt.Errorf("load manifest env_files: %w", err)
	}
	clusterEnvs, err := rc.ClusterEnvs()
	if err != nil {
		return fmt.Errorf("load cluster env_files: %w", err)
	}
	runtimeData, err := buildFastPathRuntimeData(manifest, sharedEnv, filepath.Dir(rc.ManifestPath))
	if err != nil {
		return err
	}

	entries := collectClusterDiffEntries(ctx, clusterDiffCollection{
		Manifest:     manifest,
		SSHPool:      sshPool,
		OnlyHosts:    onlyHosts,
		OnlyServices: onlyServices,
		ManifestDir:  filepath.Dir(rc.ManifestPath),
		SharedEnv:    sharedEnv,
		ClusterEnvs:  clusterEnvs,
		RuntimeData:  runtimeData,
		ReleaseRepos: rc.ReleaseRepos,
	})

	rep := clusterDiffReport{
		Cluster: fmt.Sprintf("%s-%s", manifest.Type, manifest.Profile),
		Entries: entries,
		Summary: summarizeClusterDiff(entries),
	}

	if outputFmt == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		renderClusterDiffText(cmd.OutOrStdout(), rep)
	}

	if rep.Summary.Changed > 0 || rep.Summary.Unknown > 0 {
		return &ExitCodeError{
			Code:    1,
			Message: fmt.Sprintf("%d change(s), %d unknown across %d entries", rep.Summary.Changed, rep.Summary.Unknown, rep.Summary.Total),
		}
	}
	return nil
}

type clusterDiffCollection struct {
	Manifest     *inventory.Manifest
	SSHPool      *fwssh.Pool
	OnlyHosts    []string
	OnlyServices []string
	ManifestDir  string
	SharedEnv    map[string]string
	ClusterEnvs  map[string]map[string]string
	RuntimeData  map[string]any
	ReleaseRepos []string
}

type diffProbeTarget struct {
	service  string
	deploy   string
	phase    orchestrator.Phase
	hostName string
	host     inventory.Host
	desired  *detect.Fingerprint
	paths    []string
	modeled  []detect.FileKind
	unknown  string
}

type diffProvCacheEntry struct {
	prov provisioner.Provisioner
	err  error
}

func buildFastPathRuntimeData(manifest *inventory.Manifest, sharedEnv map[string]string, manifestDir string) (map[string]any, error) {
	runtimeData := map[string]any{}
	if token := strings.TrimSpace(sharedEnv["SERVICE_TOKEN"]); token != "" {
		runtimeData["service_token"] = token
	}
	if edgeTelemetryJWTRequired(manifest) {
		if err := ensureEdgeTelemetryJWTKeypair(runtimeData, sharedEnv); err != nil {
			return nil, fmt.Errorf("load edge telemetry jwt keypair: %w", err)
		}
	}
	if internalPKIBootstrapRequired(manifest) {
		pki, err := loadInternalPKIBootstrap(sharedEnv, manifestDir)
		if err != nil {
			return nil, fmt.Errorf("load internal PKI bootstrap material: %w", err)
		}
		runtimeData["internal_pki_bootstrap"] = pki
	}
	return runtimeData, nil
}

// collectClusterDiffEntries walks every enabled application/interface/
// observability service placement and produces one clusterDiffEntry per host.
// The desired side uses buildTaskConfig so deploy aliases, generated env, and
// release metadata match the regular provision path.
func collectClusterDiffEntries(ctx context.Context, opts clusterDiffCollection) []clusterDiffEntry {
	manifest := opts.Manifest
	sshPool := opts.SSHPool
	if manifest == nil {
		return nil
	}
	hostFilter := setFromSlice(opts.OnlyHosts)
	serviceFilter := setFromSlice(opts.OnlyServices)

	// Cache provisioners by deploy slug so aliases share one provisioner.
	provCache := map[string]diffProvCacheEntry{}

	var targets []diffProbeTarget

	targets = append(targets, collectServiceDiffTargets(ctx, manifest.Services, orchestrator.PhaseApplications, opts, hostFilter, serviceFilter, provCache)...)
	targets = append(targets, collectServiceDiffTargets(ctx, manifest.Interfaces, orchestrator.PhaseInterfaces, opts, hostFilter, serviceFilter, provCache)...)
	targets = append(targets, collectServiceDiffTargets(ctx, manifest.Observability, orchestrator.PhaseInterfaces, opts, hostFilter, serviceFilter, provCache)...)

	// Batch SSH probes per host: one shell-roundtrip even when a host has
	// many services to fingerprint.
	pathsByHost := map[string][]string{}
	hostsByName := map[string]inventory.Host{}
	for _, t := range targets {
		if t.unknown != "" {
			continue
		}
		pathsByHost[t.hostName] = append(pathsByHost[t.hostName], t.paths...)
		hostsByName[t.hostName] = t.host
	}
	observedByHost := map[string]map[string]string{}
	probeErrByHost := map[string]string{}
	for hostName, paths := range pathsByHost {
		host := hostsByName[hostName]
		dedup := dedupeSorted(paths)
		got, err := detect.ProbeSHA256(ctx, sshPool, host, dedup)
		if err != nil {
			// Mark every target on this host as unknown — we can't classify
			// without observed hashes. Better to be loud than guess.
			observedByHost[hostName] = nil
			probeErrByHost[hostName] = err.Error()
			continue
		}
		observedByHost[hostName] = got
	}

	entries := make([]clusterDiffEntry, 0, len(targets))
	for _, t := range targets {
		if t.unknown != "" {
			entries = append(entries, clusterDiffEntry{
				Host:    t.hostName,
				Service: t.service,
				Deploy:  t.deploy,
				Phase:   t.phase,
				Kinds:   []orchestrator.DiffKind{orchestrator.DiffUnknown},
				Details: map[orchestrator.DiffKind]string{orchestrator.DiffUnknown: t.unknown},
			})
			continue
		}
		observed := observedByHost[t.hostName]
		if observed == nil {
			detail := "ssh probe failed"
			if probeErrByHost[t.hostName] != "" {
				detail += ": " + probeErrByHost[t.hostName]
			}
			entries = append(entries, clusterDiffEntry{
				Host:    t.hostName,
				Service: t.service,
				Deploy:  t.deploy,
				Phase:   t.phase,
				Kinds:   []orchestrator.DiffKind{orchestrator.DiffUnknown},
				Details: map[orchestrator.DiffKind]string{orchestrator.DiffUnknown: detail},
				Modeled: t.modeled,
			})
			continue
		}
		hd := orchestrator.Classify(t.service, t.hostName, t.desired, observed)
		entries = append(entries, clusterDiffEntry{
			Host:    hd.Host,
			Service: hd.Service,
			Deploy:  t.deploy,
			Phase:   t.phase,
			Kinds:   hd.Kinds,
			Details: hd.Details,
			Modeled: t.modeled,
		})
	}

	// Stable order for output and tests.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Host != entries[j].Host {
			return entries[i].Host < entries[j].Host
		}
		return entries[i].Service < entries[j].Service
	})
	return entries
}

func collectServiceDiffTargets(
	ctx context.Context,
	services map[string]inventory.ServiceConfig,
	phase orchestrator.Phase,
	opts clusterDiffCollection,
	hostFilter map[string]bool,
	serviceFilter map[string]bool,
	provCache map[string]diffProvCacheEntry,
) []diffProbeTarget {
	manifest := opts.Manifest
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)

	var targets []diffProbeTarget

	for _, name := range names {
		svc := services[name]
		if !svc.Enabled {
			continue
		}
		deploy, err := resolveDeployName(name, svc)
		if err != nil {
			deploy = name
		}
		if len(serviceFilter) > 0 && !serviceFilter[name] && !serviceFilter[deploy] {
			continue
		}
		srcHosts := diffServiceHosts(name, svc, manifest)
		if len(srcHosts) == 0 {
			continue
		}
		hosts := append([]string(nil), srcHosts...)
		sort.Strings(hosts)

		ce, ok := provCache[deploy]
		if !ok {
			prov, err := provisioner.GetProvisioner(deploy, opts.SSHPool)
			ce = diffProvCacheEntry{prov: prov, err: err}
			provCache[deploy] = ce
		}

		for _, hostName := range hosts {
			if len(hostFilter) > 0 && !hostFilter[hostName] {
				continue
			}
			host, ok := manifest.GetHost(hostName)
			if !ok {
				continue
			}
			base := diffProbeTarget{service: name, deploy: deploy, phase: phase, hostName: hostName, host: host}

			if ce.err != nil {
				base.unknown = ce.err.Error()
				targets = append(targets, base)
				continue
			}
			fp, ok := ce.prov.(provisioner.Fingerprinter)
			if !ok {
				base.unknown = "provisioner does not implement Fingerprinter"
				targets = append(targets, base)
				continue
			}
			task := orchestrator.NewServiceTask(deploy, name, hostName, hostName, phase)
			cfg, err := buildTaskConfig(task, manifest, opts.RuntimeData, false, opts.ManifestDir, opts.SharedEnv, opts.ClusterEnvs, opts.ReleaseRepos)
			if err != nil {
				base.unknown = err.Error()
				targets = append(targets, base)
				continue
			}
			desired, err := fp.Fingerprint(ctx, host, cfg)
			if err != nil {
				base.unknown = err.Error()
				targets = append(targets, base)
				continue
			}
			if desired == nil || len(desired.Files) == 0 {
				base.unknown = "no kinds modeled for this service yet"
				targets = append(targets, base)
				continue
			}

			base.desired = desired
			for _, kind := range desired.SortedKinds() {
				base.modeled = append(base.modeled, kind)
				if p := desired.Files[kind].Path; p != "" {
					base.paths = append(base.paths, p)
				}
			}
			targets = append(targets, base)
		}
	}
	return targets
}

func diffServiceHosts(name string, svc inventory.ServiceConfig, manifest *inventory.Manifest) []string {
	switch name {
	case "privateer":
		return orchestrator.EffectivePrivateerHostsForManifest(svc, manifest)
	case "vmagent":
		return orchestrator.EffectiveVMAgentHosts(svc, manifest)
	default:
		return serviceHosts(svc)
	}
}

func summarizeClusterDiff(entries []clusterDiffEntry) clusterDiffSummary {
	s := clusterDiffSummary{Total: len(entries)}
	for _, e := range entries {
		if len(e.Kinds) == 0 {
			continue
		}
		hasUnknown := false
		hasOther := false
		for _, k := range e.Kinds {
			if k == orchestrator.DiffUnknown {
				hasUnknown = true
			} else {
				hasOther = true
			}
		}
		if hasUnknown {
			s.Unknown++
		}
		if hasOther {
			s.Changed++
		}
	}
	return s
}

func renderClusterDiffText(w io.Writer, rep clusterDiffReport) {
	fmt.Fprintf(w, "Cluster: %s\n\n", rep.Cluster)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tSERVICE\tDIFF\tDETAIL")
	for _, e := range rep.Entries {
		diffStr := "ok"
		detail := "-"
		if len(e.Kinds) > 0 {
			kinds := make([]string, 0, len(e.Kinds))
			for _, k := range e.Kinds {
				kinds = append(kinds, string(k))
			}
			diffStr = strings.Join(kinds, ",")
			// Show details for the first kind with content.
			for _, k := range e.Kinds {
				if d, ok := e.Details[k]; ok && d != "" {
					detail = d
					break
				}
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Host, e.Service, diffStr, detail)
	}
	_ = tw.Flush()

	fmt.Fprintln(w)
	switch {
	case rep.Summary.Changed == 0 && rep.Summary.Unknown == 0:
		fmt.Fprintf(w, "No diffs across %d entries\n", rep.Summary.Total)
	case rep.Summary.Changed > 0 && rep.Summary.Unknown == 0:
		fmt.Fprintf(w, "%d changed across %d entries (all classifiable)\n", rep.Summary.Changed, rep.Summary.Total)
	default:
		fmt.Fprintf(w, "%d changed, %d unknown across %d entries — unknown requires `cluster provision`\n",
			rep.Summary.Changed, rep.Summary.Unknown, rep.Summary.Total)
	}
}

func setFromSlice(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out[s] = true
		}
	}
	return out
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func strDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func stringSliceFlag(cmd *cobra.Command, name string) []string {
	f := cmd.Flag(name)
	if f == nil {
		return nil
	}
	v := strings.Trim(f.Value.String(), "[]")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
