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

// A DHCP or RA search domain turns a dropped upstream query into a lookup of
// the name under that suffix, which a wildcard record answers wrongly. Every
// host path (node_baseline on cluster hosts, the edge role on standalone
// edges) writes the networkd drop-in that ignores those domains.
// Standalone `edge provision` does not run node_baseline; without the journald
// drop-in an edge's journal grows to journald's default cap of 10% of the
// filesystem (up to 4G) next to the cache it serves from.
func TestEdgeRoleCapsTheJournal(t *testing.T) {
	task := roleTaskByName(t, "ansible/collections/ansible_collections/frameworks/infra/roles/edge/tasks/main.yml",
		"Edge | journald retention")
	spec, ok := moduleSpec(task, "include_role")
	if !ok || spec["name"] != "frameworks.infra.node_baseline" || spec["tasks_from"] != "journald.yml" {
		t.Fatalf("edge role must apply node_baseline's journald.yml: %v", task)
	}
	when := strings.Join(func() []string {
		var out []string
		for _, w := range task["when"].([]any) {
			out = append(out, w.(string))
		}
		return out
	}(), "\n")
	for _, want := range []string{"ansible_facts.os_family != 'Darwin'", "ansible_facts.service_mgr == 'systemd'"} {
		if !strings.Contains(when, want) {
			t.Errorf("edge journald task must be gated on %q: %v", want, task["when"])
		}
	}
}

func TestNodeBaselineIgnoresDHCPAndRASearchDomains(t *testing.T) {
	const role = "ansible/collections/ansible_collections/frameworks/infra/roles/node_baseline/"
	main := readRepoFile(t, role+"tasks/main.yml")
	tasks := readRepoFile(t, role+"tasks/dns_search.yml")
	dropin := readRepoFile(t, role+"templates/networkd_no_search_domains.conf.j2")
	handlers := readRepoFile(t, role+"handlers/main.yml")
	vars := readRepoFile(t, role+"vars/main.yml")
	edge := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/edge/tasks/main.yml")

	if !strings.Contains(main, "ansible.builtin.import_tasks: dns_search.yml") {
		t.Errorf("node_baseline main.yml does not import dns_search.yml:\n%s", main)
	}
	for _, section := range []string{"[DHCPv4]", "[DHCPv6]", "[IPv6AcceptRA]"} {
		if !strings.Contains(dropin, section+"\nUseDomains=no\n") {
			t.Errorf("networkd drop-in must set UseDomains=no under %s:\n%s", section, dropin)
		}
	}
	for _, want := range []string{
		"systemctl, is-active, systemd-networkd.service",
		"networkctl list --no-legend --no-pager",
		"loopback|wireguard) continue",
		"Network File:",
		"/etc/systemd/network /run/systemd/network",
		`dest: "/etc/systemd/network/{{ item }}.d/{{ node_baseline_networkd_dropin_name }}"`,
		"notify: node_baseline networkd reload",
	} {
		if !strings.Contains(tasks, want) {
			t.Errorf("dns_search.yml missing %q:\n%s", want, tasks)
		}
	}
	if !strings.Contains(vars, "node_baseline_networkd_dropin_name: 90-frameworks-no-search-domains.conf") {
		t.Errorf("node_baseline vars must name the networkd drop-in:\n%s", vars)
	}
	for _, want := range []string{"listen: node_baseline networkd reload", "argv: [networkctl, reload]", "when: not ansible_check_mode"} {
		if !strings.Contains(handlers, want) {
			t.Errorf("node_baseline handlers missing %q:\n%s", want, handlers)
		}
	}
	// Privateer's wg0 route is set at runtime with resolvectl; the policy
	// must not reach into resolved's link settings itself.
	for _, forbidden := range []string{"[resolvectl", "resolvectl domain", "resolvectl revert", "resolved.conf"} {
		if strings.Contains(tasks, forbidden) {
			t.Errorf("dns_search.yml must leave resolved settings to networkd; found %q:\n%s", forbidden, tasks)
		}
	}

	start := strings.Index(edge, "- name: Edge | ignore DHCP and RA search domains")
	install := strings.Index(edge, "- name: Edge | install (container)")
	if start < 0 || install < 0 || start > install {
		t.Fatalf("edge role must apply the search domain policy before installing:\n%s", edge)
	}
	for _, want := range []string{
		"name: frameworks.infra.node_baseline",
		"tasks_from: dns_search.yml",
		"ansible_facts.os_family != 'Darwin'",
		"tags: [install, configure]",
	} {
		if !strings.Contains(edge[start:install], want) {
			t.Errorf("edge search domain task missing %q:\n%s", want, edge[start:install])
		}
	}
}
