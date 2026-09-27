package provisioner

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/inventory"
)

// edgeInterruptingReports are the check-mode handler results (roles
// mistserver and edge, handlers/main.yml) that mean applying the plan would
// restart MistServer or Caddy, or recreate the edge container, and so drop
// the node's live sessions. A MistServer binary change alone is not among
// them: it goes through the rolling USR1 reload that keeps streams up.
var edgeInterruptingReports = []string{
	"Report mistserver restart in check mode",
	"Report edge caddy restart in check mode",
	"Report edge container recreate in check mode",
}

// EdgeInterruptingReports returns the handler names whose check-mode change
// marks an edge plan as interrupting.
func EdgeInterruptingReports() []string {
	return slices.Clone(edgeInterruptingReports)
}

// EdgeInspection is the check-mode verdict for one edge node.
type EdgeInspection struct {
	// Changed is true when applying the role would change anything.
	Changed bool
	// Interrupting is true when applying would restart MistServer or Caddy
	// or recreate the edge container.
	Interrupting bool
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
			break
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
		return fmt.Sprintf("drift: %d change(s); applying restarts media services (node is drained first)", len(i.ChangedTasks))
	default:
		return fmt.Sprintf("drift: %d change(s); applies without interrupting media", len(i.ChangedTasks))
	}
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
