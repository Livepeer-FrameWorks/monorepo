package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"sort"
	"strings"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
)

// Release Yugabyte convergence first brings every node to the engine the
// release pins, then applies the yugabyte role to every node in two passes
// through the Yugabyte roll (yugabyte_rolling.go), which admits one node at a
// time, nodes that do not serve first.
//
// The engine step reads each node's installed engine. When a joined node runs
// another engine, or an earlier upgrade left a process on the previous binary
// or its finalize marker, the stage runs the ordered engine upgrade that
// `cluster upgrade yugabyte` runs (yugabyte_engine_upgrade.go): each such node
// installs the engine restarting only its master, then every master still on
// the previous engine restarts, then every tserver, then the upgrade is
// finalized. A joined node on a newer engine than the pin refuses the release.
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

func describeYugabyteConvergence(out io.Writer, tasks []*orchestrator.Task) {
	fmt.Fprintf(out, "     · Yugabyte, one node at a time: masters of %s (masters confirm each can go down; all alive with a leader after), then every tserver whose applied config differs (no under-replicated or leaderless tablet; tserver rejoined)\n",
		strings.Join(taskHosts(tasks), " -> "))
}

// describeYugabyteEngine prints the engine step of the plan. Each node's
// installed engine needs SSH, so the plan names only the pin.
func describeYugabyteEngine(out io.Writer, engine string) {
	if engine == "" {
		fmt.Fprintln(out, "       first, joined nodes not on the release's Yugabyte engine upgrade in order (every master, then every tserver, then finalize); the pin and each node's engine are checked at apply")
		return
	}
	fmt.Fprintf(out, "       first, joined nodes not on Yugabyte %s upgrade in order (every master, then every tserver, then finalize); each node's installed engine is checked at apply\n", engine)
}

// yugabyteReleaseOps are the live Yugabyte operations of the stage.
type yugabyteReleaseOps interface {
	// Roll returns the roll that admits and gates node changes, or nil when
	// the manifest runs no Yugabyte universe.
	Roll(out io.Writer) *yugabyteRoll
	// TserverConfigDrift reports whether the role rendered for the tserver
	// restart scope would change node.
	TserverConfigDrift(ctx context.Context, node *orchestrator.Task) (bool, error)
	// Restart restarts only scope (master or tserver) on node through the
	// role's restart tag, which renders its unit and, for a tserver, records
	// the applied configuration.
	Restart(ctx context.Context, node *orchestrator.Task, scope string) error
	// InstallEngine installs the release's engine on node restarting only its
	// master: the install pass of the ordered engine upgrade.
	InstallEngine(ctx context.Context, node *orchestrator.Task) error
	// CompleteEngine initializes node's databases and validates it once both
	// of its processes run the new engine.
	CompleteEngine(ctx context.Context, node *orchestrator.Task) error
}

func runYugabyteConvergence(ctx context.Context, c *releaseHostConvergence, tasks []*orchestrator.Task, dryRun bool) error {
	out := c.cmd.OutOrStdout()
	fmt.Fprintf(out, "\nYugabyte (%d node(s))\n", len(tasks))
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
		names = append(names, task.Host)
	}
	sort.Strings(names)

	// The role refuses an engine change on a joined node, so the engine is
	// settled before any node's configuration is checked.
	states, err := roll.readYugabyteEngines(ctx, hosts, names)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	engine, err := planYugabyteEngine(c.yugabyteEngine, names, states)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}

	if dryRun {
		if engine.needed() {
			fmt.Fprintf(out, "  [DRY-RUN] would upgrade %s", engine.describe())
			if len(engine.Install) > 0 {
				fmt.Fprintf(out, ", installing it on %s", strings.Join(engine.Install, " -> "))
			}
			fmt.Fprintln(out, "; each node's configuration is checked against the new engine once it runs")
			return nil
		}
		fmt.Fprintf(out, "  every joined node runs Yugabyte %s\n", engine.Target)
		for _, name := range names {
			fmt.Fprintf(out, "  node on %s\n", name)
			if err := c.convergeTaskBefore(ctx, byHost[name], true, out, nil); err != nil {
				return fmt.Errorf("node on %s: %w", name, err)
			}
		}
		fmt.Fprintln(out, "  [DRY-RUN] changed masters would restart one at a time, then every tserver whose applied config differs, each admitted by the masters")
		return nil
	}

	for _, name := range names {
		roll.serving[name] = roll.u.ServesYSQL(ctx, hosts[name])
	}
	if engine.needed() {
		if err := upgradeYugabyteEngine(ctx, out, roll, ops, engine, hosts, byHost); err != nil {
			return fmt.Errorf("engine: %w", err)
		}
	} else {
		fmt.Fprintf(out, "  every joined node runs Yugabyte %s\n", engine.Target)
	}

	roll.setOrder(append([]string(nil), names...))
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
			return ops.Restart(ctx, byHost[name], "tserver")
		}); err != nil {
			return fmt.Errorf("tserver on %s: %w", name, err)
		}
	}
	return nil
}

