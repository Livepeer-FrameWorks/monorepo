package provisioner

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/inventory"
)

// edgeInterruptingReports are the check-mode handler results (roles
// mistserver and edge, handlers/main.yml) that mean applying the plan would
// restart MistServer or Caddy, or recreate the edge container, and so drop
// the node's live sessions. MistServer restarts only for the controller
// interface/port reconcile and a changed start-time environment (env file
// assignments, unit Environment lines, launchd EnvironmentVariables), which
// the USR1 re-exec does not pick up: a binary change goes through the rolling
// USR1 reload, and any other unit or wrapper change waits for the next start
// (edgePendingRestartReports).
var edgeInterruptingReports = []string{
	"Report mistserver restart in check mode",
	"Report edge caddy restart in check mode",
	"Report edge container recreate in check mode",
}

// edgePendingRestartReports are the check-mode handler results for unit or
// env changes the running process does not need: applying installs them and
// records a pending restart instead of restarting.
var edgePendingRestartReports = []string{
	"Report mistserver restart pending in check mode",
	"Report edge caddy restart pending in check mode",
}

// EdgeInterruptingReports returns the handler names whose check-mode change
// marks an edge plan as interrupting.
func EdgeInterruptingReports() []string {
	return slices.Clone(edgeInterruptingReports)
}

// EdgePendingRestartReports returns the handler names whose check-mode change
// marks an edge plan as leaving a restart pending.
func EdgePendingRestartReports() []string {
	return slices.Clone(edgePendingRestartReports)
}

// EdgeInspection is the check-mode verdict for one edge node.
type EdgeInspection struct {
	// Changed is true when applying the role would change anything.
	Changed bool
	// Interrupting is true when applying would restart MistServer or Caddy
	// or recreate the edge container.
	Interrupting bool
	// PendingRestart is true when applying installs a unit or env change
	// that takes effect at the service's next start.
	PendingRestart bool
	// ChangedTasks names the tasks and handlers that reported a change.
	ChangedTasks []string
}

// ClassifyEdgeChanges builds the verdict from the changed task names of a
// check-mode run.
func ClassifyEdgeChanges(changedTasks []string) EdgeInspection {
	inspection := EdgeInspection{Changed: len(changedTasks) > 0, ChangedTasks: slices.Clone(changedTasks)}
	for _, task := range changedTasks {
		if slices.Contains(edgeInterruptingReports, task) {
			inspection.Interrupting = true
		}
		if slices.Contains(edgePendingRestartReports, task) {
			inspection.PendingRestart = true
		}
	}
	return inspection
}

// Summary is a one-line operator description of the verdict.
func (i EdgeInspection) Summary() string {
	switch {
	case !i.Changed:
		return "in sync (no changes)"
	case i.Interrupting:
		return fmt.Sprintf("drift: %d change(s); applying restarts media services", len(i.ChangedTasks))
	case i.PendingRestart:
		return fmt.Sprintf("drift: %d change(s); applies without interrupting media (a unit/env change waits for the next service start)", len(i.ChangedTasks))
	default:
		return fmt.Sprintf("drift: %d change(s); applies without interrupting media", len(i.ChangedTasks))
	}
}

// edgeRestartPendingMarkers are the marker files the mistserver and edge
// roles write for a pending restart (mistserver_restart_pending_marker,
// edge_caddy_restart_pending_marker), with the service each belongs to. The
// Darwin user-domain MistServer marker lives under $HOME.
var edgeRestartPendingMarkers = []struct{ Path, Service string }{
	{"/opt/frameworks/mistserver/.restart-pending", "frameworks-mistserver"},
	{"/opt/frameworks/caddy/.restart-pending", "frameworks-caddy"},
	{"/usr/local/opt/frameworks/mistserver/.restart-pending", "MistServer (launchd)"},
	{"$HOME/.local/opt/frameworks/mistserver/.restart-pending", "MistServer (launchd)"},
}

