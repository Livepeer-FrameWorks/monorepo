package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"

	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	fwversion "github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

func withCLIVersion(t *testing.T, v string) {
	t.Helper()
	prev := fwversion.Version
	fwversion.Version = v
	t.Cleanup(func() { fwversion.Version = prev })
}

func withEdgeRenderDigest(t *testing.T, digest string) {
	t.Helper()
	prev := edgeRenderDigest
	edgeRenderDigest = func() (string, error) { return digest, nil }
	t.Cleanup(func() { edgeRenderDigest = prev })
}

func TestClassifyEdgeConfig(t *testing.T) {
	cases := []struct {
		name, node, digest, nodeRender, cli, cliRender string
		want                                           edgeConfigState
	}{
		// Markers without a render digest compare the CLI release.
		{"older release is stale", "v0.3.10", "sha256:a", "", "v0.3.11", "sha256:r1", edgeConfigStale},
		{"rc before its release is stale", "v0.3.11-rc4", "sha256:a", "", "v0.3.11", "sha256:r1", edgeConfigStale},
		{"same release is current", "v0.3.11", "sha256:a", "", "v0.3.11", "sha256:r1", edgeConfigCurrent},
		{"newer node than CLI is current", "v0.3.12", "sha256:a", "", "v0.3.11", "sha256:r1", edgeConfigCurrent},
		{"git describe build compares as its tag", "v0.3.11", "sha256:a", "", "v0.3.11-4-gabcdef0", "sha256:r1", edgeConfigCurrent},
		{"no marker is behind", "", "", "", "v0.3.11", "sha256:r1", edgeConfigUnmarked},
		{"dev CLI cannot compare", "v0.3.11", "sha256:a", "", "dev", "sha256:r1", edgeConfigUnknown},
		{"dev-rendered node cannot compare", "dev", "sha256:a", "", "v0.3.11", "sha256:r1", edgeConfigUnknown},
		// A CLI that cannot read its own render digest compares the release.
		{"no CLI render digest compares the release", "v0.3.10", "sha256:a", "sha256:r1", "v0.3.11", "", edgeConfigStale},
		// Markers with a render digest compare it with this CLI's.
		{"older release with the same render is current", "v0.3.10", "sha256:a", "sha256:r1", "v0.3.11", "sha256:r1", edgeConfigCurrent},
		{"older release with a different render is stale", "v0.3.10", "sha256:a", "sha256:r0", "v0.3.11", "sha256:r1", edgeConfigStale},
		{"same release with a different render is stale", "v0.3.11", "sha256:a", "sha256:r0", "v0.3.11", "sha256:r1", edgeConfigStale},
		{"newer node with a different render is current", "v0.3.12", "sha256:a", "sha256:r2", "v0.3.11", "sha256:r1", edgeConfigCurrent},
		{"dev builds with the same render are current", "dev", "sha256:a", "sha256:r1", "dev", "sha256:r1", edgeConfigCurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, detail := classifyEdgeConfig(tc.node, tc.digest, tc.nodeRender, tc.cli, tc.cliRender); got != tc.want {
				t.Fatalf("classifyEdgeConfig(%q, %q, %q, %q) = %s (%s), want %s", tc.node, tc.nodeRender, tc.cli, tc.cliRender, got, detail, tc.want)
			}
		})
	}
}

// An edge provisioned by an older CLI release whose render digest equals
// this CLI's carries exactly the provisioned files this CLI would render, so
// `edge doctor` reports it current instead of stale after every CLI release.
func TestLocalEdgeConfigMarkerWithThisCLIsRenderIsCurrent(t *testing.T) {
	withCLIVersion(t, "v0.3.12")
	withEdgeRenderDigest(t, "sha256:render")
	nativePath := filepath.Join("/var/lib/frameworks/helmsman", edgeConfigMarkerFile)
	readFile := func(path string) ([]byte, error) {
		if path == nativePath {
			return []byte("EDGE_CONFIG_CLI_VERSION=v0.3.11\nEDGE_CONFIG_DIGEST=sha256:abc\nEDGE_CONFIG_RENDER_DIGEST=sha256:render\n"), nil
		}
		return nil, fs.ErrNotExist
	}
	noSudo := func(string) (string, bool, error) { return "", false, nil }
	marker := localEdgeConfigMarker(readFile, noSudo, "")
	if marker.State != string(edgeConfigCurrent) {
		t.Fatalf("marker from older CLI with this CLI's render = %+v, want current", marker)
	}
	if got := edgeConfigMarkerNextSteps(marker); len(got) != 0 {
		t.Fatalf("current marker next steps = %+v, want none", got)
	}

	withEdgeRenderDigest(t, "sha256:other")
	if marker := localEdgeConfigMarker(readFile, noSudo, ""); marker.State != string(edgeConfigStale) || !strings.Contains(marker.Detail, "renders the provisioned files differently") {
		t.Fatalf("marker with a different render = %+v, want stale", marker)
	}
}

