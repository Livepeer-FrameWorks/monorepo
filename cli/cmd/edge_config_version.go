package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/releases"
	"frameworks/cli/internal/ux"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/provisioner"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
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

// edgeRenderDigest is this CLI's edge render digest, the value `edge
// provision` writes as EDGE_CONFIG_RENDER_DIGEST.
var edgeRenderDigest = provisioner.EdgeRenderDigest

// currentEdgeRenderDigest returns this CLI's render digest, or "" when its
// Ansible tree cannot be read, which leaves the release comparison.
func currentEdgeRenderDigest() string {
	digest, err := edgeRenderDigest()
	if err != nil {
		return ""
	}
	return digest
}

// classifyEdgeConfig compares a node's provisioned-config marker with the
// running CLI. When both carry a render digest, equal digests mean this CLI
// renders the node's provisioned files exactly as the CLI that wrote the
// marker did, whatever their releases. A marker without one (written before
// the render digest existed) falls back to comparing the CLI release it
// records. A node without a marker was rendered by a CLI that predates
// markers, so it is behind as well. Development builds (and markers they
// wrote) carry no comparable release.
func classifyEdgeConfig(nodeCLIVersion, nodeDigest, nodeRenderDigest, cliVersion, cliRenderDigest string) (edgeConfigState, string) {
	nodeCLIVersion = strings.TrimSpace(nodeCLIVersion)
	nodeRenderDigest = strings.TrimSpace(nodeRenderDigest)
	if nodeCLIVersion == "" && strings.TrimSpace(nodeDigest) == "" && nodeRenderDigest == "" {
		return edgeConfigUnmarked, "no provisioned-config marker (rendered before this CLI's edge config)"
	}
	if cliRenderDigest = strings.TrimSpace(cliRenderDigest); nodeRenderDigest != "" && cliRenderDigest != "" {
		if nodeRenderDigest == cliRenderDigest {
			return edgeConfigCurrent, fmt.Sprintf("rendered by CLI %s; this CLI renders the same provisioned files", nodeCLIVersion)
		}
		if newerEdgeConfigRelease(nodeCLIVersion, cliVersion) {
			return edgeConfigCurrent, fmt.Sprintf("rendered by newer CLI %s", nodeCLIVersion)
		}
		return edgeConfigStale, fmt.Sprintf("rendered by CLI %s; CLI %s renders the provisioned files differently", nodeCLIVersion, cliVersion)
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

// newerEdgeConfigRelease reports whether the node's marker names a release
// newer than this CLI. An older CLI renders differently from a newer one by
// design, which is not drift on the node.
func newerEdgeConfigRelease(nodeCLIVersion, cliVersion string) bool {
	node := releases.StripGitDescribeSuffix(nodeCLIVersion)
	current := releases.StripGitDescribeSuffix(cliVersion)
	if !edgeConfigReleasePattern.MatchString(node) || !edgeConfigReleasePattern.MatchString(current) {
		return false
	}
	return releases.CompareSemver(node, current) > 0
}

type edgeConfigNode struct {
	Name         string
	CLIVersion   string
	Digest       string
	RenderDigest string
	Reported     bool
	// Unreported is why an edge without health has none.
	Unreported string
}

// evaluateEdgeConfigVersions builds the doctor verdict over the cluster's
// edges. Every stale or unmarked edge fails the check. An edge whose health
// could not be read (not reporting) or whose config cannot be compared with
// this CLI leaves the check unverified, which is a warning: the doctor only
// reports healthy when every edge's config was read and is current.
func evaluateEdgeConfigVersions(nodes []edgeConfigNode, cliVersion, cliRenderDigest string) *health.CheckResult {
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
			entry := n.Name
			if reason := strings.TrimSpace(n.Unreported); reason != "" {
				entry = fmt.Sprintf("%s (%s)", n.Name, reason)
			}
			silent = append(silent, entry)
			continue
		}
		state, detail := classifyEdgeConfig(n.CLIVersion, n.Digest, n.RenderDigest, cliVersion, cliRenderDigest)
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
		result.Metadata["not_reporting"] = strings.Join(silent, "; ")
	}
	if len(unknown) > 0 {
		result.Metadata["not_comparable"] = strings.Join(unknown, "; ")
	}
	var notes []string
	if len(silent) > 0 {
		notes = append(notes, fmt.Sprintf("%d not reporting: %s", len(silent), strings.Join(silent, "; ")))
	}
	if len(unknown) > 0 {
		notes = append(notes, "not comparable: "+strings.Join(unknown, "; "))
	}
	if len(behind) > 0 {
		result.Status = "degraded"
		result.Message = fmt.Sprintf("%d/%d edge(s) carry provisioned files behind this CLI's render: %s", len(behind), len(nodes), strings.Join(behind, "; "))
		if len(notes) > 0 {
			result.Message += "; " + strings.Join(notes, "; ")
		}
		// Components never wait on the marker; only the provisioned files
		// the rolling update leaves alone need the CLI.
		result.Message += ". Each edge updates its components itself through its rolling update (`frameworks cluster edges rollout status` shows progress); " +
			"`frameworks edge provision --ssh <user@edge> --dry-run` shows whether this CLI renders the provisioned files differently, and a run without --dry-run repairs them"
		return result
	}
	current := len(nodes) - len(silent) - len(unknown)
	if len(notes) > 0 {
		result.Status = yugabyteLayoutWarning
		result.Message = fmt.Sprintf("%d/%d edge(s) verified on this CLI's edge config; %s", current, len(nodes), strings.Join(notes, "; "))
		return result
	}
	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("%d edge(s) run this CLI's edge config", current)
	return result
}

