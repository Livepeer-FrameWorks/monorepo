package ansiblerun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestInventoryRenderer_WritesInventoryAndGroupVars(t *testing.T) {
	dir := t.TempDir()
	inv := Inventory{
		Hosts: []Host{
			{
				Name:       "central-1",
				Address:    "10.0.0.1",
				User:       "ubuntu",
				Port:       22,
				PrivateKey: "/keys/central.pem",
				Vars: map[string]any{
					"datacenter": "us-east-1",
				},
			},
			{
				Name:       "edge-1",
				Address:    "10.0.1.1",
				User:       "deploy",
				Connection: "ssh",
			},
		},
		Groups: []Group{
			{Name: "control", Hosts: []string{"central-1"}},
			{Name: "edge", Hosts: []string{"edge-1"}, Vars: map[string]any{"cluster_role": "ingest"}},
		},
		GlobalVars: map[string]any{"frameworks_version": "0.1.0"},
	}

	r := &InventoryRenderer{}
	invPath, err := r.Render(dir, inv)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if filepath.Dir(invPath) != dir {
		t.Fatalf("inventory path %s not under %s", invPath, dir)
	}

	raw, err := os.ReadFile(invPath)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("inventory not valid YAML: %v\n%s", err, raw)
	}

	all, ok := parsed["all"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level 'all' map, got %T", parsed["all"])
	}
	hosts, _ := all["hosts"].(map[string]any)
	if _, ok := hosts["central-1"]; !ok {
		t.Errorf("central-1 missing from all.hosts")
	}
	if _, ok := hosts["edge-1"]; !ok {
		t.Errorf("edge-1 missing from all.hosts")
	}
	children, _ := all["children"].(map[string]any)
	if _, ok := children["control"]; !ok {
		t.Errorf("control group missing")
	}
	if _, ok := children["edge"]; !ok {
		t.Errorf("edge group missing")
	}

	allVars, err := os.ReadFile(filepath.Join(dir, "group_vars", "all.yml"))
	if err != nil {
		t.Fatalf("group_vars/all.yml missing: %v", err)
	}
	if !strings.Contains(string(allVars), "frameworks_version") {
		t.Errorf("all.yml missing frameworks_version: %s", allVars)
	}

	edgeVars, err := os.ReadFile(filepath.Join(dir, "group_vars", "edge.yml"))
	if err != nil {
		t.Fatalf("group_vars/edge.yml missing: %v", err)
	}
	if !strings.Contains(string(edgeVars), "cluster_role") {
		t.Errorf("edge.yml missing cluster_role: %s", edgeVars)
	}

	if _, err := os.Stat(filepath.Join(dir, "group_vars", "control.yml")); !os.IsNotExist(err) {
		t.Errorf("control.yml should not exist (group has no Vars)")
	}
}

func TestInventoryRenderer_RejectsEmptyHosts(t *testing.T) {
	r := &InventoryRenderer{}
	_, err := r.Render(t.TempDir(), Inventory{})
	if err == nil {
		t.Fatal("expected error on empty Hosts")
	}
}

func TestInventoryRenderer_RejectsMissingAddress(t *testing.T) {
	r := &InventoryRenderer{}
	_, err := r.Render(t.TempDir(), Inventory{
		Hosts: []Host{{Name: "h1"}},
	})
	if err == nil {
		t.Fatal("expected error on host without Address")
	}
}
