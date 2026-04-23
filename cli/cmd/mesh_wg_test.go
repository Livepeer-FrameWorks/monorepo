package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/internal/mesh"
	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

func TestMeshWgGenerateFreshManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "cluster.yaml")
	hostsPath := filepath.Join(dir, "hosts.enc.yaml")
	writeFile(t, manifestPath, `version: "1"
type: cluster
profile: production
hosts_file: hosts.enc.yaml
hosts:
  core-1: {}
  core-2: {}
services:
  privateer:
    enabled: true
    hosts: [core-1, core-2]
`)
	writeFile(t, hostsPath, `hosts:
  core-1:
    external_ip: 203.0.113.10
    user: ubuntu
  core-2:
    external_ip: 203.0.113.11
    user: ubuntu
`)

	cmd := testCobraCommand()
	if err := runMeshWgGenerate(cmd, manifestPath, hostsPath, "", "10.88.0.0/16", 51820, "", false); err != nil {
		t.Fatalf("runMeshWgGenerate: %v", err)
	}

	got, err := inventory.LoadWithHostsFileNoValidate(manifestPath, hostsPath, "")
	if err != nil {
		t.Fatalf("load generated manifest: %v", err)
	}
	if err := mesh.ValidateIdentity(got, []string{"core-1", "core-2"}); err != nil {
		t.Fatalf("generated identity did not validate: %v", err)
	}
	if got.WireGuard == nil || !got.WireGuard.Enabled || got.WireGuard.MeshCIDR != "10.88.0.0/16" {
		t.Fatalf("wireguard block not populated correctly: %+v", got.WireGuard)
	}
	if got.Hosts["core-1"].WireguardIP == "" || got.Hosts["core-1"].WireguardPrivateKey == "" {
		t.Fatalf("core-1 identity incomplete after generation: %+v", got.Hosts["core-1"])
	}
}

func TestMeshWgGenerateRepairsTopLevelWireGuardBlock(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "cluster.yaml")
	hostsPath := filepath.Join(dir, "hosts.enc.yaml")
	writeFile(t, manifestPath, `version: "1"
type: cluster
profile: production
hosts_file: hosts.enc.yaml
hosts:
  core-1: {}
services:
  privateer:
    enabled: true
    hosts: [core-1]
`)
	writeFile(t, hostsPath, `hosts:
  core-1:
    external_ip: 203.0.113.10
    user: ubuntu
`)

	cmd := testCobraCommand()
	if err := runMeshWgGenerate(cmd, manifestPath, hostsPath, "", "10.88.0.0/16", 51820, "", false); err != nil {
		t.Fatalf("initial runMeshWgGenerate: %v", err)
	}

	raw := readFile(t, manifestPath)
	writeFile(t, manifestPath, strings.Replace(raw, "enabled: true", "enabled: false", 1))

	if err := runMeshWgGenerate(cmd, manifestPath, hostsPath, "", "10.88.0.0/16", 51820, "", false); err != nil {
		t.Fatalf("repair runMeshWgGenerate: %v", err)
	}
	got, err := inventory.LoadWithHostsFileNoValidate(manifestPath, hostsPath, "")
	if err != nil {
		t.Fatalf("load repaired manifest: %v", err)
	}
	if got.WireGuard == nil || !got.WireGuard.Enabled {
		t.Fatalf("wireguard block was not repaired: %+v", got.WireGuard)
	}
	if err := mesh.ValidateIdentity(got, []string{"core-1"}); err != nil {
		t.Fatalf("repaired identity did not validate: %v", err)
	}
}

func TestMeshWgRotateUnknownHostFailsWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "cluster.yaml")
	hostsPath := filepath.Join(dir, "hosts.enc.yaml")
	writeFile(t, manifestPath, `version: "1"
type: cluster
profile: production
hosts_file: hosts.enc.yaml
hosts:
  core-1: {}
`)
	writeFile(t, hostsPath, `hosts:
  core-1:
    external_ip: 203.0.113.10
    user: ubuntu
`)
	beforeManifest := readFile(t, manifestPath)
	beforeHosts := readFile(t, hostsPath)

	err := runMeshWgGenerate(testCobraCommand(), manifestPath, hostsPath, "", "10.88.0.0/16", 51820, "missing-host", false)
	if err == nil || !strings.Contains(err.Error(), `host "missing-host" not found`) {
		t.Fatalf("expected unknown host error, got %v", err)
	}
	if got := readFile(t, manifestPath); got != beforeManifest {
		t.Fatal("manifest changed after failed unknown-host rotation")
	}
	if got := readFile(t, hostsPath); got != beforeHosts {
		t.Fatal("host inventory changed after failed unknown-host rotation")
	}
}

func TestValidateProvisionMeshIdentityStrictAndReadOnly(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core-1": {Name: "core-1", ExternalIP: "203.0.113.10", User: "ubuntu"},
		},
		Services: map[string]inventory.ServiceConfig{
			"privateer": {Enabled: true, Hosts: []string{"core-1"}},
		},
	}

	err := validateProvisionMeshIdentity(manifest, "Run: frameworks mesh wg generate --manifest /repo/clusters/production/cluster.yaml")
	if err == nil {
		t.Fatal("expected provision mesh validation to fail")
	}
	msg := err.Error()
	for _, want := range []string{"wireguard.enabled", "wireguard_ip", "wireguard_public_key", "wireguard_private_key", "frameworks mesh wg generate"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected validation error to contain %q, got:\n%s", want, msg)
		}
	}
}

func TestValidateProvisionMeshIdentitySkipsDisabledPrivateer(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"local-1": {Name: "local-1", ExternalIP: "127.0.0.1", User: "dev"},
		},
		Services: map[string]inventory.ServiceConfig{
			"privateer": {Enabled: false, Hosts: []string{"local-1"}},
		},
		WireGuard: &inventory.WireGuardConfig{Enabled: false},
	}

	if err := validateProvisionMeshIdentity(manifest, "cluster.yaml"); err != nil {
		t.Fatalf("disabled privateer should not require mesh identity: %v", err)
	}
}

func TestClusterProvisionRejectsPositionalArgs(t *testing.T) {
	cmd := newClusterProvisionCmd()
	if err := cmd.Args(cmd, []string{"production"}); err == nil {
		t.Fatal("expected positional cluster name to be rejected")
	}
}

func TestMeshIdentityRemediationForGithubSourceUsesLocalCheckout(t *testing.T) {
	got := meshIdentityRemediation(&resolvedCluster{
		Source:  inventory.SourceGithubRepoFlag,
		Cluster: "production",
	})
	if !strings.Contains(got, "--gitops-dir <checkout> --cluster production") {
		t.Fatalf("expected local checkout remediation for github source, got %q", got)
	}
}

func testCobraCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
