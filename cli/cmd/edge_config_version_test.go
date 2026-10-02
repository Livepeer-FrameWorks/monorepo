package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
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

func TestClassifyEdgeConfig(t *testing.T) {
	cases := []struct {
		name, node, digest, cli string
		want                    edgeConfigState
	}{
		{"older release is stale", "v0.3.10", "sha256:a", "v0.3.11", edgeConfigStale},
		{"rc before its release is stale", "v0.3.11-rc4", "sha256:a", "v0.3.11", edgeConfigStale},
		{"same release is current", "v0.3.11", "sha256:a", "v0.3.11", edgeConfigCurrent},
		{"newer node than CLI is current", "v0.3.12", "sha256:a", "v0.3.11", edgeConfigCurrent},
		{"git describe build compares as its tag", "v0.3.11", "sha256:a", "v0.3.11-4-gabcdef0", edgeConfigCurrent},
		{"no marker is behind", "", "", "v0.3.11", edgeConfigUnmarked},
		{"dev CLI cannot compare", "v0.3.11", "sha256:a", "dev", edgeConfigUnknown},
		{"dev-rendered node cannot compare", "dev", "sha256:a", "v0.3.11", edgeConfigUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, detail := classifyEdgeConfig(tc.node, tc.digest, tc.cli); got != tc.want {
				t.Fatalf("classifyEdgeConfig(%q, %q) = %s (%s), want %s", tc.node, tc.cli, got, detail, tc.want)
			}
		})
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
	result := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, healthByID, nil), "v0.3.11")
	if result.OK || result.Status != "degraded" {
		t.Fatalf("result = ok=%v status=%s, want failing degraded", result.OK, result.Status)
	}
	for _, want := range []string{"2/4", "edge-b (rendered by CLI v0.3.10, current CLI is v0.3.11)", "edge-c (no provisioned-config marker"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("message %q missing %q", result.Message, want)
		}
	}
	if strings.Contains(result.Message, "edge-a") {
		t.Fatalf("current edge listed as stale: %q", result.Message)
	}
	if result.Metadata["not_reporting"] != "edge-d" {
		t.Fatalf("not_reporting = %q, want edge-d", result.Metadata["not_reporting"])
	}

	current := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes[:1], healthByID, nil), "v0.3.11")
	if !current.OK || current.Status != "healthy" {
		t.Fatalf("all-current result = ok=%v status=%s (%s)", current.OK, current.Status, current.Message)
	}
}

func TestLocalEdgeConfigMarkerAndDoctorOutput(t *testing.T) {
	withCLIVersion(t, "v0.3.12")
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
	if step, ok := edgeConfigMarkerNextStep(marker); !ok || !strings.Contains(step.Cmd, "edge provision") {
		t.Fatalf("stale marker next step = %+v, %v", step, ok)
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
	result := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, nil, unreported), "v0.3.11")
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
	notComparable := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, healthByID, nil), "v0.3.11")
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
	result := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, healthByID, missing), "v0.3.11")
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
