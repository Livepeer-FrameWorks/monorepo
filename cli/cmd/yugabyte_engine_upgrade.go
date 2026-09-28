package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
)

// An ordered Yugabyte engine upgrade installs the new engine on each node while restarting only its master, then
// restarts every master still on the previous engine, then every tserver, then finalizes (promote AutoFlags, upgrade
// the YSQL catalog). `cluster upgrade yugabyte` and the Yugabyte stage of `release apply` both run it through the
// roll, so every change passes the same gates.
//
// Finalizing leaves no state a probe can compare against the installed engine, so the upgrade writes a marker on
// every node before the first change and removes it only after finalizing succeeds. A run that stops anywhere in
// between leaves the marker, and the next run finishes the upgrade.

// yugabyteFinalizePendingMarker records, on each node, that an engine upgrade began and has not been finalized.
const yugabyteFinalizePendingMarker = "/var/lib/yugabyte/data/.frameworks-engine-finalize-pending"

// yugabyteEngineStateScript reports whether a node joined the universe, the engine version its selected release
// carries, whether an engine upgrade awaits finalizing, and which processes still run a binary other than the
// installed one. Only the running executable's inode is compared, so a tserver whose configuration alone differs
// does not count.
const yugabyteEngineStateScript = provisioner.YugabyteBinaryResolverShell + `
set -u
joined=no
if [ -f /var/lib/yugabyte/data/.frameworks-bootstrap-complete ]; then joined=yes; fi
pending=no
if [ -f ` + yugabyteFinalizePendingMarker + ` ]; then pending=yes; fi
installed=none
stale=
if master="$(fw_yb_bin yb-master 2>/dev/null)"; then
  installed="$("$master" --version 2>/dev/null | sed -n 's/^version \([^ ][^ ]*\).*/\1/p' | head -n 1)"
  if [ -z "$installed" ]; then echo "cannot read the engine version of $master" >&2; exit 1; fi
  for process in yb-master yb-tserver; do
    pid="$(systemctl show -p MainPID --value "$process" 2>/dev/null || true)"
    if [ -z "$pid" ] || [ "$pid" = 0 ]; then continue; fi
    running="$(stat -L -c %i "/proc/$pid/exe" 2>/dev/null)" || { echo "cannot read the running binary of $process" >&2; exit 1; }
    binary="$(fw_yb_bin "$process")" || exit 1
    selected="$(stat -L -c %i "$binary")" || exit 1
    if [ "$running" != "$selected" ]; then stale="$stale,$process"; fi
  done
fi
echo "joined=$joined installed=$installed finalize_pending=$pending stale=${stale#,}"
`

// yugabyteFinalizePendingScript writes (with a version argument) or removes the finalize marker.
const yugabyteFinalizePendingScript = `set -eu
marker=` + yugabyteFinalizePendingMarker + `
version=%s
if [ -n "$version" ]; then
  printf '%%s\n' "$version" > "$marker"
else
  rm -f "$marker"
fi
`

// yugabyteEngineState is one node's engine as the engine state probe reports it.
type yugabyteEngineState struct {
	// Joined reports whether the node finished bootstrapping into the universe.
	Joined bool
	// Installed is the engine version of the node's selected release; empty when no engine is installed.
	Installed string
	// FinalizePending reports the marker of an engine upgrade that has not been finalized.
	FinalizePending bool
	// Stale lists the processes still running a binary other than the installed one.
	Stale []string
}

func parseYugabyteEngineState(host, out string) (yugabyteEngineState, error) {
	fields := map[string]string{}
	for _, field := range strings.Fields(out) {
		if key, value, ok := strings.Cut(field, "="); ok {
			fields[key] = value
		}
	}
	joined, installed, pending, stale := fields["joined"], fields["installed"], fields["finalize_pending"], fields["stale"]
	if (joined != "yes" && joined != "no") || installed == "" || (pending != "yes" && pending != "no") {
		return yugabyteEngineState{}, fmt.Errorf("unexpected engine state report from %s: %q", host, strings.TrimSpace(out))
	}
	state := yugabyteEngineState{Joined: joined == "yes", FinalizePending: pending == "yes"}
	if installed != "none" {
		state.Installed = installed
	}
	for process := range strings.SplitSeq(stale, ",") {
		if process != "" {
			state.Stale = append(state.Stale, process)
		}
	}
	return state, nil
}

func (u *sshYugabyteUniverse) EngineState(ctx context.Context, host inventory.Host) (yugabyteEngineState, error) {
	out, err := yugabyteNodeScript(ctx, u.pool, host, yugabyteEngineStateScript)
	if err != nil {
		return yugabyteEngineState{}, fmt.Errorf("read the Yugabyte engine on %s: %w", host.Name, err)
	}
	return parseYugabyteEngineState(host.Name, out)
}

func (u *sshYugabyteUniverse) SetFinalizePending(ctx context.Context, host inventory.Host, version string) error {
	_, err := yugabyteNodeScript(ctx, u.pool, host, fmt.Sprintf(yugabyteFinalizePendingScript, fwssh.ShellQuote(version)))
	return err
}

