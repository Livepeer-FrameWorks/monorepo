package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/remoteaccess"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

const clusterDiffTimeout = 30 * time.Minute

func newClusterDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Typed change-kind survey: what would `cluster apply` need to touch?",
		Long: `Compare desired (manifest + release artifacts) against observed host state.
Every service is checked through the same read-only Ansible planner used by
live provisioning. File fingerprints add binary/env/unit/cert detail where
available; changes outside that typed model are reported as infra.

Diff kinds: binary, env, unit, cert, infra, unknown.

Unknown means the authoritative check could not complete. It is informational:
it is not evidence of drift and does not prescribe any mutation.

Exits non-zero only when a classifiable diff is detected, so CI can gate on
proven drift without treating unmodeled state as changed.`,
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

	ctx, cancel := context.WithTimeout(context.Background(), clusterDiffTimeout)
	defer cancel()

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := fwssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	sharedEnv, err := rc.PreparedSharedEnv()
	if err != nil {
		return fmt.Errorf("load manifest env_files: %w", err)
	}
	clusterEnvs, err := rc.ClusterEnvs()
	if err != nil {
		return fmt.Errorf("load cluster env_files: %w", err)
	}
	runtimeData, err := buildFastPathRuntimeData(ctx, manifest, sharedEnv, filepath.Dir(rc.ManifestPath), sshKey)
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

	return clusterDiffExitError(rep.Summary)
}

func clusterDiffExitError(summary clusterDiffSummary) error {
	if summary.Changed == 0 {
		return nil
	}
	return &ExitCodeError{
		Code:    1,
		Message: fmt.Sprintf("%d proven change(s), %d unknown across %d entries", summary.Changed, summary.Unknown, summary.Total),
	}
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
	prov     provisioner.Provisioner
	config   provisioner.ServiceConfig
	desired  *detect.Fingerprint
	paths    []string
	modeled  []detect.FileKind
	unknown  string
	fpError  string
}

type authoritativeDiff struct {
	changed bool
	tasks   []string
	err     error
}

type diffProvCacheEntry struct {
	prov provisioner.Provisioner
	err  error
}

