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

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/spf13/cobra"
)

func newClusterApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Plan or execute a typed rolling service update",
		Long: `Compose typed diffs (cluster diff) with per-service rolling-update
strategy (max_unavailable, region_stagger, canary, primary_last) and
print the waves ` + "`cluster apply --confirm`" + ` executes.

Refuses any topology that contains an ` + "`unknown`" + ` or ` + "`infra`" + ` diff —
those go through ` + "`cluster provision`" + ` instead.

Without --confirm this command is read-only.`,
		Example: `  frameworks cluster apply
  frameworks cluster apply --only-services foghorn,bridge
  frameworks cluster apply --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runClusterApply(cmd, rc)
		},
	}
	cmd.Flags().String("output", "text", "output format: text | json")
	cmd.Flags().StringSlice("only-hosts", nil, "limit plan to these host names (comma-separated)")
	cmd.Flags().StringSlice("only-services", nil, "limit plan to these service names (comma-separated)")
	cmd.Flags().Bool("confirm", false, "actually execute the rolling-update plan (default: dry-run only)")
	return cmd
}

// clusterApplyHost is one host's slot inside a wave: which host + what
// diff kinds the rolling update would apply there.
type clusterApplyHost struct {
	Host    string                  `json:"host"`
	Kinds   []orchestrator.DiffKind `json:"kinds"`
	Details []string                `json:"details,omitempty"`
}

// clusterApplyWave is one wave for one service. Hosts in a wave run in
// parallel; the executor waits for every host's readiness gate before the
// next wave starts.
type clusterApplyWave struct {
	Index int                `json:"index"`
	Hosts []clusterApplyHost `json:"hosts"`
}

// clusterApplyService is the per-service plan: which strategy applied,
// which waves came out.
type clusterApplyService struct {
	Service  string                      `json:"service"`
	Deploy   string                      `json:"deploy,omitempty"`
	Strategy orchestrator.UpdateStrategy `json:"strategy"`
	Waves    []clusterApplyWave          `json:"waves"`
}

// clusterApplyReport is the top-level read-only artifact. Skipped lists
// the entries that would block execution (unknown/infra kinds) so an
// operator can see exactly which services need `cluster provision`.
type clusterApplyReport struct {
	Cluster  string                `json:"cluster"`
	Services []clusterApplyService `json:"services"`
	Skipped  []clusterDiffEntry    `json:"skipped,omitempty"`
}

func runClusterApply(cmd *cobra.Command, rc *resolvedCluster) error {
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
	confirm := boolFlag(cmd, "confirm")
	if confirm && outputFmt == "json" {
		return fmt.Errorf("--output json is only supported for dry-run plans")
	}

	// Confirm runs can take much longer than dry-runs — every host
	// restart + gate wait is N×30s in the worst case. Lift the timeout
	// generously; the gate per-host timeout still bounds individual host
	// progress, and ctrl-C cancels the whole rollout cleanly.
	timeout := 60 * time.Second
	if confirm {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
	manifestDir := filepath.Dir(rc.ManifestPath)
	runtimeData, err := buildFastPathRuntimeData(manifest, sharedEnv, manifestDir)
	if err != nil {
		return err
	}

	entries := collectClusterDiffEntries(ctx, clusterDiffCollection{
		Manifest:     manifest,
		SSHPool:      sshPool,
		OnlyHosts:    onlyHosts,
		OnlyServices: onlyServices,
		ManifestDir:  manifestDir,
		SharedEnv:    sharedEnv,
		ClusterEnvs:  clusterEnvs,
		RuntimeData:  runtimeData,
		ReleaseRepos: rc.ReleaseRepos,
	})

	rep := buildClusterApplyReport(manifest, entries)

	if outputFmt == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		renderClusterApplyText(cmd.OutOrStdout(), rep)
	}

	// Refuse-on-unknown is the fast-path safety guarantee. Skipped is
	// non-empty whenever a host's diff includes unknown or infra — those
	// must go through `cluster provision`. Exit non-zero so CI gates
	// catch them before someone runs apply for real.
	if len(rep.Skipped) > 0 {
		return &ExitCodeError{
			Code: 1,
			Message: fmt.Sprintf("%d entr(y/ies) contain unknown or infra diffs; run `cluster provision` for those",
				len(rep.Skipped)),
		}
	}

	if !confirm {
		return nil
	}

	// --confirm: actually execute the plan.
	return executeClusterApply(ctx, cmd.OutOrStdout(), rep, manifest, sshPool,
		manifestDir, sharedEnv, clusterEnvs, runtimeData, rc.ReleaseRepos)
}

// executeClusterApply drives the rolling-update mutation path. Each host
// action reuses the regular provisioner with a host-scoped ServiceConfig, so
// install/configure/service handlers remain the single writer for binaries,
// env files, units, and validation.
func executeClusterApply(
	ctx context.Context,
	w io.Writer,
	rep clusterApplyReport,
	manifest *inventory.Manifest,
	sshPool *fwssh.Pool,
	manifestDir string,
	sharedEnv map[string]string,
	clusterEnvs map[string]map[string]string,
	runtimeData map[string]any,
	releaseRepos []string,
) error {
	if len(rep.Services) == 0 {
		fmt.Fprintln(w, "Nothing to do.")
		return nil
	}

	targets, err := buildApplyTargets(ctx, rep, manifest, sshPool, manifestDir, sharedEnv, clusterEnvs, runtimeData, releaseRepos)
	if err != nil {
		return err
	}
	actionFn := provisionerActionFunc(targets)
	probeFn := sshProbeFunc(sshPool)

	overallHalted := false
	for _, svc := range rep.Services {
		fmt.Fprintf(w, "\nExecuting service: %s (waves=%d)\n", svc.Service, len(svc.Waves))

		plan, inputs, err := buildExecutorPlan(svc, manifest)
		if err != nil {
			return fmt.Errorf("build executor plan for %s: %w", svc.Service, err)
		}

		result, execErr := orchestrator.ExecuteRolloutPlan(ctx, plan, inputs, actionFn, probeFn,
			orchestrator.ExecuteOptions{})
		renderExecuteResult(w, result)
		if execErr != nil {
			fmt.Fprintf(w, "  HALT: %v\n", execErr)
			overallHalted = true
			break
		}
	}

	if overallHalted {
		return &ExitCodeError{
			Code:    2,
			Message: "rollout halted; re-run `cluster apply` after fixing the failing host",
		}
	}
	fmt.Fprintln(w, "\nAll rollout actions completed. Re-run `cluster diff` to verify remote state.")
	return nil
}

// buildExecutorPlan composes a single-service RolloutPlan + ExecutorInput
// map from the report's wave structure. Decides reload vs restart per
// host using the service reload capability: env-only diffs on reload-capable
// services get ActionReload; everything else gets ActionRestart.
func buildExecutorPlan(
	svc clusterApplyService,
	manifest *inventory.Manifest,
) (orchestrator.RolloutPlan, map[string]orchestrator.ExecutorInput, error) {
	plan := orchestrator.RolloutPlan{Service: svc.Service}
	inputs := map[string]orchestrator.ExecutorInput{}
	deploy := strDefault(svc.Deploy, svc.Service)
	supportsReload := servicedefs.SupportsSIGHUPReload(deploy)

	for _, wave := range svc.Waves {
		ow := orchestrator.Wave{}
		for _, h := range wave.Hosts {
			host, ok := manifest.GetHost(h.Host)
			if !ok {
				return plan, nil, fmt.Errorf("manifest has no host %q", h.Host)
			}
			task := &orchestrator.Task{Name: h.Host, Host: h.Host, Type: deploy, ServiceID: svc.Service}
			ow.Tasks = append(ow.Tasks, task)

			action := orchestrator.ActionRestart
			if supportsReload && diffsAreEnvOnly(h.Kinds) {
				action = orchestrator.ActionReload
			}
			inputs[svc.Service+"@"+h.Host] = orchestrator.ExecutorInput{
				Key:     svc.Service + "@" + h.Host,
				Service: deploy,
				Host:    host,
				Action:  action,
			}
		}
		plan.Waves = append(plan.Waves, ow)
	}
	return plan, inputs, nil
}

// diffsAreEnvOnly reports whether the kinds slice is non-empty and
// contains only DiffEnv. If anything else is present (binary, unit,
// cert) a reload won't pick up the change — restart is required.
func diffsAreEnvOnly(kinds []orchestrator.DiffKind) bool {
	if len(kinds) == 0 {
		return false
	}
	for _, k := range kinds {
		if k != orchestrator.DiffEnv {
			return false
		}
	}
	return true
}

type clusterApplyTarget struct {
	provisioner provisioner.Provisioner
	config      provisioner.ServiceConfig
}

func buildApplyTargets(
	_ context.Context,
	rep clusterApplyReport,
	manifest *inventory.Manifest,
	sshPool *fwssh.Pool,
	manifestDir string,
	sharedEnv map[string]string,
	clusterEnvs map[string]map[string]string,
	runtimeData map[string]any,
	releaseRepos []string,
) (map[string]clusterApplyTarget, error) {
	targets := map[string]clusterApplyTarget{}
	provCache := map[string]provisioner.Provisioner{}
	for _, svc := range rep.Services {
		deploy := strDefault(svc.Deploy, svc.Service)
		prov, ok := provCache[deploy]
		if !ok {
			var err error
			prov, err = provisioner.GetProvisioner(deploy, sshPool)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", deploy, err)
			}
			provCache[deploy] = prov
		}
		for _, wave := range svc.Waves {
			for _, h := range wave.Hosts {
				if _, ok := manifest.GetHost(h.Host); !ok {
					return nil, fmt.Errorf("manifest has no host %q", h.Host)
				}
				task := orchestrator.NewServiceTask(deploy, svc.Service, h.Host, h.Host, inferApplyPhase(svc, manifest))
				cfg, err := buildTaskConfig(task, manifest, runtimeData, false, manifestDir, sharedEnv, clusterEnvs, releaseRepos)
				if err != nil {
					return nil, fmt.Errorf("%s on %s: %w", svc.Service, h.Host, err)
				}
				targets[svc.Service+"@"+h.Host] = clusterApplyTarget{provisioner: prov, config: cfg}
			}
		}
	}
	return targets, nil
}

func inferApplyPhase(svc clusterApplyService, manifest *inventory.Manifest) orchestrator.Phase {
	if _, ok := manifest.Interfaces[svc.Service]; ok {
		return orchestrator.PhaseInterfaces
	}
	if _, ok := manifest.Observability[svc.Service]; ok {
		return orchestrator.PhaseInterfaces
	}
	return orchestrator.PhaseApplications
}

// provisionerActionFunc applies the same host-scoped role config used by
// cluster provision. The Action label records the expected service handler
// (reload for env-only reload-capable services, restart otherwise), but the
// role remains responsible for writing files and firing handlers.
func provisionerActionFunc(targets map[string]clusterApplyTarget) orchestrator.ActionFunc {
	return func(ctx context.Context, in orchestrator.ExecutorInput) error {
		key := in.Key
		if key == "" {
			key = in.Service + "@" + in.Host.Name
		}
		target, ok := targets[key]
		if !ok {
			return fmt.Errorf("no apply target registered for %s", key)
		}
		return target.provisioner.Provision(ctx, in.Host, target.config)
	}
}

// sshProbeFunc wraps the SSH pool as an orchestrator.ProbeFunc so the
// readiness gates can shell out to curl / systemctl over the same pool.
func sshProbeFunc(pool *fwssh.Pool) orchestrator.ProbeFunc {
	return func(ctx context.Context, host inventory.Host, cmd string) (orchestrator.ProbeResult, error) {
		cfg := sshConfigFor(host)
		result, err := pool.Run(ctx, cfg, cmd)
		if err != nil {
			return orchestrator.ProbeResult{}, err
		}
		if result == nil {
			return orchestrator.ProbeResult{}, fmt.Errorf("ssh returned nil result")
		}
		return orchestrator.ProbeResult{
			Stdout:   result.Stdout,
			Stderr:   result.Stderr,
			ExitCode: result.ExitCode,
		}, nil
	}
}

func sshConfigFor(host inventory.Host) *fwssh.ConnectionConfig {
	return &fwssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	}
}

// renderExecuteResult prints the per-wave per-host outcome of one
// service's execution to w. Operators see exactly which hosts got which
// action, whether the action and gate both succeeded, and how long each
// host took.
func renderExecuteResult(w io.Writer, result orchestrator.ExecuteResult) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  WAVE\tHOST\tACTION\tAPPLIED\tGATE\tDURATION\tERROR")
	for _, wave := range result.Waves {
		for _, h := range wave.Hosts {
			errStr := "-"
			if h.Error != nil {
				errStr = h.Error.Error()
			}
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%t\t%t\t%s\t%s\n",
				wave.Index, h.Host, h.Action, h.Applied, h.GatePass,
				h.Duration.Round(time.Millisecond), errStr)
		}
	}
	_ = tw.Flush()
}

// boolFlag reads a Cobra bool flag without paying for type assertions.
func boolFlag(cmd *cobra.Command, name string) bool {
	f := cmd.Flag(name)
	if f == nil {
		return false
	}
	return f.Value.String() == "true"
}

// buildClusterApplyReport composes the diff entries with per-service
// rolling-update strategy. Entries with unknown or infra kinds get
// routed to Skipped instead of into a wave; entries with no kinds (no-op
// hosts) are dropped silently — there's nothing to plan for them.
func buildClusterApplyReport(manifest *inventory.Manifest, entries []clusterDiffEntry) clusterApplyReport {
	rep := clusterApplyReport{
		Cluster: fmt.Sprintf("%s-%s", manifest.Type, manifest.Profile),
	}

	// Bucket entries by service. Skipped entries are pulled out first
	// so the wave-building loop doesn't have to think about them.
	bySvc := map[string][]clusterDiffEntry{}
	for _, e := range entries {
		if len(e.Kinds) == 0 {
			continue
		}
		if entryIsBlocking(e) {
			rep.Skipped = append(rep.Skipped, e)
			continue
		}
		bySvc[e.Service] = append(bySvc[e.Service], e)
	}

	svcNames := make([]string, 0, len(bySvc))
	for n := range bySvc {
		svcNames = append(svcNames, n)
	}
	sort.Strings(svcNames)

	for _, name := range svcNames {
		deploy := strDefault(bySvc[name][0].Deploy, name)
		strategy := applyUpdateStrategyOverride(
			orchestrator.DefaultStrategyFor(deploy),
			updateStrategyConfigFor(manifest, name),
		)
		inputs := make([]orchestrator.RolloutInput, 0, len(bySvc[name]))
		for _, e := range bySvc[name] {
			host, ok := manifest.GetHost(e.Host)
			region := ""
			if ok {
				region = host.Labels["region"]
			}
			inputs = append(inputs, orchestrator.RolloutInput{
				Task: &orchestrator.Task{
					Name:      e.Host,
					Type:      deploy,
					ServiceID: name,
					Host:      e.Host,
				},
				Region: region,
			})
		}
		plan := orchestrator.BuildWaves(name, inputs, strategy)
		svcEntry := clusterApplyService{
			Service:  name,
			Deploy:   deploy,
			Strategy: strategy,
		}
		for i, w := range plan.Waves {
			wave := clusterApplyWave{Index: i + 1}
			for _, task := range w.Tasks {
				kinds, details := lookupEntryByHost(bySvc[name], task.Host)
				wave.Hosts = append(wave.Hosts, clusterApplyHost{
					Host:    task.Host,
					Kinds:   kinds,
					Details: details,
				})
			}
			svcEntry.Waves = append(svcEntry.Waves, wave)
		}
		rep.Services = append(rep.Services, svcEntry)
	}

	return rep
}

func updateStrategyConfigFor(manifest *inventory.Manifest, name string) *inventory.UpdateStrategyConfig {
	if manifest == nil {
		return nil
	}
	if svc, ok := manifest.Services[name]; ok {
		return svc.UpdateStrategy
	}
	if svc, ok := manifest.Interfaces[name]; ok {
		return svc.UpdateStrategy
	}
	if svc, ok := manifest.Observability[name]; ok {
		return svc.UpdateStrategy
	}
	return nil
}

func applyUpdateStrategyOverride(base orchestrator.UpdateStrategy, override *inventory.UpdateStrategyConfig) orchestrator.UpdateStrategy {
	if override == nil {
		return base
	}
	if override.MaxUnavailable != nil {
		base.MaxUnavailable = *override.MaxUnavailable
	}
	if override.Canary != nil {
		base.Canary = *override.Canary
	}
	if override.RegionStagger != nil {
		base.RegionStagger = *override.RegionStagger
	}
	if override.PrimaryLast != nil {
		base.PrimaryLast = *override.PrimaryLast
	}
	return base
}

// entryIsBlocking reports whether a diff entry would force `cluster
// apply` to fall through to `cluster provision`. Unknown means the
// fingerprinter doesn't model this kind; infra means a structural
// change (DB schema, package, kernel sysctl) that the fast path
// shouldn't attempt.
func entryIsBlocking(e clusterDiffEntry) bool {
	for _, k := range e.Kinds {
		if k == orchestrator.DiffUnknown || k == orchestrator.DiffInfra {
			return true
		}
	}
	return false
}

