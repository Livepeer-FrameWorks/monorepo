package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

// serviceStatus holds the collected version info for a single service entry.
type serviceStatus struct {
	Name      string `json:"name"`
	Deployed  string `json:"deployed"`
	Available string `json:"available"`
	Status    string `json:"status"`
	Mode      string `json:"mode"`
}

func newClusterStatusCmd() *cobra.Command {
	var manifestPath string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show deployed vs available versions for all cluster services",
		Long: `Compare the currently deployed service versions against the latest
available versions from the GitOps manifest.

Fetches the release manifest for the cluster's configured channel (default:
stable) and detects each enabled service on its configured host. The table
shows whether each service is up to date, has an upgrade available, is not
running, or is not installed.`,
		Example: `  # Check status against cluster.yaml
  frameworks cluster status

  # Use a different manifest file
  frameworks cluster status --manifest /etc/frameworks/cluster.yaml

  # Machine-readable JSON output
  frameworks cluster status --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterStatus(cmd, manifestPath, jsonOutput)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON output")

	return cmd
}

func runClusterStatus(cmd *cobra.Command, manifestPath string, jsonOutput bool) error {
	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	channel := manifest.ResolvedChannel()
	gitopsChannel, gitopsVersion := gitops.ResolveVersion(channel)

	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return fmt.Errorf("failed to create gitops fetcher: %w", err)
	}

	gitopsManifest, err := fetcher.Fetch(gitopsChannel, gitopsVersion)
	if err != nil {
		return fmt.Errorf("failed to fetch gitops manifest (channel: %s): %w", channel, err)
	}

	if !jsonOutput {
		fmt.Fprintf(cmd.OutOrStdout(), "Cluster: %s-%s (channel: %s)\n", manifest.Type, manifest.Profile, channel)
		fmt.Fprintf(cmd.OutOrStdout(), "Platform version: %s\n\n", gitopsManifest.PlatformVersion)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var results []serviceStatus

	collectStatus := func(name string, svc inventory.ServiceConfig) {
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

		host, ok := manifest.GetHost(hostName)
		if !ok {
			return
		}

		deployName, resolveErr := resolveDeployName(name, svc)
		if resolveErr != nil {
			return
		}

		detector := detect.NewDetector(host)
		state, detectErr := detector.Detect(ctx, deployName)

		deployed := ""
		statusStr := "not installed"
		mode := svc.Mode

		if detectErr == nil && state != nil {
			if state.Exists {
				deployed = state.Version
				mode = state.Mode
				if state.Running {
					statusStr = "up to date"
				} else {
					statusStr = "not running"
				}
			}
		}

		available := ""
		if svcInfo, infoErr := gitopsManifest.GetServiceInfo(deployName); infoErr == nil {
			available = svcInfo.Version
		}

		if statusStr == "up to date" && deployed != available && available != "" {
			statusStr = "upgrade available"
		}

		results = append(results, serviceStatus{
			Name:      name,
			Deployed:  deployed,
			Available: available,
			Status:    statusStr,
			Mode:      mode,
		})
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
	fmt.Fprintln(w, "SERVICE\tDEPLOYED\tAVAILABLE\tSTATUS")
	for _, r := range results {
		deployed := r.Deployed
		if deployed == "" {
			deployed = "unknown"
		}
		available := r.Available
		if available == "" {
			available = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, deployed, available, r.Status)
	}
	return w.Flush()
}