// cluster doctor compares each edge's reported render digest with this CLI's:
// an edge from an older release with the same render passes, one with a
// different render fails, and one whose marker predates the render digest
// keeps the release comparison.
func TestEvaluateEdgeConfigVersionsComparesRenderDigest(t *testing.T) {
	nodes := []*quartermasterpb.InfrastructureNode{
		{NodeId: "n1", NodeName: "edge-same"},
		{NodeId: "n2", NodeName: "edge-diff"},
		{NodeId: "n3", NodeName: "edge-old-marker"},
	}
	healthByID := map[string]*foghorncontrolpb.GetNodeHealthResponse{
		"n1": {ProvisionedConfigCliVersion: "v0.3.10", ProvisionedConfigDigest: "sha256:a", ProvisionedConfigRenderDigest: "sha256:r1"},
		"n2": {ProvisionedConfigCliVersion: "v0.3.10", ProvisionedConfigDigest: "sha256:b", ProvisionedConfigRenderDigest: "sha256:r0"},
		"n3": {ProvisionedConfigCliVersion: "v0.3.10", ProvisionedConfigDigest: "sha256:c"},
	}
	result := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, healthByID, nil), "v0.3.11", "sha256:r1")
	if result.OK || result.Status != "degraded" {
		t.Fatalf("result = ok=%v status=%s, want failing degraded", result.OK, result.Status)
	}
	for _, want := range []string{
		"2/3",
		"edge-diff (rendered by CLI v0.3.10; CLI v0.3.11 renders the provisioned files differently)",
		"edge-old-marker (rendered by CLI v0.3.10, current CLI is v0.3.11)",
	} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("message %q missing %q", result.Message, want)
		}
	}
	if strings.Contains(result.Message, "edge-same") {
		t.Fatalf("edge with this CLI's render listed as stale: %q", result.Message)
	}

	same := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes[:1], healthByID, nil), "v0.3.11", "sha256:r1")
	if !same.OK || same.Status != "healthy" {
		t.Fatalf("edge with this CLI's render = ok=%v status=%s (%s), want healthy", same.OK, same.Status, same.Message)
	}
}

// cluster doctor lists every edge whose config is older than this CLI's and
// fails the check; edges without health are named as not reporting.
func TestEvaluateEdgeConfigVersionsListsStaleEdges(t *testing.T) {
	nodes := []*quartermasterpb.InfrastructureNode{
		{NodeId: "n1", NodeName: "edge-a"},
		{NodeId: "n2", NodeName: "edge-b"},
		{NodeId: "n3", NodeName: "edge-c"},
		{NodeId: "n4", NodeName: "edge-d"},
	}
	healthByID := map[string]*foghorncontrolpb.GetNodeHealthResponse{
		"n1": {ProvisionedConfigCliVersion: "v0.3.11", ProvisionedConfigDigest: "sha256:a"},
		"n2": {ProvisionedConfigCliVersion: "v0.3.10", ProvisionedConfigDigest: "sha256:b"},
		"n3": {},
	}
	result := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, healthByID, nil), "v0.3.11", "sha256:r1")
	if result.OK || result.Status != "degraded" {
		t.Fatalf("result = ok=%v status=%s, want failing degraded", result.OK, result.Status)
	}
	for _, want := range []string{"2/4", "edge-b (rendered by CLI v0.3.10, current CLI is v0.3.11)", "edge-c (no provisioned-config marker"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("message %q missing %q", result.Message, want)
		}
	}
	// Edges take config through their own flow; release apply never re-renders them.
	if strings.Contains(result.Message, "release apply") {
		t.Fatalf("message tells operators release apply re-renders edges: %q", result.Message)
	}
	// The rolling update is the edge's own path; provision only repairs the
	// provisioned files and never implies a drain.
	for _, want := range []string{"frameworks cluster edges rollout status", "frameworks edge provision --ssh <user@edge> --dry-run"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("message %q missing hint %q", result.Message, want)
		}
	}
	if strings.Contains(strings.ToLower(result.Message), "drain") {
		t.Fatalf("message tells operators to drain: %q", result.Message)
	}
	if strings.Contains(result.Message, "edge-a") {
		t.Fatalf("current edge listed as stale: %q", result.Message)
	}
	if result.Metadata["not_reporting"] != "edge-d" {
		t.Fatalf("not_reporting = %q, want edge-d", result.Metadata["not_reporting"])
	}

	current := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes[:1], healthByID, nil), "v0.3.11", "sha256:r1")
	if !current.OK || current.Status != "healthy" {
		t.Fatalf("all-current result = ok=%v status=%s (%s)", current.OK, current.Status, current.Message)
	}
}