// upgradeYugabyteEngine runs the ordered engine upgrade: the finalize marker
// on every node, the install pass over the nodes not yet on the engine, then
// the phases shared with `cluster upgrade yugabyte`.
func upgradeYugabyteEngine(ctx context.Context, out io.Writer, roll *yugabyteRoll, ops yugabyteReleaseOps, engine yugabyteEnginePlan, hosts map[string]inventory.Host, byHost map[string]*orchestrator.Task) error {
	fmt.Fprintf(out, "  upgrading %s\n", engine.describe())
	if err := roll.beginEngineUpgrade(ctx, engine.Target); err != nil {
		return err
	}
	roll.setOrder(append([]string(nil), engine.Install...))
	for _, name := range append([]string(nil), roll.order...) {
		fmt.Fprintf(out, "  engine %s on %s (master restarts; the tserver keeps the previous engine)\n", engine.Target, name)
		if err := roll.changeMaster(ctx, hosts[name], func(gate func() error) error {
			if err := gate(); err != nil {
				return err
			}
			return ops.InstallEngine(ctx, byHost[name])
		}); err != nil {
			return fmt.Errorf("install on %s: %w", name, err)
		}
	}
	node := func(host inventory.Host) (*orchestrator.Task, error) {
		if task, ok := byHost[host.Name]; ok {
			return task, nil
		}
		return nil, fmt.Errorf("no planned Yugabyte node on %s", host.Name)
	}
	if err := roll.completeEngineUpgrade(ctx, engine.Target, yugabyteEngineNodeOps{
		Restart: func(ctx context.Context, host inventory.Host, scope string) error {
			task, err := node(host)
			if err != nil {
				return err
			}
			return ops.Restart(ctx, task, scope)
		},
		Complete: func(ctx context.Context, host inventory.Host) error {
			task, err := node(host)
			if err != nil {
				return err
			}
			return ops.CompleteEngine(ctx, task)
		},
	}); err != nil {
		return err
	}
	fmt.Fprintf(out, "  every Yugabyte node runs %s; upgrade finalized\n", engine.Target)
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

// render renders node's role with overrides added to the runtime data.
func (o sshYugabyteReleaseOps) render(node *orchestrator.Task, overrides map[string]any) (provisioner.Provisioner, provisioner.ServiceConfig, inventory.Host, error) {
	host, ok := o.c.manifest.GetHost(node.Host)
	if !ok {
		return nil, provisioner.ServiceConfig{}, inventory.Host{}, fmt.Errorf("host %s not found in manifest", node.Host)
	}
	runtimeData := maps.Clone(o.c.runtimeData)
	if runtimeData == nil {
		runtimeData = map[string]any{}
	}
	maps.Copy(runtimeData, overrides)
	prov, config, err := renderProvisionTask(node, o.c.pool, o.c.manifest, false, runtimeData, o.c.manifestDir, o.c.sharedEnv, o.c.clusterEnvs, o.c.releaseRepos)
	return prov, config, host, err
}

func (o sshYugabyteReleaseOps) TserverConfigDrift(ctx context.Context, node *orchestrator.Task) (bool, error) {
	prov, config, host, err := o.render(node, map[string]any{"restart_scope": "tserver"})
	if err != nil {
		return false, err
	}
	return provisionWouldChange(ctx, prov, host, config, nil)
}

func (o sshYugabyteReleaseOps) Restart(ctx context.Context, node *orchestrator.Task, scope string) error {
	prov, config, host, err := o.render(node, map[string]any{"restart_scope": scope})
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

// InstallEngine deploys without initializing or validating, which need YSQL
// from tservers the upgrade restarts only later.
func (o sshYugabyteReleaseOps) InstallEngine(ctx context.Context, node *orchestrator.Task) error {
	prov, config, host, err := o.render(node, map[string]any{"restart_scope": "master", "allow_engine_change": true})
	if err != nil {
		return err
	}
	return runProvisionPhase(ctx, provisionApplyTimeout, "deploy", func(phaseCtx context.Context) error {
		return prov.Deploy(phaseCtx, host, config)
	})
}

func (o sshYugabyteReleaseOps) CompleteEngine(ctx context.Context, node *orchestrator.Task) error {
	prov, config, host, err := o.render(node, nil)
	if err != nil {
		return err
	}
	return initializeAndValidateYugabyteNode(ctx, o.c.pool, o.c.manifest, host, prov, config)
}
