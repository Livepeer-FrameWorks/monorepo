package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

// nodeHostRole is one host-level role every planned host converges before its
// services: the node baseline (operator tools, log retention, sshd, fail2ban),
// then OS tuning. Provision and release apply converge them through
// convergeNodeHost, so both render the same role vars.
type nodeHostRole struct {
	name   string
	prov   provisioner.Provisioner
	config provisioner.ServiceConfig
}

// nodeHostProvisioners returns the node baseline and node tuning provisioners;
// tests replace it to converge without hosts.
var nodeHostProvisioners = func(pool *ssh.Pool) (baseline, tuning provisioner.Provisioner, err error) {
	baseline, err = provisioner.NewNodeBaselineProvisioner(pool)
	if err != nil {
		return nil, nil, fmt.Errorf("node baseline: %w", err)
	}
	tuning, err = provisioner.NewNodeTuningProvisioner(pool)
	if err != nil {
		return nil, nil, fmt.Errorf("node tuning: %w", err)
	}
	return baseline, tuning, nil
}

func nodeHostRoles(pool *ssh.Pool, manifest *inventory.Manifest, host inventory.Host, force bool) ([]nodeHostRole, error) {
	baseline, tuning, err := nodeHostProvisioners(pool)
	if err != nil {
		return nil, err
	}
	return []nodeHostRole{
		{name: "node baseline", prov: baseline, config: provisioner.ServiceConfig{Mode: "native", Metadata: map[string]any{}, Force: force}},
		{name: "node tuning", prov: tuning, config: provisioner.ServiceConfig{Mode: "native", Metadata: map[string]any{"profile": nodeTuningProfileForHost(manifest, host)}, Force: force}},
	}, nil
}

// convergeNodeHost applies each node host role whose check-mode precheck
// reports a change (every role with force). With dryRun it reports to out what
// would change and applies nothing. The roles restart no platform service:
// only sshd reloads, and journald restarts, when their config changed.
func convergeNodeHost(ctx context.Context, out io.Writer, pool *ssh.Pool, manifest *inventory.Manifest, host inventory.Host, force, dryRun bool) error {
	roles, err := nodeHostRoles(pool, manifest, host, force)
	if err != nil {
		return err
	}
	for _, role := range roles {
		if !force {
			inspection, checkErr := provisionInspectChanges(ctx, role.prov, host, role.config, nil)
			if checkErr != nil {
				return fmt.Errorf("%s precheck: %w", role.name, checkErr)
			}
			if !inspection.Changed {
				ux.Success(out, fmt.Sprintf("    %s already converged", role.name))
				continue
			}
			if dryRun {
				fmt.Fprintf(out, "    [DRY-RUN] %s would converge\n", role.name)
				writeProvisionChangeTasks(out, "      ", inspection.Tasks)
				continue
			}
			fmt.Fprintf(out, "    converging %s\n", role.name)
			writeProvisionChangeTasks(out, "      ", inspection.Tasks)
		} else if dryRun {
			fmt.Fprintf(out, "    [DRY-RUN] %s would converge (forced)\n", role.name)
			continue
		}
		if err := runProvisionPhase(ctx, provisionApplyTimeout, role.name, func(phaseCtx context.Context) error {
			return role.prov.Provision(phaseCtx, host, role.config)
		}); err != nil {
			return fmt.Errorf("%s: %w", role.name, err)
		}
	}
	return nil
}

// verifyFreshSSHLogin opens a new SSH session to each host, proving sshd still
// accepts the operator's key after the baseline reloaded it.
func verifyFreshSSHLogin(ctx context.Context, pool *ssh.Pool, manifest *inventory.Manifest, hosts []string) error {
	var failed []string
	for _, name := range hosts {
		host, ok := manifest.GetHost(name)
		if !ok {
			return fmt.Errorf("host %s not found in manifest", name)
		}
		cfg := sshConfigFor(host)
		// A cached session from before the reload would prove nothing; the
		// login below reports any connection problem.
		_ = pool.CloseHost(cfg) //nolint:errcheck // superseded by the login result
		result, err := pool.Run(ctx, cfg, "true")
		switch {
		case err != nil:
			failed = append(failed, fmt.Sprintf("%s (%v)", name, err))
		case result.ExitCode != 0:
			failed = append(failed, fmt.Sprintf("%s (exit %d: %s)", name, result.ExitCode, strings.TrimSpace(result.Stderr)))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("new SSH logins failed after the node baseline converged: %s", strings.Join(failed, "; "))
	}
	return nil
}