// compareYugabyteVersions orders two dotted engine versions such as 2025.2.3.0 and 2026.1.1.2.
func compareYugabyteVersions(a, b string) (int, error) {
	parse := func(v string) ([]int, error) {
		parts := strings.Split(v, ".")
		numbers := make([]int, 0, len(parts))
		for _, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("engine version %q is not a dotted number", v)
			}
			numbers = append(numbers, n)
		}
		return numbers, nil
	}
	left, err := parse(a)
	if err != nil {
		return 0, err
	}
	right, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := range max(len(left), len(right)) {
		var l, r int
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		if l != r {
			if l < r {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

// yugabyteEnginePlan is what the engine step of a release must do.
type yugabyteEnginePlan struct {
	Target string
	// From lists the distinct engines joined nodes run other than Target.
	From []string
	// Install names, in the order given, the joined nodes whose selected engine is not Target.
	Install []string
	// Resume reports an upgrade whose engine is installed everywhere but that still has a process on the previous
	// binary or is not finalized.
	Resume bool
}

func (p yugabyteEnginePlan) needed() bool { return len(p.Install) > 0 || p.Resume }

func (p yugabyteEnginePlan) describe() string {
	from := "a partly upgraded universe"
	if len(p.From) > 0 {
		from = strings.Join(p.From, ", ")
	}
	return fmt.Sprintf("engine %s -> %s (masters, tservers, finalize)", from, p.Target)
}

// planYugabyteEngine decides the engine step from each node's state, in names order. Nodes that never joined the
// universe take the engine through ordinary convergence. A joined node on a newer engine than target refuses the
// step: a finalized YugabyteDB universe cannot return to an older engine.
func planYugabyteEngine(target string, names []string, states map[string]yugabyteEngineState) (yugabyteEnginePlan, error) {
	plan := yugabyteEnginePlan{Target: target}
	if target == "" {
		return plan, errors.New("the release pins no Yugabyte engine")
	}
	for _, name := range names {
		state := states[name]
		if state.FinalizePending || len(state.Stale) > 0 {
			plan.Resume = true
		}
		if !state.Joined || state.Installed == "" || state.Installed == target {
			continue
		}
		order, err := compareYugabyteVersions(state.Installed, target)
		if err != nil {
			return plan, fmt.Errorf("%s: %w", name, err)
		}
		if order > 0 {
			return plan, fmt.Errorf("refusing to downgrade the Yugabyte engine on %s from %s to the release's %s: a YugabyteDB universe does not return to an older engine; apply a release that pins %s or newer", name, state.Installed, target, state.Installed)
		}
		plan.Install = append(plan.Install, name)
		if !slices.Contains(plan.From, state.Installed) {
			plan.From = append(plan.From, state.Installed)
		}
	}
	return plan, nil
}

// readYugabyteEngines reads the engine state of every named node, in order.
func (y *yugabyteRoll) readYugabyteEngines(ctx context.Context, hosts map[string]inventory.Host, names []string) (map[string]yugabyteEngineState, error) {
	states := make(map[string]yugabyteEngineState, len(names))
	for _, name := range names {
		state, err := y.u.EngineState(ctx, hosts[name])
		if err != nil {
			return nil, err
		}
		states[name] = state
	}
	return states, nil
}

// beginEngineUpgrade marks every node before the first engine change, so an interrupted upgrade is finished by the
// next run even once every node runs the new engine.
func (y *yugabyteRoll) beginEngineUpgrade(ctx context.Context, version string) error {
	for _, host := range y.hosts {
		if err := y.u.SetFinalizePending(ctx, host, version); err != nil {
			return fmt.Errorf("mark the Yugabyte engine upgrade on %s: %w", host.Name, err)
		}
	}
	return nil
}

// yugabyteEngineNodeOps are the node operations of an engine upgrade after its install pass, rendered by the caller.
type yugabyteEngineNodeOps struct {
	// Restart restarts only scope (master or tserver) on host through the role's restart tag.
	Restart func(ctx context.Context, host inventory.Host, scope string) error
	// Complete initializes host's databases and validates the node once both of its processes run the new engine.
	Complete func(ctx context.Context, host inventory.Host) error
}

// completeEngineUpgrade finishes an engine upgrade whose install pass is done: every master still on the previous
// engine restarts before any tserver, then every node is completed, then the upgrade is finalized and the marker
// removed. Each phase reads process state afresh, so a rerun resumes where the universe is.
func (y *yugabyteRoll) completeEngineUpgrade(ctx context.Context, version string, ops yugabyteEngineNodeOps) error {
	restartOnly := func(scope string) func(context.Context, inventory.Host) error {
		return func(ctx context.Context, host inventory.Host) error { return ops.Restart(ctx, host, scope) }
	}
	fmt.Fprintln(y.out, "  Restarting Yugabyte masters still on the previous engine, then every tserver...")
	if err := y.rollStale(ctx, "yb-master", restartOnly("master")); err != nil {
		return fmt.Errorf("restart Yugabyte masters on %s: %w", version, err)
	}
	if err := y.rollStale(ctx, "yb-tserver", restartOnly("tserver")); err != nil {
		return fmt.Errorf("every Yugabyte master runs %s but not every tserver; rerun to continue: %w", version, err)
	}
	for _, host := range y.hosts {
		if err := ops.Complete(ctx, host); err != nil {
			return err
		}
	}
	if err := y.finalizeUpgrade(ctx); err != nil {
		return fmt.Errorf("every Yugabyte node runs %s but the upgrade is not finalized; rerun to finalize: %w", version, err)
	}
	for _, host := range y.hosts {
		if err := y.u.SetFinalizePending(ctx, host, ""); err != nil {
			return fmt.Errorf("the Yugabyte upgrade to %s is finalized but its marker on %s remains, so the next run finalizes again: %w", version, host.Name, err)
		}
	}
	return nil
}
