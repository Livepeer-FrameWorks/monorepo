package templates

import (
	"path/filepath"
	"regexp"
	"testing"
)

// ephemeralPortRangeValue matches the value of every ip_local_port_range
// setting in compose YAML, sysctl -w, sysctl.d and Ansible defaults syntax.
var ephemeralPortRangeValue = regexp.MustCompile(`ip_local_port_range"?\s*[:=]\s*"?(\d+)\s+(\d+)`)

// TestEphemeralPortRangeIsKernelDefault asserts every surface that sets the
// ephemeral port range writes the kernel default 32768-60999, which keeps
// automatic source ports clear of the platform's listen ports below 32768.
func TestEphemeralPortRangeIsKernelDefault(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	vars := EdgeVars{
		NodeID:          "range-node",
		EdgeDomain:      "edge.range.test",
		SiteAddress:     "edge.range.test",
		AcmeEmail:       "ops@range.test",
		FoghornGRPCAddr: "foghorn.range.test:18008",
		EnrollmentToken: "range-token",
		Mode:            "container",
		// Linux hosts get the range from node_tuning; the darwin compose sets
		// it inside Docker Desktop's VM.
		EdgeOS: "darwin",
	}
	if err := WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		t.Fatalf("WriteEdgeTemplates: %v", err)
	}

	sources := map[string]string{
		"rendered darwin docker-compose.edge.yml": readFile(t, filepath.Join(tmpDir, "docker-compose.edge.yml")),
		"edge role compose.yml.j2":                readFile(t, ansibleTemplatePath(t, "compose.yml.j2")),
		"node_tuning defaults":                    readFile(t, repositoryPath(t, "ansible/collections/ansible_collections/frameworks/infra/roles/node_tuning/defaults/main.yml")),
		"cli edge tune":                           readFile(t, repositoryPath(t, "cli/cmd/edge.go")),
	}
	for name, content := range sources {
		matches := ephemeralPortRangeValue.FindAllStringSubmatch(content, -1)
		if len(matches) == 0 {
			t.Errorf("%s: sets no ip_local_port_range", name)
			continue
		}
		for _, m := range matches {
			if m[1] != "32768" || m[2] != "60999" {
				t.Errorf("%s: ip_local_port_range = %s %s, want 32768 60999", name, m[1], m[2])
			}
		}
	}
}
