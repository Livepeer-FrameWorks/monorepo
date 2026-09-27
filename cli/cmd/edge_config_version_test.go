package cmd

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

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
	result := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes, healthByID), "v0.3.11")
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

	current := evaluateEdgeConfigVersions(edgeConfigNodesFromHealth(nodes[:1], healthByID), "v0.3.11")
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
