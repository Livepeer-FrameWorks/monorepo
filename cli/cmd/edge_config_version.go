package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"frameworks/cli/internal/releases"
	"frameworks/cli/internal/ux"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/health"

	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	fwversion "github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"github.com/spf13/cobra"
)

// edgeConfigMarkerFile is the provisioned-config marker the edge role writes
// into Helmsman's state dir (tasks/config-marker.yml).
const edgeConfigMarkerFile = "provisioned-config.env"

type edgeConfigState string

const (
	edgeConfigCurrent  edgeConfigState = "current"
	edgeConfigStale    edgeConfigState = "stale"
	edgeConfigUnmarked edgeConfigState = "unmarked"
	edgeConfigUnknown  edgeConfigState = "unknown"
)

var edgeConfigReleasePattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// classifyEdgeConfig compares the CLI release recorded in a node's
// provisioned-config marker with the running CLI. A node without a marker
// was rendered by a CLI that predates markers, so it is behind as well.
// Development builds (and markers they wrote) carry no comparable release.
func classifyEdgeConfig(nodeCLIVersion, nodeDigest, cliVersion string) (edgeConfigState, string) {
	nodeCLIVersion = strings.TrimSpace(nodeCLIVersion)
	if nodeCLIVersion == "" && strings.TrimSpace(nodeDigest) == "" {
		return edgeConfigUnmarked, "no provisioned-config marker (rendered before this CLI's edge config)"
	}
	current := releases.StripGitDescribeSuffix(cliVersion)
	if !edgeConfigReleasePattern.MatchString(current) {
		return edgeConfigUnknown, fmt.Sprintf("CLI %q is not a release build; cannot compare with %s", cliVersion, nodeCLIVersion)
	}
	node := releases.StripGitDescribeSuffix(nodeCLIVersion)
	if !edgeConfigReleasePattern.MatchString(node) {
		return edgeConfigUnknown, fmt.Sprintf("rendered by non-release CLI %q", nodeCLIVersion)
	}
	if releases.CompareSemver(node, current) < 0 {
		return edgeConfigStale, fmt.Sprintf("rendered by CLI %s, current CLI is %s", nodeCLIVersion, cliVersion)
	}
	return edgeConfigCurrent, "rendered by CLI " + nodeCLIVersion
}

type edgeConfigNode struct {
	Name       string
	CLIVersion string
	Digest     string
	Reported   bool
}

// evaluateEdgeConfigVersions builds the doctor verdict over the cluster's
// edges: every stale or unmarked edge is listed; an edge whose health was
// not available is listed as not reporting.
func evaluateEdgeConfigVersions(nodes []edgeConfigNode, cliVersion string) *health.CheckResult {
	result := &health.CheckResult{Name: "edge_config_version", CheckedAt: time.Now(), Metadata: map[string]string{}}
	if len(nodes) == 0 {
		result.OK = true
		result.Status = "healthy"
		result.Message = "no edge nodes registered"
		return result
	}
	var behind, unknown, silent []string
	for _, n := range nodes {
		if !n.Reported {
			silent = append(silent, n.Name)
			continue
		}
		state, detail := classifyEdgeConfig(n.CLIVersion, n.Digest, cliVersion)
		switch state {
		case edgeConfigStale, edgeConfigUnmarked:
			behind = append(behind, fmt.Sprintf("%s (%s)", n.Name, detail))
		case edgeConfigUnknown:
			unknown = append(unknown, fmt.Sprintf("%s (%s)", n.Name, detail))
		}
	}
	sort.Strings(behind)
	sort.Strings(unknown)
	sort.Strings(silent)
	if len(silent) > 0 {
		result.Metadata["not_reporting"] = strings.Join(silent, ", ")
	}
	if len(unknown) > 0 {
		result.Metadata["not_comparable"] = strings.Join(unknown, "; ")
	}
	if len(behind) > 0 {
		result.Status = "degraded"
		result.Message = fmt.Sprintf("%d/%d edge(s) run config older than this CLI: %s", len(behind), len(nodes), strings.Join(behind, "; "))
		return result
	}
	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("%d edge(s) run this CLI's edge config", len(nodes)-len(silent)-len(unknown))
	if len(silent) > 0 {
		result.Message += fmt.Sprintf("; %d not reporting (%s)", len(silent), strings.Join(silent, ", "))
	}
	if len(unknown) > 0 {
		result.Message += "; not comparable: " + strings.Join(unknown, "; ")
	}
	return result
}

// edgeConfigNodesFromHealth joins the registered edge nodes with the
// provisioned-config marker Foghorn reports in node health.
func edgeConfigNodesFromHealth(nodes []*quartermasterpb.InfrastructureNode, healthByID map[string]*foghorncontrolpb.GetNodeHealthResponse) []edgeConfigNode {
	out := make([]edgeConfigNode, 0, len(nodes))
	for _, n := range nodes {
		name := firstNonEmpty(n.GetNodeName(), n.GetNodeId())
		h := healthByID[n.GetNodeId()]
		out = append(out, edgeConfigNode{
			Name:       name,
			CLIVersion: h.GetProvisionedConfigCliVersion(),
			Digest:     h.GetProvisionedConfigDigest(),
			Reported:   h != nil,
		})
	}
	return out
}