func TestLocalEdgeConfigMarkerAndDoctorOutput(t *testing.T) {
	withCLIVersion(t, "v0.3.12")
	// The marker predates the render digest, so the release comparison applies.
	withEdgeRenderDigest(t, "sha256:render")
	nativePath := filepath.Join("/var/lib/frameworks/helmsman", edgeConfigMarkerFile)
	containerPath := filepath.Join(edgeAnsibleBaseDir, "state", edgeConfigMarkerFile)

	files := map[string]string{nativePath: "EDGE_CONFIG_CLI_VERSION=v0.3.11\nEDGE_CONFIG_DIGEST=sha256:abc\n"}
	readFile := func(path string) ([]byte, error) {
		if path == containerPath {
			return nil, fs.ErrPermission
		}
		if content, ok := files[path]; ok {
			return []byte(content), nil
		}
		return nil, fs.ErrNotExist
	}
	// The container state dir is unreadable without sudo and holds no marker.
	sudoRead := func(path string) (string, bool, error) {
		if path != containerPath {
			t.Fatalf("sudo read of unexpected path %s", path)
		}
		return "", false, nil
	}
	marker := localEdgeConfigMarker(readFile, sudoRead, "")
	if marker.Path != nativePath || marker.State != string(edgeConfigStale) || marker.Digest != "sha256:abc" {
		t.Fatalf("marker = %+v, want stale native marker", marker)
	}
	var out bytes.Buffer
	renderEdgeConfigMarker(&out, marker)
	if !strings.Contains(out.String(), "rendered by CLI v0.3.11, current CLI is v0.3.12") || !strings.Contains(out.String(), "digest=sha256:abc") {
		t.Fatalf("doctor output = %q", out.String())
	}
	// The rolling update comes first; provision is the repair for the files
	// it does not render, never a routine drain-and-reprovision.
	steps := edgeConfigMarkerNextSteps(marker)
	var cmds []string
	for _, step := range steps {
		cmds = append(cmds, step.Cmd)
		if strings.Contains(strings.ToLower(step.Why), "drain") {
			t.Fatalf("next step tells the operator to drain: %+v", step)
		}
	}
	wantCmds := []string{
		"frameworks cluster edges rollout status",
		"frameworks edge logs helmsman",
		"frameworks edge provision --ssh <user@this-node> --dry-run",
	}
	if !slices.Equal(cmds, wantCmds) {
		t.Fatalf("stale marker next steps = %q, want %q", cmds, wantCmds)
	}
	if !strings.Contains(steps[0].Why, "nothing is needed") || !strings.Contains(steps[2].Why, "does not render") {
		t.Fatalf("next steps do not separate the rolling update from the provisioned files: %+v", steps)
	}
	if got := edgeConfigMarkerNextSteps(edgeConfigMarker{State: string(edgeConfigCurrent)}); len(got) != 0 {
		t.Fatalf("current marker next steps = %+v, want none", got)
	}

	refused := func(string) (string, bool, error) { return "", false, errors.New("sudo: a password is required") }
	if got := localEdgeConfigMarker(readFile, refused, ""); got.State != string(edgeConfigUnknown) || got.Path != containerPath {
		t.Fatalf("unreadable marker = %+v, want unknown at %s", got, containerPath)
	}

	none := localEdgeConfigMarker(func(string) ([]byte, error) { return nil, fs.ErrNotExist }, sudoRead, "")
	if none.State != string(edgeConfigUnmarked) {
		t.Fatalf("host without marker = %+v, want unmarked", none)
	}
}

