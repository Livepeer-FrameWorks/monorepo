package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// requiredEnvGap is one planned deployment whose declared operator inputs are
// missing from its rendered env.
type requiredEnvGap struct {
	Target  string
	Missing []servicedefs.RequiredEnvVar
}

// requiredEnvPreflightError lists every gap with its setup guide so one run
// reports all missing inputs instead of the first failing host.
func requiredEnvPreflightError(gaps []requiredEnvGap) error {
	if len(gaps) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("missing required operator config (set it in the shared env files or a per-service env_file, then rerun):")
	for _, gap := range gaps {
		for _, req := range gap.Missing {
			fmt.Fprintf(&b, "\n  - %s: %s (%s)", gap.Target, req.Key, req.SetupGuide)
		}
	}
	return fmt.Errorf("%s", b.String())
}

// requiredEnvConfigRenderer renders the env a planned task would deploy with.
type requiredEnvConfigRenderer func(task *orchestrator.Task) (map[string]string, error)

// collectPlanRequiredEnvGaps renders every planned task whose service declares
// operator inputs and returns the tasks missing any of them. It reads no host
// state, so it runs before the first mutation and under --dry-run.
func collectPlanRequiredEnvGaps(plan *orchestrator.ExecutionPlan, render requiredEnvConfigRenderer) ([]requiredEnvGap, error) {
	if plan == nil {
		return nil, nil
	}
	var gaps []requiredEnvGap
	for _, task := range plan.AllTasks {
		if task == nil || len(servicedefs.RequiredExternalEnv(task.Type)) == 0 {
			continue
		}
		env, err := render(task)
		if err != nil {
			return nil, fmt.Errorf("render %s config for required-env preflight: %w", task.Name, err)
		}
		if missing := missingRequiredExternalEnv(task.Type, env); len(missing) > 0 {
			gaps = append(gaps, requiredEnvGap{Target: fmt.Sprintf("%s on %s", task.Name, task.Host), Missing: missing})
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Target < gaps[j].Target })
	return gaps, nil
}

// preflightProvisionRequiredEnv fails a provision before any host changes when
// a planned service lacks a declared operator input. With --ignore-validation
// the gaps are reported and the affected services deploy without starting, as
// provisionTask does per task.
func preflightProvisionRequiredEnv(out io.Writer, plan *orchestrator.ExecutionPlan, manifest *inventory.Manifest, manifestDir string, sharedEnv map[string]string, clusterEnvs map[string]map[string]string, releaseRepos []string, ignoreValidation bool) error {
	runtimeData := map[string]any{}
	if token := strings.TrimSpace(sharedEnv["SERVICE_TOKEN"]); token != "" {
		runtimeData["service_token"] = token
	}
	gaps, err := collectPlanRequiredEnvGaps(plan, func(task *orchestrator.Task) (map[string]string, error) {
		config, buildErr := buildTaskConfig(task, manifest, runtimeData, false, manifestDir, sharedEnv, clusterEnvs, releaseRepos)
		return config.EnvVars, buildErr
	})
	if err != nil {
		return err
	}
	gapErr := requiredEnvPreflightError(gaps)
	if gapErr == nil {
		return nil
	}
	if ignoreValidation {
		fmt.Fprintf(out, "Warning: %v\n  --ignore-validation is set; those services deploy without starting.\n\n", gapErr)
		return nil
	}
	return gapErr
}

// preflightReleaseRequiredEnv renders every replica `release apply` will install
// or upgrade with the same config the upgrade deploys and fails before the
// first mutation when a declared operator input is missing.
func preflightReleaseRequiredEnv(ctx context.Context, rc *resolvedCluster, services []string) error {
	var runtimeData map[string]any
	gaps, err := collectReleaseRequiredEnvGaps(rc.Manifest, services, func(serviceName, deployName string, host inventory.Host) (map[string]string, error) {
		if runtimeData == nil {
			systemTenantID, tenantErr := rc.ResolveSystemTenantID(ctx)
			if tenantErr != nil {
				return nil, fmt.Errorf("resolve system tenant: %w", tenantErr)
			}
			runtimeData = map[string]any{"system_tenant_id": systemTenantID}
		}
		config, _, buildErr := buildUpgradeTaskConfig(rc, rc.Manifest, host, serviceName, deployName, runtimeData)
		return config.EnvVars, buildErr
	})
	if err != nil {
		return err
	}
	return requiredEnvPreflightError(gaps)
}

// releaseReplicaEnvRenderer renders the env one release replica deploys with.
type releaseReplicaEnvRenderer func(serviceName, deployName string, host inventory.Host) (map[string]string, error)

// collectReleaseRequiredEnvGaps renders every replica of each release service
// whose deploy declares operator inputs and returns the replicas missing any.
// Services without declared inputs are never rendered.
func collectReleaseRequiredEnvGaps(manifest *inventory.Manifest, services []string, render releaseReplicaEnvRenderer) ([]requiredEnvGap, error) {
	var gaps []requiredEnvGap
	for _, serviceName := range services {
		deployName := releaseDeployName(manifest, serviceName)
		if len(servicedefs.RequiredExternalEnv(deployName)) == 0 {
			continue
		}
		hosts, found := resolveUpgradeHosts(manifest, serviceName)
		if !found {
			continue
		}
		for _, host := range hosts {
			env, err := render(serviceName, deployName, host)
			if err != nil {
				return nil, fmt.Errorf("render %s on %s for required-env preflight: %w", serviceName, host.Name, err)
			}
			if missing := missingRequiredExternalEnv(deployName, env); len(missing) > 0 {
				gaps = append(gaps, requiredEnvGap{Target: fmt.Sprintf("%s on %s", serviceName, host.Name), Missing: missing})
			}
		}
	}
	return gaps, nil
}
