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

func TestNodeBaselineRoleBoundsLogsAndHardensSSH(t *testing.T) {
	const role = "ansible/collections/ansible_collections/frameworks/infra/roles/node_baseline/"
	vars := readRepoFile(t, role+"vars/main.yml")
	journald := readRepoFile(t, role+"templates/journald_frameworks.conf.j2")
	sshd := readRepoFile(t, role+"templates/sshd_frameworks.conf.j2")
	fail2ban := readRepoFile(t, role+"templates/fail2ban_sshd.local.j2")
	sshdTasks := readRepoFile(t, role+"tasks/sshd.yml")
	journaldTasks := readRepoFile(t, role+"tasks/journald.yml")
	main := readRepoFile(t, role+"tasks/main.yml")

	for _, family := range []string{"Debian:", "RedHat:", "Archlinux:", "Alpine:"} {
		line := ""
		for l := range strings.SplitSeq(vars, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), family) && strings.Contains(l, "[") {
				line = l
			}
		}
		if !strings.Contains(line, "logrotate") || !strings.Contains(line, "fail2ban") {
			t.Errorf("node_baseline host packages for %s lack logrotate or fail2ban: %q", family, line)
		}
	}
	for _, want := range []string{"node_baseline_journald_max_use: 2G", "node_baseline_journald_keep_free_percent: 10"} {
		if !strings.Contains(vars, want) {
			t.Errorf("node_baseline vars missing %q", want)
		}
	}
	for _, want := range []string{"[Journal]", "SystemMaxUse={{ node_baseline_journald_max_use }}", "SystemKeepFree={{ node_baseline_journald_keep_free_mb }}M"} {
		if !strings.Contains(journald, want) {
			t.Errorf("journald drop-in missing %q:\n%s", want, journald)
		}
	}
	if !strings.Contains(journaldTasks, "dest: /etc/systemd/journald.conf.d/frameworks.conf") {
		t.Errorf("journald drop-in not written to journald.conf.d/frameworks.conf:\n%s", journaldTasks)
	}
	for _, want := range []string{
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"PermitRootLogin prohibit-password",
		"MaxStartups 10:30:60",
		"LoginGraceTime 20",
	} {
		if !strings.Contains(sshd, want+"\n") {
			t.Errorf("sshd drop-in missing %q:\n%s", want, sshd)
		}
	}
	// A render sshd rejects must never reach the daemon: every write is
	// validated, the complete config is validated before reload, and the
	// rescue restores the previous files.
	for _, want := range []string{
		`validate: "{{ node_baseline_sshd_binary }} -t -f %s"`,
		`argv: ["{{ node_baseline_sshd_binary }}", -t]`,
		"rescue:",
		"restore the previous sshd drop-in",
		"restore the previous sshd main config",
		"state: reloaded",
	} {
		if !strings.Contains(sshdTasks, want) {
			t.Errorf("sshd tasks missing %q", want)
		}
	}
	if strings.Index(sshdTasks, "rescue:") > strings.Index(sshdTasks, "state: reloaded") {
		t.Errorf("sshd reload must follow the validated block")
	}
	for _, want := range []string{"[sshd]", "enabled = true", "backend = systemd"} {
		if !strings.Contains(fail2ban, want) {
			t.Errorf("fail2ban jail missing %q:\n%s", want, fail2ban)
		}
	}
	for _, want := range []string{"host_packages.yml", "journald.yml", "logrotate.yml", "sshd.yml", "fail2ban.yml"} {
		if !strings.Contains(main, want) {
			t.Errorf("node_baseline main.yml does not import %s", want)
		}
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