// An edge whose health cannot be read leaves the doctor's edge config check
// unverified: the check is a warning, printed [WARN], never [OK].
func TestEvaluateEdgeConfigVersionsNotReportingIsNeverHealthy(t *testing.T) {
	nodes := []*quartermasterpb.InfrastructureNode{
		{NodeId: "n1", NodeName: "edge-eu"},
		{NodeId: "n2", NodeName: "edge-us"},
	}
	unreported := map[string]string{"n1": `cluster "media-eu": foghorn down`, "n2": `cluster "media-us": foghorn down`}
	result := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, nil, unreported), "v0.3.11", "sha256:r1")
	if result.OK || result.Status != yugabyteLayoutWarning {
		t.Fatalf("all edges not reporting = ok=%v status=%s (%s), want warning", result.OK, result.Status, result.Message)
	}
	for _, want := range []string{"0/2 edge(s) verified", "2 not reporting", `edge-eu (cluster "media-eu": foghorn down)`} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("message %q missing %q", result.Message, want)
		}
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	printHealthResult(cmd, "Edge config version", result)
	if !strings.HasPrefix(out.String(), "[WARN]") {
		t.Fatalf("doctor line = %q, want [WARN]", out.String())
	}

	healthByID := map[string]*foghorncontrolpb.GetNodeHealthResponse{
		"n1": {ProvisionedConfigCliVersion: "v0.3.11", ProvisionedConfigDigest: "sha256:a"},
		"n2": {ProvisionedConfigCliVersion: "dev", ProvisionedConfigDigest: "sha256:b"},
	}
	notComparable := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, healthByID, nil), "v0.3.11", "sha256:r1")
	if notComparable.OK || notComparable.Status != yugabyteLayoutWarning || !strings.Contains(notComparable.Message, "1/2 edge(s) verified") {
		t.Fatalf("not-comparable edge = ok=%v status=%s (%s), want warning", notComparable.OK, notComparable.Status, notComparable.Message)
	}
}

// Each edge's health is read from the Foghorn serving the edge's own cluster;
// a cell whose Foghorn cannot be dialed marks only its own edges as not
// reporting, with the reason.
func TestCollectNodeHealthReadsEachEdgeFromItsCellFoghorn(t *testing.T) {
	manifest := restartFoghornCellManifest("eu", "us")
	nodes := []*quartermasterpb.InfrastructureNode{
		{NodeId: "eu-1", NodeName: "edge-eu-1", ClusterId: "media-eu"},
		{NodeId: "us-1", NodeName: "edge-us-1", ClusterId: "media-us"},
		{NodeId: "eu-2", NodeName: "edge-eu-2", ClusterId: "media-eu"},
	}
	var dialed []string
	dial := func(_ context.Context, clusterID string) (nodeHealthClient, func(), error) {
		entry, err := foghornEntryForCluster(manifest, clusterID)
		if err != nil {
			return nil, nil, err
		}
		dialed = append(dialed, entry)
		if entry == "foghorn-us" {
			return nil, nil, errors.New("connection refused")
		}
		return fakeNodeHealthClient{entry: entry}, func() {}, nil
	}
	healthByID, dialErrs, missing := collectNodeHealth(context.Background(), fwcfg.Context{}, nodes, dial)
	if strings.Join(dialed, ",") != "foghorn-eu,foghorn-us" {
		t.Fatalf("dialed %v, want each cell's Foghorn once", dialed)
	}
	if len(dialErrs) != 1 || !strings.Contains(dialErrs[0].Error(), `cluster "media-us": connection refused`) {
		t.Fatalf("dial errors = %v", dialErrs)
	}
	for _, id := range []string{"eu-1", "eu-2"} {
		if got := healthByID[id].GetProvisionedConfigCliVersion(); got != "v0.3.11" {
			t.Fatalf("%s health version = %q, want it read from foghorn-eu", id, got)
		}
	}
	if _, ok := healthByID["us-1"]; ok || !strings.Contains(missing["us-1"], "connection refused") {
		t.Fatalf("us-1 health=%v missing=%q, want not reporting with reason", healthByID["us-1"], missing["us-1"])
	}
	result := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, healthByID, missing), "v0.3.11", "sha256:r1")
	if result.OK || result.Status != yugabyteLayoutWarning || !strings.Contains(result.Message, "edge-us-1 (cluster \"media-us\": connection refused)") {
		t.Fatalf("result = ok=%v status=%s (%s)", result.OK, result.Status, result.Message)
	}
}

