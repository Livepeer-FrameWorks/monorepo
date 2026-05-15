package provisioner

import (
	"context"
	"strings"
	"testing"
)

func TestNodeBaselineRoleInstallsOperatorTooling(t *testing.T) {
	defaults := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/node_baseline/defaults/main.yml")
	tasks := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/node_baseline/tasks/main.yml")
	playbook := readRepoFile(t, "ansible/playbooks/node_baseline.yml")

	for _, want := range []string{
		"Debian:",
		"RedHat:",
		"Archlinux:",
		"Alpine:",
		"netcat-openbsd",
		"nmap-ncat",
		"openbsd-netcat",
		"jq",
		"tcpdump",
		"strace",
	} {
		if !strings.Contains(defaults, want) {
			t.Fatalf("node_baseline defaults missing %q:\n%s", want, defaults)
		}
	}
	for _, want := range []string{
		"Node_baseline | refresh apt package cache",
		"ansible.builtin.package:",
		"node_baseline_resolved_packages | unique | list",
		"skip unsupported package family",
	} {
		if !strings.Contains(tasks, want) {
			t.Fatalf("node_baseline tasks missing %q:\n%s", want, tasks)
		}
	}
	if !strings.Contains(playbook, "frameworks.infra.node_baseline") {
		t.Fatalf("node_baseline playbook does not include role:\n%s", playbook)
	}
}

func TestNodeBaselineRoleVarsForwardExtraPackages(t *testing.T) {
	vars, err := nodeBaselineRoleVars(context.Background(), nilHost(), ServiceConfig{
		Metadata: map[string]any{"extra_packages": []string{"mtr", "htop"}},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("node baseline vars: %v", err)
	}
	got, ok := vars["node_baseline_extra_packages"].([]string)
	if !ok || len(got) != 2 || got[0] != "mtr" || got[1] != "htop" {
		t.Fatalf("node_baseline_extra_packages = %#v", vars["node_baseline_extra_packages"])
	}
}