// lookupEntryByHost returns the diff kinds + per-kind detail strings for
// a host in a per-service entry list. Used to weave the diff back into
// the wave printout so operators see what each wave host is actually
// changing.
func lookupEntryByHost(entries []clusterDiffEntry, host string) ([]orchestrator.DiffKind, []string) {
	for _, e := range entries {
		if e.Host != host {
			continue
		}
		details := make([]string, 0, len(e.Details))
		for _, k := range e.Kinds {
			if d, ok := e.Details[k]; ok && d != "" {
				details = append(details, string(k)+": "+d)
			}
		}
		return e.Kinds, details
	}
	return nil, nil
}

func renderClusterApplyText(w io.Writer, rep clusterApplyReport) {
	fmt.Fprintf(w, "Cluster: %s\n\n", rep.Cluster)

	if len(rep.Services) == 0 {
		fmt.Fprintln(w, "No services have changes to roll out.")
	}

	for _, svc := range rep.Services {
		label := svc.Service
		if svc.Deploy != "" && svc.Deploy != svc.Service {
			label = fmt.Sprintf("%s (deploy: %s)", svc.Service, svc.Deploy)
		}
		fmt.Fprintf(w, "Service: %s  (strategy: max_unavailable=%d canary=%d region_stagger=%t primary_last=%t)\n",
			label, svc.Strategy.MaxUnavailable, svc.Strategy.Canary,
			svc.Strategy.RegionStagger, svc.Strategy.PrimaryLast)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  WAVE\tHOST\tKINDS\tDETAIL")
		for _, wave := range svc.Waves {
			for _, h := range wave.Hosts {
				kinds := make([]string, 0, len(h.Kinds))
				for _, k := range h.Kinds {
					kinds = append(kinds, string(k))
				}
				detail := "-"
				if len(h.Details) > 0 {
					detail = h.Details[0]
				}
				fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\n", wave.Index, h.Host, strings.Join(kinds, ","), detail)
			}
		}
		_ = tw.Flush()
		fmt.Fprintln(w)
	}

	if len(rep.Skipped) > 0 {
		fmt.Fprintf(w, "Skipped (run `cluster provision` for these — unknown or infra diff):\n")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  HOST\tSERVICE\tKINDS\tREASON")
		for _, e := range rep.Skipped {
			kinds := make([]string, 0, len(e.Kinds))
			for _, k := range e.Kinds {
				kinds = append(kinds, string(k))
			}
			reason := "-"
			for _, k := range e.Kinds {
				if d, ok := e.Details[k]; ok && d != "" {
					reason = d
					break
				}
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", e.Host, e.Service, strings.Join(kinds, ","), reason)
		}
		_ = tw.Flush()
	}
}