// edgeConfigNodesFromHealth joins the registered edge nodes with the
// provisioned-config marker Foghorn reports in node health.
// unreported carries, per node ID, why a node has no health.
func edgeConfigNodesFromHealth(nodes []*quartermasterpb.InfrastructureNode, healthByID map[string]*foghorncontrolpb.GetNodeHealthResponse, unreported map[string]string) []edgeConfigNode {
	out := make([]edgeConfigNode, 0, len(nodes))
	for _, n := range nodes {
		name := firstNonEmpty(n.GetNodeName(), n.GetNodeId())
		h := healthByID[n.GetNodeId()]
		out = append(out, edgeConfigNode{
			Name:         name,
			CLIVersion:   h.GetProvisionedConfigCliVersion(),
			Digest:       h.GetProvisionedConfigDigest(),
			RenderDigest: h.GetProvisionedConfigRenderDigest(),
			Reported:     h != nil,
			Unreported:   unreported[n.GetNodeId()],
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
	Path         string `json:"path,omitempty"`
	CLIVersion   string `json:"cli_version,omitempty"`
	Digest       string `json:"digest,omitempty"`
	RenderDigest string `json:"render_digest,omitempty"`
	State        string `json:"state"`
	Detail       string `json:"detail,omitempty"`
}

func parseEdgeConfigMarker(content string) (cliVersion, digest, renderDigest string) {
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
		case "EDGE_CONFIG_RENDER_DIGEST":
			renderDigest = strings.TrimSpace(value)
		}
	}
	return cliVersion, digest, renderDigest
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
		version, digest, renderDigest := parseEdgeConfigMarker(content)
		state, detail := classifyEdgeConfig(version, digest, renderDigest, fwversion.Version, currentEdgeRenderDigest())
		return edgeConfigMarker{Path: path, CLIVersion: version, Digest: digest, RenderDigest: renderDigest, State: string(state), Detail: detail}
	}
	state, detail := classifyEdgeConfig("", "", "", fwversion.Version, "")
	return edgeConfigMarker{State: string(state), Detail: detail}
}

func renderEdgeConfigMarker(w io.Writer, m edgeConfigMarker) {
	fmt.Fprintln(w, "\nConfig Version:")
	line := fmt.Sprintf("%-18s %s", m.State+":", m.Detail)
	if m.Digest != "" {
		line += "  digest=" + m.Digest
	}
	if m.RenderDigest != "" {
		line += "  render=" + m.RenderDigest
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

// edgeConfigMarkerNextSteps answers a node whose provisioned-config marker is
// behind the CLI. Helmsman, MistServer and Caddy follow the cluster's release
// target through the edge's rolling update, and Helmsman renders the
// production Caddyfile and MistServer config itself, so none of that waits on
// the marker. The marker moves only when provisioning re-renders the files
// the rolling update leaves alone, which the last step covers.
func edgeConfigMarkerNextSteps(m edgeConfigMarker) []ux.NextStep {
	switch edgeConfigState(m.State) {
	case edgeConfigStale, edgeConfigUnmarked:
		return []ux.NextStep{
			{
				Cmd: "frameworks cluster edges rollout status",
				Why: "This edge updates Helmsman, MistServer and Caddy itself through its rolling update; nothing is needed while that runs. This shows whether it reached its cluster's release target.",
			},
			{
				Cmd: "frameworks edge logs helmsman",
				Why: "If the edge stays behind its release target, Helmsman's log shows why an update did not apply.",
			},
			{
				Cmd: "frameworks edge provision --ssh <user@this-node> --dry-run",
				Why: "The rolling update does not render the provisioned files (edge env and service units or compose file, vmagent config, maintenance page). The dry-run shows whether this CLI renders them differently; a run without --dry-run repairs them.",
			},
		}
	}
	return nil
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

// doctorEdgeConfigVersions lists the cluster's edges whose provisioned files
// are behind this CLI's render (classifyEdgeConfig). Endpoints resolve against the doctor's own
// manifest, and each edge's health is read from the Foghorn serving that
// edge's cluster. It needs lifecycle (Quartermaster + Foghorn) access for that
// manifest; without it the check reports a warning instead of guessing.
func doctorEdgeConfigVersions(ctx context.Context, rc *resolvedCluster) *health.CheckResult {
	notChecked := func(format string, args ...any) *health.CheckResult {
		return &health.CheckResult{Name: "edge_config_version", Status: yugabyteLayoutWarning, CheckedAt: time.Now(),
			Message: "not checked: " + fmt.Sprintf(format, args...)}
	}
	ctxCfg, err := lifecycleContextForResolved(ctx, rc)
	if err != nil {
		return notChecked("%v", err)
	}
	resolver := controlplane.NewResolverWithManifest(ctxCfg, rc.Manifest, rc.ManifestPath, rc.AgeKey)
	defer resolver.Close()
	ep, err := resolver.ResolveGRPC(ctx, "quartermaster")
	if err != nil {
		return notChecked("resolve quartermaster: %v", err)
	}
	qm, err := qmclient.NewGRPCClient(clusterNodesQuartermasterGRPCConfig(ep, ctxCfg))
	if err != nil {
		return notChecked("connect Quartermaster gRPC: %v", err)
	}
	defer func() { _ = qm.Close() }()
	cctx, cancel := clusterNodesRPCContext(ctx, ctxCfg, 15*time.Second)
	resp, err := qm.ListNodes(cctx, ctxCfg.ClusterID, "edge", "", nil)
	cancel()
	if err != nil {
		return notChecked("list edge nodes: %v", err)
	}
	healthByID, _, missing := collectNodeHealth(ctx, ctxCfg, resp.GetNodes(), foghornNodeHealthDialer(resolver, ctxCfg))
	return evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(resp.GetNodes(), healthByID, missing), fwversion.Version, currentEdgeRenderDigest())
}
