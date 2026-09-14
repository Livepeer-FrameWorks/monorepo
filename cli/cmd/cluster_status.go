package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// serviceStatus holds the collected version info for a single service entry.
type serviceStatus struct {
	Name            string                 `json:"name"`
	Deployed        string                 `json:"deployed"`
	Available       string                 `json:"available"`
	Status          string                 `json:"status"`
	Mode            string                 `json:"mode"`
	RunningReplicas int                    `json:"running_replicas"`
	TotalReplicas   int                    `json:"total_replicas"`
	Replicas        []serviceReplicaStatus `json:"replicas"`
}

type serviceReplicaStatus struct {
	Host           string `json:"host"`
	Deployed       string `json:"deployed"`
	Status         string `json:"status"`
	Mode           string `json:"mode"`
	ArtifactDigest string `json:"artifact_digest,omitempty"`
	Error          string `json:"error,omitempty"`
}

func newClusterStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show deployed vs available versions for all cluster services",
		Long: `Compare the currently deployed service versions against the latest
available versions from the GitOps manifest.

Fetches the release manifest for the cluster's configured channel (default:
stable) and detects each enabled service on every configured host. The table
shows whether each service is up to date, has an upgrade available, is not
running, is not installed, or has diverged across replicas.`,
		Example: `  frameworks cluster status
  frameworks cluster status --manifest /etc/frameworks/cluster.yaml
  frameworks cluster status --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runClusterStatus(cmd, rc, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON output")

	return cmd
}

func runClusterStatus(cmd *cobra.Command, rc *resolvedCluster, jsonOutput bool) error {
	manifest := rc.Manifest
	channel := manifest.ResolvedChannel()
	gitopsChannel, gitopsVersion := gitops.ResolveVersion(channel)

	gitopsManifest, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, gitopsChannel, gitopsVersion)
	if err != nil {
		return fmt.Errorf("failed to fetch gitops manifest (channel: %s): %w", channel, err)
	}

	if !jsonOutput {
		fmt.Fprintf(cmd.OutOrStdout(), "Cluster: %s-%s (channel: %s)\n", manifest.Type, manifest.Profile, channel)
		fmt.Fprintf(cmd.OutOrStdout(), "Platform version: %s\n\n", gitopsManifest.PlatformVersion)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := fwssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	var results []serviceStatus

	collectStatus := func(name string, svc inventory.ServiceConfig) {
		if !svc.Enabled {
			return
		}

		hostNames := serviceHosts(svc)
		if len(hostNames) == 0 {
			return
		}

		deployName, resolveErr := resolveDeployName(name, svc)
		if resolveErr != nil {
			return
		}

		available := ""
		availableDigest := ""
		if svcInfo, infoErr := gitopsManifest.GetServiceInfo(deployName); infoErr == nil {
			available = svcInfo.Version
			availableDigest = svcInfo.Digest
		}

		replicas := make([]serviceReplicaStatus, len(hostNames))
		var wg sync.WaitGroup
		for i, hostName := range hostNames {
			i, hostName := i, hostName
			wg.Add(1)
			go func() {
				defer wg.Done()
				replica := serviceReplicaStatus{Host: hostName, Mode: svc.Mode, Status: "not installed"}
				host, ok := manifest.GetHost(hostName)
				if !ok {
					replica.Status = "configuration error"
					replica.Error = "host is not declared in the manifest"
					replicas[i] = replica
					return
				}

				state, detectErr := detect.NewDetector(sshPool, host).Detect(ctx, deployName)
				if detectErr != nil {
					replica.Status = "inspection failed"
					replica.Error = detectErr.Error()
					replicas[i] = replica
					return
				}
				if state == nil || !state.Exists {
					replicas[i] = replica
					return
				}

				replica.Deployed = state.Version
				replica.Mode = state.Mode
				replica.ArtifactDigest = dockerImageDigest(state.Metadata["image"])
				switch {
				case !state.Running:
					replica.Status = "not running"
				case available != "" && state.Version != available:
					replica.Status = "upgrade available"
				case state.Mode == "docker" && availableDigest != "" && replica.ArtifactDigest != availableDigest:
					replica.Status = "artifact drift"
					replica.Error = fmt.Sprintf("running digest %s, want %s", displayDigest(replica.ArtifactDigest), availableDigest)
				default:
					replica.Status = "up to date"
				}
				replicas[i] = replica
			}()
		}
		wg.Wait()

		results = append(results, summarizeServiceReplicas(name, svc.Mode, available, replicas))
	}

	// Collect from each service group in deterministic order.
	serviceNames := make([]string, 0, len(manifest.Services))
	for n := range manifest.Services {
		serviceNames = append(serviceNames, n)
	}
	sort.Strings(serviceNames)
	for _, n := range serviceNames {
		collectStatus(n, manifest.Services[n])
	}

	ifaceNames := make([]string, 0, len(manifest.Interfaces))
	for n := range manifest.Interfaces {
		ifaceNames = append(ifaceNames, n)
	}
	sort.Strings(ifaceNames)
	for _, n := range ifaceNames {
		collectStatus(n, manifest.Interfaces[n])
	}

	obsNames := make([]string, 0, len(manifest.Observability))
	for n := range manifest.Observability {
		obsNames = append(obsNames, n)
	}
	sort.Strings(obsNames)
	for _, n := range obsNames {
		collectStatus(n, manifest.Observability[n])
	}

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tRUNNING\tDEPLOYED\tAVAILABLE\tSTATUS")
	for _, r := range results {
		deployed := r.Deployed
		if deployed == "" {
			deployed = "unknown"
		}
		available := r.Available
		if available == "" {
			available = "-"
		}
		fmt.Fprintf(w, "%s\t%d/%d\t%s\t%s\t%s\n", r.Name, r.RunningReplicas, r.TotalReplicas, deployed, available, r.Status)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	printReplicaDiscrepancies(cmd, results)

	printStatusControlPlaneSection(cmd, manifest)
	return nil
}

func summarizeServiceReplicas(name, configuredMode, available string, replicas []serviceReplicaStatus) serviceStatus {
	result := serviceStatus{
		Name:          name,
		Available:     available,
		Mode:          configuredMode,
		TotalReplicas: len(replicas),
		Replicas:      replicas,
	}
	versions := map[string]struct{}{}
	modes := map[string]struct{}{}
	statusCounts := map[string]int{}
	for _, replica := range replicas {
		statusCounts[replica.Status]++
		if replica.Status == "up to date" || replica.Status == "upgrade available" || replica.Status == "artifact drift" {
			result.RunningReplicas++
		}
		if replica.Deployed != "" {
			versions[replica.Deployed] = struct{}{}
		}
		if replica.Mode != "" {
			modes[replica.Mode] = struct{}{}
		}
	}

	versionList := sortedStatusKeys(versions)
	switch len(versionList) {
	case 0:
		result.Deployed = ""
	case 1:
		result.Deployed = versionList[0]
	default:
		result.Deployed = "mixed (" + strings.Join(versionList, ", ") + ")"
	}
	modeList := sortedStatusKeys(modes)
	if len(modeList) == 1 {
		result.Mode = modeList[0]
	} else if len(modeList) > 1 {
		result.Mode = "mixed"
	}

	switch {
	case len(replicas) == 0:
		result.Status = "not configured"
	case statusCounts["configuration error"] > 0:
		result.Status = fmt.Sprintf("configuration error (%d/%d)", statusCounts["configuration error"], len(replicas))
	case statusCounts["inspection failed"] > 0:
		result.Status = fmt.Sprintf("inspection failed (%d/%d)", statusCounts["inspection failed"], len(replicas))
	case statusCounts["not installed"] == len(replicas):
		result.Status = "not installed"
	case statusCounts["not installed"] > 0:
		result.Status = fmt.Sprintf("partially installed (%d/%d)", len(replicas)-statusCounts["not installed"], len(replicas))
	case statusCounts["not running"] == len(replicas):
		result.Status = "not running"
	case statusCounts["not running"] > 0:
		result.Status = fmt.Sprintf("partially running (%d/%d)", result.RunningReplicas, len(replicas))
	case len(versionList) > 1:
		result.Status = "mixed versions"
	case statusCounts["artifact drift"] > 0:
		result.Status = fmt.Sprintf("artifact drift (%d/%d)", statusCounts["artifact drift"], len(replicas))
	case statusCounts["upgrade available"] > 0:
		result.Status = "upgrade available"
	default:
		result.Status = "up to date"
	}
	return result
}

func displayDigest(digest string) string {
	if digest == "" {
		return "unknown"
	}
	return digest
}

func sortedStatusKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}

func printReplicaDiscrepancies(cmd *cobra.Command, results []serviceStatus) {
	printedHeader := false
	for _, result := range results {
		if result.Status == "up to date" || result.TotalReplicas <= 1 {
			continue
		}
		for _, replica := range result.Replicas {
			if replica.Status == "up to date" {
				continue
			}
			if !printedHeader {
				fmt.Fprintln(cmd.OutOrStdout(), "\nReplica discrepancies:")
				printedHeader = true
			}
			deployed := replica.Deployed
			if deployed == "" {
				deployed = "unknown"
			}
			detail := replica.Status
			if replica.Error != "" {
				detail += ": " + replica.Error
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %s@%s: %s (%s)\n", result.Name, replica.Host, deployed, detail)
		}
	}
}

// printStatusControlPlaneSection runs ControlPlaneReadiness without SOPS and
// renders the report. Without a service token the readiness checks return
// Checked=false unless endpoint resolution itself fails — in that case
// buildControlPlaneReport surfaces the failure as a warning and forces
// Checked=true so the policy gate cannot read silent failure as success.
func printStatusControlPlaneSection(cmd *cobra.Command, manifest *inventory.Manifest) {
	out := cmd.OutOrStdout()
	cfg, err := fwcfg.Load()
	if err != nil {
		return
	}
	active, mErr := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
	if mErr != nil || active.SystemTenantID == "" {
		return
	}

	qmAddr, _ := resolveServiceGRPCAddr(manifest, "quartermaster", 19002) //nolint:errcheck // empty on miss is the intent

	// Route through buildControlPlaneReport so endpoint-resolution failures
	// surface as warnings instead of degrading silently to Checked=false.
	// Status has no service token, so ControlPlaneReadiness returns
	// Checked=false unless resolution itself fails — and that case still
	// produces a warning via endpointResolutionWarnings.
	report := buildControlPlaneReport(cmd.Context(), manifest, map[string]any{
		"system_tenant_id": active.SystemTenantID,
	}, nil)

	fmt.Fprintln(out, "")
	ux.Subheading(out, "Control Plane:")
	fmt.Fprintf(out, "  system tenant:  %s\n", active.SystemTenantID)
	if qmAddr != "" {
		fmt.Fprintf(out, "  quartermaster:  %s\n", qmAddr)
	}
	renderReadinessBlock(cmd, report, statusControlPlaneFallbacks())
}

// statusControlPlaneFallbacks returns the next-steps for an unverified
// control plane. The order is load-bearing — `cluster doctor --deep`
// must lead (see cluster_status_fallback_test.go).
func statusControlPlaneFallbacks() []ux.NextStep {
	return []ux.NextStep{
		{Cmd: "frameworks cluster doctor --deep", Why: "Run the authenticated doctor — decrypts SOPS and verifies default/official cluster, operator account, pricing."},
		{Cmd: "frameworks cluster provision", Why: "Re-run provisioning with SOPS access if the control plane needs reconciliation."},
		{Cmd: "frameworks admin clusters list", Why: "Any admin command that touches Quartermaster reads the service token and will succeed or fail explicitly."},
	}
}
