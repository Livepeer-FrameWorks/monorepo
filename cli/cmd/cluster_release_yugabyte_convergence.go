package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
)

// Release Yugabyte convergence applies the yugabyte role to every node in two
// passes through the Yugabyte roll (yugabyte_rolling.go), which admits one
// node at a time, nodes that do not serve first.
//
// The master pass renders the role with restart scope master, so a changed
// flagfile or unit restarts only yb-master; the masters must confirm the node's
// master can go down first, and every master must be alive with one leader
// before the next node. The role renders tserver.conf in this pass without
// restarting the tserver.
//
// The tserver pass then restarts each tserver whose running binary or applied
// configuration receipt differs from what is installed, or whose role
// rendered for the tserver scope still reports a change (its unit). Each
// restart waits until the masters confirm every tablet survives losing the
// node, and the next starts only once the node serves YSQL again, its tserver
// is alive, and no tablet is under-replicated or leaderless.
//
// An engine change is refused by the role here; `cluster upgrade yugabyte`
// owns it.

func describeYugabyteConvergence(out io.Writer, tasks []*orchestrator.Task) {
	fmt.Fprintf(out, "     · Yugabyte, one node at a time: masters of %s (masters confirm each can go down; all alive with a leader after), then every tserver whose applied config differs (no under-replicated or leaderless tablet; tserver rejoined)\n",
		strings.Join(taskHosts(tasks), " -> "))
}

// yugabyteReleaseOps are the live Yugabyte operations of the stage.
type yugabyteReleaseOps interface {
	// Roll returns the roll that admits and gates node changes, or nil when
	// the manifest runs no Yugabyte universe.
	Roll(out io.Writer) *yugabyteRoll
	// TserverConfigDrift reports whether the role rendered for the tserver
	// restart scope would change node.
	TserverConfigDrift(ctx context.Context, node *orchestrator.Task) (bool, error)
	// RestartTserver restarts node's tserver through the role's restart tag,
	// which renders its unit and records the applied configuration.
	RestartTserver(ctx context.Context, node *orchestrator.Task) error
}

func runYugabyteConvergence(ctx context.Context, c *releaseHostConvergence, tasks []*orchestrator.Task, dryRun bool) error {
	out := c.cmd.OutOrStdout()
	fmt.Fprintf(out, "\nYugabyte (%d node(s))\n", len(tasks))
	if dryRun {
		for _, task := range tasks {
			fmt.Fprintf(out, "  node on %s\n", task.Host)
			if err := c.convergeTaskBefore(ctx, task, true, out, nil); err != nil {
				return fmt.Errorf("node on %s: %w", task.Host, err)
			}
		}
		fmt.Fprintln(out, "  [DRY-RUN] changed masters would restart one at a time, then every tserver whose applied config differs, each admitted by the masters")
		return nil
	}

	ops := c.yugabyteOps()
	roll := ops.Roll(out)
	if roll == nil {
		return errors.New("the manifest declares Yugabyte nodes but no Yugabyte universe")
	}
	byHost := map[string]*orchestrator.Task{}
	hosts := map[string]inventory.Host{}
	names := make([]string, 0, len(tasks))
	for _, task := range tasks {
		host, ok := c.manifest.GetHost(task.Host)
		if !ok {
			return fmt.Errorf("host %s not found in manifest", task.Host)
		}
		byHost[task.Host], hosts[task.Host] = task, host
		roll.serving[task.Host] = roll.u.ServesYSQL(ctx, host)
		names = append(names, task.Host)
	}

	roll.setOrder(names)
	for _, name := range append([]string(nil), roll.order...) {
		fmt.Fprintf(out, "  master on %s\n", name)
		masterTask := withReleaseRuntimeOverrides(byHost[name], map[string]any{"restart_scope": "master"})
		if err := roll.changeMaster(ctx, hosts[name], func(gate func() error) error {
			return c.convergeTaskBefore(ctx, masterTask, false, out, gate)
		}); err != nil {
			return fmt.Errorf("master on %s: %w", name, err)
		}
	}

	var pending []string
	for _, name := range names {
		state, err := roll.u.ProcessState(ctx, hosts[name], "yb-tserver")
		if err != nil {
			return fmt.Errorf("tserver on %s: %w", name, err)
		}
		drift, err := ops.TserverConfigDrift(ctx, byHost[name])
		if err != nil {
			return fmt.Errorf("tserver on %s: precheck: %w", name, err)
		}
		if state != yugabyteProcessCurrent || drift {
			pending = append(pending, name)
		}
	}
	if len(pending) == 0 {
		fmt.Fprintln(out, "  every tserver runs its applied configuration")
		return nil
	}
	roll.setOrder(pending)
	for _, name := range append([]string(nil), roll.order...) {
		fmt.Fprintf(out, "  tserver on %s\n", name)
		if err := roll.change(ctx, hosts[name], func(gate func() error) error {
			if err := gate(); err != nil {
				return err
			}
			return ops.RestartTserver(ctx, byHost[name])
		}); err != nil {
			return fmt.Errorf("tserver on %s: %w", name, err)
		}
	}
	return nil
}

func (c *releaseHostConvergence) yugabyteOps() yugabyteReleaseOps {
	if c.serviceOps != nil && c.serviceOps.yugabyte != nil {
		return c.serviceOps.yugabyte
	}
	return sshYugabyteReleaseOps{c: c}
}

type sshYugabyteReleaseOps struct {
	c *releaseHostConvergence
}

func (o sshYugabyteReleaseOps) Roll(out io.Writer) *yugabyteRoll {
	return newYugabyteGate(out, o.c.manifest, o.c.pool)
}

// renderTserver renders node's role for the tserver restart scope.
func (o sshYugabyteReleaseOps) renderTserver(node *orchestrator.Task) (provisioner.Provisioner, provisioner.ServiceConfig, inventory.Host, error) {
	host, ok := o.c.manifest.GetHost(node.Host)
	if !ok {
		return nil, provisioner.ServiceConfig{}, inventory.Host{}, fmt.Errorf("host %s not found in manifest", node.Host)
	}
	runtimeData := maps.Clone(o.c.runtimeData)
	if runtimeData == nil {
		runtimeData = map[string]any{}
	}
	runtimeData["restart_scope"] = "tserver"
	prov, config, err := renderProvisionTask(node, o.c.pool, o.c.manifest, false, runtimeData, o.c.manifestDir, o.c.sharedEnv, o.c.clusterEnvs, o.c.releaseRepos)
	return prov, config, host, err
}

func (o sshYugabyteReleaseOps) TserverConfigDrift(ctx context.Context, node *orchestrator.Task) (bool, error) {
	prov, config, host, err := o.renderTserver(node)
	if err != nil {
		return false, err
	}
	return provisionWouldChange(ctx, prov, host, config, nil)
}

func (o sshYugabyteReleaseOps) RestartTserver(ctx context.Context, node *orchestrator.Task) error {
	prov, config, host, err := o.renderTserver(node)
	if err != nil {
		return err
	}
	restarter, ok := prov.(provisioner.Restarter)
	if !ok {
		return fmt.Errorf("the %s provisioner cannot restart a single process", prov.GetName())
	}
	return runProvisionPhase(ctx, provisionApplyTimeout, "restart", func(phaseCtx context.Context) error {
		return restarter.Restart(phaseCtx, host, config)
	})
}