// PendingRestarts names the services on host whose installed unit or env
// change their running process has not started with yet.
func (e *EdgeProvisioner) PendingRestarts(ctx context.Context, host inventory.Host) ([]string, error) {
	var script strings.Builder
	for i, m := range edgeRestartPendingMarkers {
		fmt.Fprintf(&script, "[ -f \"%s\" ] && echo %d; ", m.Path, i)
	}
	script.WriteString("true")
	result, err := e.RunCommand(ctx, host, script.String())
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("read pending restart markers: exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return parsePendingRestartMarkers(result.Stdout), nil
}

func parsePendingRestartMarkers(stdout string) []string {
	var services []string
	for line := range strings.SplitSeq(stdout, "\n") {
		i, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || i < 0 || i >= len(edgeRestartPendingMarkers) {
			continue
		}
		if service := edgeRestartPendingMarkers[i].Service; !slices.Contains(services, service) {
			services = append(services, service)
		}
	}
	return services
}

// edgeCheckTags are the role tags a check-mode plan covers: everything an
// apply renders or restarts, without the live validate probes.
var edgeCheckTags = []string{"install", "configure", "service"}

// inspectEdgeRole runs the edge role in check mode and classifies what it
// reports. w receives the playbook output (with --diff when diff is set);
// nil keeps the run silent.
func inspectEdgeRole(ctx context.Context, e *EdgeProvisioner, host inventory.Host, config *EdgeProvisionConfig, remoteOS, remoteArch string, diff bool, w io.Writer) (EdgeInspection, error) {
	recap := &ansiblerun.RecapOutputer{W: w}
	if err := runEdgeRoleWith(ctx, e.sshPool, host, config, remoteOS, remoteArch, edgeRoleRun{
		Tags:     edgeCheckTags,
		Check:    true,
		Diff:     diff,
		Outputer: recap,
	}); err != nil {
		return EdgeInspection{Changed: true, Interrupting: true}, err
	}
	if !recap.HasRecap() {
		return EdgeInspection{Changed: true, Interrupting: true}, fmt.Errorf("edge: ansible check emitted no PLAY RECAP")
	}
	return ClassifyEdgeChanges(recap.Tasks()), nil
}

// Inspect is the pre-apply check for an installed edge: it resolves the same
// role vars an apply would, runs the role in check mode without touching the
// host, and reports whether an apply would change anything and whether that
// change interrupts media. Preflight, registration and HTTPS verification do
// not run; a failed check is reported as interrupting so callers never skip
// a drain on an error.
func (e *EdgeProvisioner) Inspect(ctx context.Context, host inventory.Host, config EdgeProvisionConfig) (EdgeInspection, error) {
	mode := config.resolvedMode()
	if mode != "native" && mode != "container" {
		return EdgeInspection{Changed: true, Interrupting: true}, fmt.Errorf("invalid edge mode %q", config.Mode)
	}
	if strings.TrimSpace(config.Version) == "" {
		config.Version = "stable"
	}
	remoteOS, remoteArch, err := e.DetectRemoteArch(ctx, host)
	if err != nil {
		return EdgeInspection{Changed: true, Interrupting: true}, fmt.Errorf("failed to detect remote OS: %w", err)
	}
	profile, err := e.resolveONNXProfile(ctx, host, mode, remoteOS, remoteArch, config.ONNXProfile)
	if err != nil {
		return EdgeInspection{Changed: true, Interrupting: true}, err
	}
	config.ONNXProfile = profile
	if profile == "openvino" && mode == "container" {
		if result, probeErr := e.RunCommand(ctx, host, "test -d /dev/dri"); probeErr == nil && result.ExitCode == 0 {
			config.onnxDRIDevice = true
		}
	}
	if err := ensureRemoteAnsiblePython(ctx, e.sshPool, host, true); err != nil {
		return EdgeInspection{Changed: true, Interrupting: true}, err
	}
	return inspectEdgeRole(ctx, e, host, &config, remoteOS, remoteArch, false, nil)
}