func buildFastPathRuntimeData(ctx context.Context, manifest *inventory.Manifest, sharedEnv map[string]string, manifestDir, sshKey string) (map[string]any, error) {
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
	qm, hasQuartermaster := manifest.Services["quartermaster"]
	if !hasQuartermaster || !qm.Enabled {
		return runtimeData, nil
	}
	if strings.TrimSpace(sharedEnv["SERVICE_TOKEN"]) == "" {
		return nil, fmt.Errorf("build authoritative desired state: SERVICE_TOKEN is required to resolve control-plane identities")
	}
	sess, err := remoteaccess.OpenSession(remoteaccess.Options{
		Manifest:      manifest,
		SSHKeyPath:    sshKey,
		AllowInsecure: isDevProfile(manifest),
	})
	if err != nil {
		return nil, fmt.Errorf("build authoritative desired state: open control-plane session: %w", err)
	}
	defer sess.Close()
	ownerTenantIDs, err := resolveClusterOwnerTenantIDs(ctx, manifest, runtimeData, sess)
	if err != nil {
		return nil, fmt.Errorf("build authoritative desired state: resolve cluster owners: %w", err)
	}
	systemTenantID := strings.TrimSpace(ownerTenantIDs[bootstrap.SystemTenantAlias])
	if systemTenantID == "" {
		return nil, fmt.Errorf("build authoritative desired state: Quartermaster returned no system tenant UUID")
	}
	runtimeData["system_tenant_id"] = systemTenantID
	runtimeData["owner_tenant_ids_by_alias"] = ownerTenantIDs
	if qmAddr, addrErr := resolveServiceGRPCAddr(manifest, "quartermaster", defaultGRPCPort("quartermaster")); addrErr == nil {
		runtimeData["quartermaster_grpc_addr"] = qmAddr
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

	authoritative := inspectClusterDiffTargets(ctx, targets)
	entries := make([]clusterDiffEntry, 0, len(targets))
	for i, t := range targets {
		entries = append(entries, classifyClusterDiffTarget(t, authoritative[i], observedByHost[t.hostName], probeErrByHost[t.hostName]))
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

func classifyClusterDiffTarget(t diffProbeTarget, inspection authoritativeDiff, observed map[string]string, probeErr string) clusterDiffEntry {
	entry := clusterDiffEntry{Host: t.hostName, Service: t.service, Deploy: t.deploy, Phase: t.phase, Modeled: t.modeled}
	if t.unknown != "" {
		entry.Kinds = []orchestrator.DiffKind{orchestrator.DiffUnknown}
		entry.Details = map[orchestrator.DiffKind]string{orchestrator.DiffUnknown: t.unknown}
		return entry
	}
	if inspection.err != nil {
		entry.Kinds = []orchestrator.DiffKind{orchestrator.DiffUnknown}
		entry.Details = map[orchestrator.DiffKind]string{orchestrator.DiffUnknown: "authoritative Ansible check failed: " + inspection.err.Error()}
		return entry
	}
	if !inspection.changed {
		return entry
	}
	if len(t.modeled) > 0 && observed == nil {
		detail := ansibleChangeDetail(inspection.tasks, "authoritative Ansible check reports managed-state changes; typed fingerprint probe failed")
		if probeErr != "" {
			detail += ": " + probeErr
		}
		entry.Kinds = inferredAnsibleKinds(inspection.tasks)
		if len(entry.Kinds) == 0 {
			entry.Kinds = []orchestrator.DiffKind{orchestrator.DiffInfra}
		}
		entry.Details = detailsForKinds(entry.Kinds, detail)
		return entry
	}
	hd := orchestrator.Classify(t.service, t.hostName, t.desired, observed)
	if len(hd.Kinds) == 0 || (len(hd.Kinds) == 1 && hd.Kinds[0] == orchestrator.DiffUnknown) {
		detail := ansibleChangeDetail(inspection.tasks, "authoritative Ansible check reports managed-state changes outside the typed file model")
		if t.fpError != "" {
			detail += ": " + t.fpError
		}
		hd.Kinds = inferredAnsibleKinds(inspection.tasks)
		if len(hd.Kinds) == 0 {
			hd.Kinds = []orchestrator.DiffKind{orchestrator.DiffInfra}
		}
		hd.Details = detailsForKinds(hd.Kinds, detail)
	} else {
		for _, kind := range inferredAnsibleKinds(inspection.tasks) {
			if !slices.Contains(hd.Kinds, kind) {
				hd.Kinds = append(hd.Kinds, kind)
				hd.Details[kind] = ansibleChangeDetail(inspection.tasks, "authoritative Ansible task reported this change")
			}
		}
	}
	sort.Slice(hd.Kinds, func(i, j int) bool { return hd.Kinds[i] < hd.Kinds[j] })
	entry.Kinds, entry.Details = hd.Kinds, hd.Details
	return entry
}

func inferredAnsibleKinds(tasks []string) []orchestrator.DiffKind {
	var kinds []orchestrator.DiffKind
	for _, task := range tasks {
		name := strings.ToLower(task)
		kind := orchestrator.DiffKind("")
		switch {
		case strings.Contains(name, "reinstall under --check"), strings.Contains(name, "binary integrity"),
			strings.Contains(name, "install service binary"), strings.Contains(name, "install privateer binary"):
			kind = orchestrator.DiffBinary
		case strings.Contains(name, "certificate"), strings.Contains(name, "service pki"), strings.Contains(name, " tls "):
			kind = orchestrator.DiffCert
		case strings.Contains(name, "environment file"), strings.Contains(name, "service environment"):
			kind = orchestrator.DiffEnv
		case strings.Contains(name, "systemd unit"), strings.Contains(name, "unit file"):
			kind = orchestrator.DiffUnit
		default:
			kind = orchestrator.DiffInfra
		}
		if kind != "" && !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

func ansibleChangeDetail(tasks []string, fallback string) string {
	if len(tasks) == 0 {
		return fallback
	}
	const maxTasks = 4
	shown := tasks
	if len(shown) > maxTasks {
		shown = shown[:maxTasks]
	}
	detail := "changed tasks: " + strings.Join(shown, "; ")
	if remaining := len(tasks) - len(shown); remaining > 0 {
		detail += fmt.Sprintf("; +%d more", remaining)
	}
	return detail
}

func detailsForKinds(kinds []orchestrator.DiffKind, detail string) map[orchestrator.DiffKind]string {
	details := make(map[orchestrator.DiffKind]string, len(kinds))
	for _, kind := range kinds {
		details[kind] = detail
	}
	return details
}

// inspectClusterDiffTargets runs the same read-only Ansible check-mode planner
// used by live provisioning. Fingerprints provide typed detail, but this result
// decides whether the role would actually mutate the host.
func inspectClusterDiffTargets(ctx context.Context, targets []diffProbeTarget) []authoritativeDiff {
	results := make([]authoritativeDiff, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	limit := make(chan struct{}, 6)
	for i := range targets {
		i := i
		target := targets[i]
		if target.unknown != "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				mu.Lock()
				results[i] = authoritativeDiff{err: ctx.Err()}
				mu.Unlock()
				return
			}
			result := authoritativeDiff{}
			if inspector, ok := target.prov.(provisioner.ChangeInspector); ok {
				inspection, err := inspector.InspectChanges(ctx, target.host, target.config, nil)
				result.changed = inspection.Changed
				result.tasks = inspection.Tasks
				result.err = err
			} else if planner, ok := target.prov.(provisioner.ChangePlanner); ok {
				result.changed, result.err = planner.WouldChange(ctx, target.host, target.config, nil)
			} else {
				result.err = fmt.Errorf("provisioner does not implement change precheck")
			}
			mu.Lock()
			results[i] = result
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
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
			base := diffProbeTarget{service: name, deploy: deploy, phase: phase, hostName: hostName, host: host, prov: ce.prov}

			if ce.err != nil {
				base.unknown = ce.err.Error()
				targets = append(targets, base)
				continue
			}
			task := newClusterDiffTask(deploy, name, hostName, phase, manifest)
			cfg, err := buildTaskConfig(task, manifest, opts.RuntimeData, false, opts.ManifestDir, opts.SharedEnv, opts.ClusterEnvs, opts.ReleaseRepos)
			if err != nil {
				base.unknown = err.Error()
				targets = append(targets, base)
				continue
			}
			base.config = cfg
			if fp, ok := ce.prov.(provisioner.Fingerprinter); ok {
				desired, fpErr := fp.Fingerprint(ctx, host, cfg)
				if fpErr != nil {
					base.fpError = fpErr.Error()
				} else {
					base.desired = desired
				}
			}
			if base.desired != nil {
				for _, kind := range base.desired.SortedKinds() {
					base.modeled = append(base.modeled, kind)
					if p := base.desired.Files[kind].Path; p != "" {
						base.paths = append(base.paths, p)
					}
				}
			}
			targets = append(targets, base)
		}
	}
	return targets
}

func newClusterDiffTask(deploy, serviceID, hostName string, phase orchestrator.Phase, manifest *inventory.Manifest) *orchestrator.Task {
	task := orchestrator.NewServiceTask(deploy, serviceID, hostName, hostName, phase)
	task.ClusterID = effectiveApplyCluster(manifest, serviceID, hostName)
	return task
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
		fmt.Fprintf(w, "%d changed, %d unknown across %d entries — unknown entries were not compared and do not imply drift\n",
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
