package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// buildDryRunTaskCompare returns a per-task annotation function that routes
// each planned task through the role-based provisioner's CheckDiff method.
// That call invokes ansible-playbook with --check --diff, so the operator
// sees the full would-change output per service inline in the plan.
//
// The ServiceConfig passed to CheckDiff is produced by the same
// buildTaskConfig helper the real apply path uses, so vars builders that
// depend on Mode, Port, credentials in shared env, or manifest-derived
// metadata produce the identical output as a live run.
//
// The returned contract function reports every task whose rendered env
// fails the service env contract, so the dry-run fails where the real run
// would.
func buildDryRunTaskCompare(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, manifest *inventory.Manifest, manifestDir string, sharedEnv map[string]string) (func(*orchestrator.Task) string, func() error, func()) {
	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := fwssh.NewPool(30*time.Second, sshKey)

	// runtimeData carries tokens minted during a real apply (enrollment
	// tokens, cert issuance tokens). Dry-run never provisions anything, so
	// we pass an empty map; the --check --diff output reports unresolved
	// tokens as env drift, which is the truthful signal.
	runtimeData := map[string]interface{}{}

	// Cluster env files merge identically to a live run so --check --diff
	// shows the same env surface the apply would produce. A load failure
	// (missing file, bad SOPS) fails the live run before any service deploys,
	// so it fails the dry-run once and marks every task as failing.
	clusterEnvs, clusterEnvsErr := rc.ClusterEnvs()

	var contractFailures []error
	if clusterEnvsErr != nil {
		contractFailures = append(contractFailures, fmt.Errorf("load cluster env_files: %w", clusterEnvsErr))
	}
	annotate := func(task *orchestrator.Task) string {
		host, ok := manifest.GetHost(task.Host)
		if !ok {
			return " | inconclusive: host not in manifest"
		}
		if clusterEnvsErr != nil {
			return fmt.Sprintf(" | would fail: load cluster env_files: %v", clusterEnvsErr)
		}

		cfg, cfgErr := buildTaskConfig(task, manifest, runtimeData, false, manifestDir, sharedEnv, clusterEnvs, rc.ReleaseRepos)
		if cfgErr != nil {
			return fmt.Sprintf(" | inconclusive: build task config: %v", cfgErr)
		}
		if contractErr := validateTaskServiceEnvContract(manifest, task, cfg); contractErr != nil {
			contractFailures = append(contractFailures, fmt.Errorf("%s on %s: %w", task.Name, task.Host, contractErr))
			return fmt.Sprintf(" | would fail: %v", contractErr)
		}

		prov, provErr := provisioner.GetProvisioner(task.Type, sshPool)
		if provErr != nil {
			return fmt.Sprintf(" | inconclusive: no provisioner for %s: %v", task.Type, provErr)
		}
		checker, ok := prov.(provisioner.CheckDiffer)
		if !ok {
			return " | inconclusive: provisioner does not support --check --diff"
		}
		rc.applyReleaseMetadata(cfg.Metadata)

		subCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := checker.CheckDiff(subCtx, host, cfg); err != nil {
			return fmt.Sprintf(" | would change (ansible --check --diff: %v)", err)
		}
		return " | no-op (ansible --check: nothing to change)"
	}
	contractErr := func() error { return errors.Join(contractFailures...) }
	return annotate, contractErr, func() { _ = sshPool.Close() }
}