type fakeNodeHealthClient struct{ entry string }

func (f fakeNodeHealthClient) GetNodeHealth(_ context.Context, req *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, metadata.MD, error) {
	if f.entry != "foghorn-eu" || !strings.HasPrefix(req.GetNodeId(), "eu-") {
		return nil, nil, fmt.Errorf("node %s asked of %s", req.GetNodeId(), f.entry)
	}
	return &foghorncontrolpb.GetNodeHealthResponse{ProvisionedConfigCliVersion: "v0.3.11", ProvisionedConfigDigest: "sha256:a"}, nil, nil
}

// On a per-cell manifest every media cluster resolves to its own Foghorn
// entry; there is no service named "foghorn" to fall back to.
func TestFoghornEntryForClusterOnTwoCellManifest(t *testing.T) {
	twoCell := restartFoghornCellManifest("eu", "us")
	for clusterID, want := range map[string]string{"media-eu": "foghorn-eu", "media-us": "foghorn-us"} {
		if got, err := foghornEntryForCluster(twoCell, clusterID); err != nil || got != want {
			t.Fatalf("foghornEntryForCluster(%s) = %q, %v; want %s", clusterID, got, err, want)
		}
	}
	if got, err := foghornEntryForCluster(twoCell, ""); err == nil {
		t.Fatalf("no cluster on a two-cell manifest = %q, want an error", got)
	}
	if got, err := foghornEntryForCluster(twoCell, "media-ap"); err == nil {
		t.Fatalf("unserved cluster = %q, want an error", got)
	}
	if got, err := foghornEntryForCluster(restartFoghornCellManifest("eu"), ""); err != nil || got != "foghorn-eu" {
		t.Fatalf("single-cell manifest without cluster = %q, %v; want foghorn-eu", got, err)
	}
	if got, err := foghornEntryForCluster(nil, "media-eu"); err != nil || got != "" {
		t.Fatalf("local access = %q, %v; want the saved endpoint", got, err)
	}
}

// The doctor dials each edge's cell Foghorn at that cell's host.
func TestClusterNodesFoghornEndpointDialsTheEdgeCell(t *testing.T) {
	manifest := restartFoghornCellManifest("eu", "us")
	for name, host := range manifest.Hosts {
		host.WireguardIP = map[string]string{"media-eu-1": "10.88.0.11", "media-us-1": "10.88.0.21"}[name]
		manifest.Hosts[name] = host
	}
	ctxCfg := fwcfg.Context{Name: "platform", Persona: fwcfg.PersonaPlatform, AccessMode: fwcfg.AccessModeMesh}
	resolver := controlplane.NewResolverWithManifest(ctxCfg, manifest, filepath.Join(t.TempDir(), "cluster.yaml"), "")
	defer resolver.Close()
	for clusterID, want := range map[string]string{"media-eu": "10.88.0.11:", "media-us": "10.88.0.21:"} {
		ep, entry, err := clusterNodesFoghornEndpoint(context.Background(), resolver, clusterID)
		if err != nil {
			t.Fatalf("resolve Foghorn for %s: %v", clusterID, err)
		}
		if !strings.HasPrefix(ep.Address, want) || entry != "foghorn-"+strings.TrimPrefix(clusterID, "media-") {
			t.Fatalf("%s resolved %s at %q, want %s*", clusterID, entry, ep.Address, want)
		}
	}
}
