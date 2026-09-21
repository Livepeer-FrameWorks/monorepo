package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"

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

// envContractFailures collects schema-contract failures across every rendered
// deployment so one run reports all of them.
type envContractFailures []string

func (f *envContractFailures) check(manifest *inventory.Manifest, target, serviceID string, env map[string]string) {
	if err := validateServiceEnvContract(manifest, serviceID, env); err != nil {
		*f = append(*f, fmt.Sprintf("%s: %v", target, err))
	}
}

func (f envContractFailures) err() error {
	if len(f) == 0 {
		return nil
	}
	sorted := append([]string(nil), f...)
	sort.Strings(sorted)
	return fmt.Errorf("service env contract fails (nothing was changed):\n  - %s", strings.Join(sorted, "\n  - "))
}

// requiredEnvConfigRenderer renders the final env a planned task would deploy
// with, after the role-level additions (provisioner.FinalServiceEnv).
type requiredEnvConfigRenderer func(task *orchestrator.Task) (map[string]string, error)

// collectPlanRequiredEnvGaps renders every planned task that is not
// infrastructure or declares operator inputs. A schema-contract failure on
// any of them is returned as the error, because nothing may be deployed
// without those keys. The tasks missing an external operator input are
// returned as gaps. It reads no host state, so it runs before the first
// mutation and under --dry-run.
func collectPlanRequiredEnvGaps(manifest *inventory.Manifest, plan *orchestrator.ExecutionPlan, render requiredEnvConfigRenderer) ([]requiredEnvGap, error) {
	if plan == nil {
		return nil, nil
	}
	var gaps []requiredEnvGap
	var failures envContractFailures
	for _, task := range plan.AllTasks {
		if task == nil {
			continue
		}
		contract := task.Phase != orchestrator.PhaseInfrastructure
		if !contract && len(servicedefs.RequiredExternalEnv(task.Type)) == 0 {
			continue
		}
		env, err := render(task)
		if err != nil {
			return nil, fmt.Errorf("render %s config for required-env preflight: %w", task.Name, err)
		}
		target := fmt.Sprintf("%s on %s", task.Name, task.Host)
		if contract {
			failures.check(manifest, target, task.Type, env)
		}
		if missing := missingRequiredExternalEnv(task.Type, env); len(missing) > 0 {
			gaps = append(gaps, requiredEnvGap{Target: target, Missing: missing})
		}
	}
	if err := failures.err(); err != nil {
		return nil, err
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Target < gaps[j].Target })
	return gaps, nil
}

// preflightProvisionRequiredEnv fails a provision before any host changes when
// a planned task fails the schema contract or lacks a declared operator input.
// The runtime values bootstrap resolves later (system tenant, owner tenants,
// Quartermaster address) feed no schema-required key, so the service token is
// the only runtime value it renders with. With --ignore-validation missing
// operator inputs are reported and the affected services deploy without
// starting, as provisionTask does per task; contract failures still refuse.
func preflightProvisionRequiredEnv(out io.Writer, plan *orchestrator.ExecutionPlan, manifest *inventory.Manifest, manifestDir string, sharedEnv map[string]string, clusterEnvs map[string]map[string]string, releaseRepos []string, ignoreValidation bool) error {
	runtimeData := map[string]any{}
	if token := strings.TrimSpace(sharedEnv["SERVICE_TOKEN"]); token != "" {
		runtimeData["service_token"] = token
	}
	gaps, err := collectPlanRequiredEnvGaps(manifest, plan, func(task *orchestrator.Task) (map[string]string, error) {
		config, buildErr := buildTaskConfig(task, manifest, runtimeData, false, manifestDir, sharedEnv, clusterEnvs, releaseRepos)
		if buildErr != nil {
			return nil, buildErr
		}
		host, _ := manifest.GetHost(task.Host)
		return provisioner.FinalServiceEnv(task.Type, host, config), nil
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
// or upgrade with the same runtime data and config the upgrade deploys, and
// fails before the first mutation when one fails the schema contract or lacks
// a declared operator input.
func preflightReleaseRequiredEnv(ctx context.Context, rc *resolvedCluster, services []string) error {
	var runtimeData map[string]any
	gaps, err := collectReleaseRequiredEnvGaps(rc.Manifest, services, func(serviceName, deployName string, host inventory.Host) (map[string]string, error) {
		if runtimeData == nil {
			systemTenantID, tenantErr := rc.ResolveSystemTenantID(ctx)
			if tenantErr != nil {
				return nil, fmt.Errorf("resolve system tenant: %w", tenantErr)
			}
			sharedEnv, envErr := rc.PreparedSharedEnv()
			if envErr != nil {
				return nil, fmt.Errorf("load manifest env_files: %w", envErr)
			}
			data, dataErr := prepareUpgradeRuntimeData(rc.Manifest, filepath.Dir(rc.ManifestPath), sharedEnv, systemTenantID)
			if dataErr != nil {
				return nil, dataErr
			}
			runtimeData = data
		}
		config, task, buildErr := buildUpgradeTaskConfig(rc, rc.Manifest, host, serviceName, deployName, runtimeData)
		if buildErr != nil {
			return nil, buildErr
		}
		return provisioner.FinalServiceEnv(task.Type, host, config), nil
	})
	if err != nil {
		return err
	}
	return requiredEnvPreflightError(gaps)
}

// releaseReplicaEnvRenderer renders the final env one release replica deploys
// with.
type releaseReplicaEnvRenderer func(serviceName, deployName string, host inventory.Host) (map[string]string, error)

// collectReleaseRequiredEnvGaps renders every replica of each release service,
// returns an error listing every replica that fails the schema contract (the
// check each upgrade runs), and returns as gaps the replicas missing a
// declared operator input.
func collectReleaseRequiredEnvGaps(manifest *inventory.Manifest, services []string, render releaseReplicaEnvRenderer) ([]requiredEnvGap, error) {
	var gaps []requiredEnvGap
	var failures envContractFailures
	for _, serviceName := range services {
		deployName := releaseDeployName(manifest, serviceName)
		hosts, found := resolveUpgradeHosts(manifest, serviceName)
		if !found {
			continue
		}
		for _, host := range hosts {
			env, err := render(serviceName, deployName, host)
			if err != nil {
				return nil, fmt.Errorf("render %s on %s for required-env preflight: %w", serviceName, host.Name, err)
			}
			target := fmt.Sprintf("%s on %s", serviceName, host.Name)
			failures.check(manifest, target, deployName, env)
			if missing := missingRequiredExternalEnv(deployName, env); len(missing) > 0 {
				gaps = append(gaps, requiredEnvGap{Target: target, Missing: missing})
			}
		}
	}
	if err := failures.err(); err != nil {
		return nil, err
	}
	return gaps, nil
}