// edgeConfigMarkerDirs are Helmsman's state dirs per install layout, in
// probe order: container role (./state bind), Linux native, macOS system
// domain, macOS user domain.
func edgeConfigMarkerDirs(home string) []string {
	dirs := []string{
		edgeAnsibleBaseDir + "/state",
		"/var/lib/frameworks/helmsman",
		"/usr/local/var/lib/frameworks/helmsman",
	}
	if strings.TrimSpace(home) != "" {
		dirs = append(dirs, filepath.Join(home, ".local/var/lib/frameworks/helmsman"))
	}
	return dirs
}

// edgeConfigMarker is a parsed provisioned-config marker.
type edgeConfigMarker struct {
	Path       string `json:"path,omitempty"`
	CLIVersion string `json:"cli_version,omitempty"`
	Digest     string `json:"digest,omitempty"`
	State      string `json:"state"`
	Detail     string `json:"detail,omitempty"`
}

func parseEdgeConfigMarker(content string) (cliVersion, digest string) {
	for line := range strings.SplitSeq(content, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "EDGE_CONFIG_CLI_VERSION":
			cliVersion = strings.TrimSpace(value)
		case "EDGE_CONFIG_DIGEST":
			digest = strings.TrimSpace(value)
		}
	}
	return cliVersion, digest
}

// localEdgeConfigMarker reads this host's marker. The container state dir is
// 0700 and owned by the in-container frameworks user, so an unreadable
// marker is retried through passwordless sudo.
func localEdgeConfigMarker(readFile func(string) ([]byte, error), sudoRead func(string) (content string, exists bool, err error), home string) edgeConfigMarker {
	for _, dir := range edgeConfigMarkerDirs(home) {
		path := filepath.Join(dir, edgeConfigMarkerFile)
		raw, err := readFile(path)
		content := string(raw)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			sudoContent, exists, sudoErr := sudoRead(path)
			if sudoErr != nil {
				return edgeConfigMarker{Path: path, State: string(edgeConfigUnknown), Detail: fmt.Sprintf("marker unreadable: %v", err)}
			}
			if !exists {
				continue
			}
			content = sudoContent
		}
		version, digest := parseEdgeConfigMarker(content)
		state, detail := classifyEdgeConfig(version, digest, fwversion.Version)
		return edgeConfigMarker{Path: path, CLIVersion: version, Digest: digest, State: string(state), Detail: detail}
	}
	state, detail := classifyEdgeConfig("", "", fwversion.Version)
	return edgeConfigMarker{State: string(state), Detail: detail}
}

func renderEdgeConfigMarker(w io.Writer, m edgeConfigMarker) {
	fmt.Fprintln(w, "\nConfig Version:")
	line := fmt.Sprintf("%-18s %s", m.State+":", m.Detail)
	if m.Digest != "" {
		line += "  digest=" + m.Digest
	}
	switch edgeConfigState(m.State) {
	case edgeConfigCurrent:
		ux.Success(w, line)
	case edgeConfigStale, edgeConfigUnmarked:
		ux.Warn(w, line)
	default:
		fmt.Fprintln(w, " "+line)
	}
}

// edgeConfigMarkerNextStep points a node whose config is behind the CLI at
// the provision re-run that renders the current config.
func edgeConfigMarkerNextStep(m edgeConfigMarker) (ux.NextStep, bool) {
	switch edgeConfigState(m.State) {
	case edgeConfigStale, edgeConfigUnmarked:
		return ux.NextStep{
			Cmd: "frameworks edge provision --ssh <user@this-node> --dry-run",
			Why: "This node runs config older than this CLI; the dry-run shows the drift, a run without --dry-run applies it (draining first when Mist or Caddy must restart).",
		}, true
	}
	return ux.NextStep{}, false
}

// sudoReadEdgeConfigMarker reads path with passwordless sudo. Exit 3 means
// the file does not exist; any other failure (including a refused sudo) is
// an error.
func sudoReadEdgeConfigMarker(cmd *cobra.Command) func(string) (string, bool, error) {
	return func(path string) (string, bool, error) {
		code, out, errOut, err := xexec.Run(cmd.Context(), "sudo", []string{"-n", "sh", "-c", `test -e "$1" || exit 3; cat "$1"`, "_", path}, "")
		if code == 3 {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("%w: %s", err, strings.TrimSpace(errOut))
		}
		return out, true, nil
	}
}

// doctorEdgeConfigVersions lists the active cluster's edges whose config is
// older than this CLI's edge config. It needs lifecycle (Quartermaster +
// Foghorn) access from the active context; without it the check reports a
// warning instead of guessing.
func doctorEdgeConfigVersions(cmd *cobra.Command) *health.CheckResult {
	qm, ctxCfg, cleanup, err := clusterNodesQMClientFromContext(cmd.Context())
	if err != nil {
		return &health.CheckResult{Name: "edge_config_version", Status: yugabyteLayoutWarning, CheckedAt: time.Now(),
			Message: fmt.Sprintf("not checked: %v", err)}
	}
	defer cleanup()
	defer func() { _ = qm.Close() }()
	cctx, cancel := clusterNodesRPCContext(cmd.Context(), ctxCfg, 15*time.Second)
	resp, err := qm.ListNodes(cctx, ctxCfg.ClusterID, "edge", "", nil)
	cancel()
	if err != nil {
		return &health.CheckResult{Name: "edge_config_version", Status: yugabyteLayoutWarning, CheckedAt: time.Now(),
			Message: fmt.Sprintf("not checked: list edge nodes: %v", err)}
	}
	healthByID := loadNodeHealth(cmd, resp.GetNodes())
	return evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(resp.GetNodes(), healthByID), fwversion.Version)
}
