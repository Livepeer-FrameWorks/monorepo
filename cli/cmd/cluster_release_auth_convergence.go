package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
)

// verifyAuthReleaseConvergence checks the running auth producers and UI after
// the rollout. Health alone cannot detect an older Chartroom bundle that reads
// a different verification-link or recovery API contract.
func verifyAuthReleaseConvergence(ctx context.Context, manifest *inventory.Manifest, release *gitops.Manifest, inspect func(context.Context, inventory.Host, string) (*detect.ServiceState, error)) error {
	configured := map[string]inventory.ServiceConfig{}
	for name, svc := range manifest.Services {
		configured[name] = svc
	}
	for name, svc := range manifest.Interfaces {
		configured[name] = svc
	}
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	slices.Sort(names)

	var failures []string
	for _, name := range names {
		if !configured[name].Enabled {
			continue
		}
		deploy := releaseDeployName(manifest, name)
		if deploy != "commodore" && deploy != "bridge" && deploy != "chartroom" {
			continue
		}
		target, err := release.GetServiceInfo(deploy)
		if err != nil {
			return fmt.Errorf("%s release artifact: %w", name, err)
		}
		hosts, found := resolveUpgradeHosts(manifest, name)
		if !found || len(hosts) == 0 {
			return fmt.Errorf("%s has no resolvable release hosts", name)
		}
		for _, host := range hosts {
			state, err := inspect(ctx, host, deploy)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s@%s: inspect: %v", name, host.Name, err))
				continue
			}
			if state == nil || !state.Exists || !state.Running {
				failures = append(failures, fmt.Sprintf("%s@%s: not running", name, host.Name))
				continue
			}
			if state.Version != target.Version {
				failures = append(failures, fmt.Sprintf("%s@%s: running %s, release requires %s", name, host.Name, state.Version, target.Version))
				continue
			}
			if state.Mode == "docker" {
				selectedImage, err := provisioner.SelectedReleaseImage(target, nil)
				if err != nil {
					return fmt.Errorf("%s selected release image: %w", name, err)
				}
				expected := dockerImageDigest(selectedImage)
				observed := dockerImageDigest(state.Metadata["image"])
				if observed != expected {
					failures = append(failures, fmt.Sprintf("%s@%s: running image digest %s, release requires %s", name, host.Name, displayDigest(observed), expected))
				}
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("auth services have not converged; rerun cluster release apply before treating this release as complete:\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
}
