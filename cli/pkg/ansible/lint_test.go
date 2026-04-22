package ansible

import (
	"strings"
	"testing"
)

func TestLintPlaybook_passesOnFullyDeclarativePlaybook(t *testing.T) {
	t.Parallel()
	pb := &Playbook{
		Name:  "valid",
		Hosts: "host",
		Plays: []Play{{
			Name:  "valid play",
			Hosts: "host",
			Tasks: []Task{
				TaskPackage("curl", PackagePresent),
				TaskGetURL("https://example.com/x.tgz", "/tmp/x.tgz", "sha256:abc"),
				TaskUnarchive("/tmp/x.tgz", "/opt/x", "/opt/x/bin/x", UnarchiveOpts{StripComponents: 1}),
				TaskCopy("/etc/x.conf", "key=value\n", CopyOpts{Mode: "0644"}),
				TaskSystemdService("x", SystemdOpts{State: "started", Enabled: BoolPtr(true)}),
			},
		}},
	}
	issues := LintPlaybook(pb)
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d:\n%s", len(issues), formatIssues(issues))
	}
}

func TestLintPlaybook_flagsBareShellWithoutGuard(t *testing.T) {
	t.Parallel()
	pb := &Playbook{
		Plays: []Play{{
			Name: "bad play",
			Tasks: []Task{
				{
					Name:   "raw shell with no idempotence marker",
					Module: "ansible.builtin.shell",
					Args:   map[string]any{"cmd": "echo hi"},
				},
			},
		}},
	}
	issues := LintPlaybook(pb)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d:\n%s", len(issues), formatIssues(issues))
	}
	if issues[0].Rule != "shell-needs-idempotence-marker" {
		t.Errorf("wrong rule: %s", issues[0].Rule)
	}
}

func TestLintPlaybook_acceptsShellWithCreates(t *testing.T) {
	t.Parallel()
	pb := &Playbook{
		Plays: []Play{{
			Tasks: []Task{
				{
					Name:   "shell with creates gate",
					Module: "ansible.builtin.shell",
					Args:   map[string]any{"cmd": "make install", "creates": "/opt/x/bin/x"},
				},
			},
		}},
	}
	if got := LintPlaybook(pb); len(got) != 0 {
		t.Errorf("creates= should satisfy guardrail, got %d issues:\n%s", len(got), formatIssues(got))
	}
}

func TestLintPlaybook_acceptsShellWithChangedWhen(t *testing.T) {
	t.Parallel()
	pb := &Playbook{
		Plays: []Play{{
			Tasks: []Task{
				{
					Name:        "shell that's always-OK",
					Module:      "ansible.builtin.shell",
					Args:        map[string]any{"cmd": "sysctl --system"},
					ChangedWhen: "false",
				},
			},
		}},
	}
	if got := LintPlaybook(pb); len(got) != 0 {
		t.Errorf("changed_when= should satisfy guardrail, got %d issues:\n%s", len(got), formatIssues(got))
	}
}

func TestLintPlaybook_flagsUnnamedTask(t *testing.T) {
	t.Parallel()
	pb := &Playbook{
		Plays: []Play{{
			Name: "play with unnamed task",
			Tasks: []Task{
				{Module: "ansible.builtin.package", Args: map[string]any{"name": "curl", "state": "present"}},
			},
		}},
	}
	issues := LintPlaybook(pb)
	if len(issues) != 1 || issues[0].Rule != "task-needs-name" {
		t.Errorf("expected 1 task-needs-name issue, got %d:\n%s", len(issues), formatIssues(issues))
	}
}

func TestLintPlaybook_flagsUnarchiveWithoutCreates(t *testing.T) {
	t.Parallel()
	pb := &Playbook{
		Plays: []Play{{
			Name: "unsafe unarchive",
			Tasks: []Task{
				{
					Name:   "extract thing",
					Module: "ansible.builtin.unarchive",
					Args:   map[string]any{"src": "/tmp/x.tgz", "dest": "/opt/x", "remote_src": true},
				},
			},
		}},
	}
	issues := LintPlaybook(pb)
	if len(issues) != 1 || issues[0].Rule != "unarchive-needs-creates" {
		t.Errorf("expected 1 unarchive-needs-creates issue, got %d:\n%s", len(issues), formatIssues(issues))
	}
}

func TestLintPlaybook_acceptsUnarchiveWithCreates(t *testing.T) {
	t.Parallel()
	pb := &Playbook{
		Plays: []Play{{
			Tasks: []Task{
				{
					Name:   "extract thing",
					Module: "ansible.builtin.unarchive",
					Args: map[string]any{
						"src":        "/tmp/x.tgz",
						"dest":       "/opt/x",
						"remote_src": true,
						"creates":    "/opt/x/bin/x",
					},
				},
			},
		}},
	}
	if got := LintPlaybook(pb); len(got) != 0 {
		t.Errorf("creates= should satisfy unarchive guard, got %d issues:\n%s", len(got), formatIssues(got))
	}
}

func TestLintPlaybook_acceptsShellWithWhen(t *testing.T) {
	t.Parallel()
	pb := &Playbook{
		Plays: []Play{{
			Tasks: []Task{
				{
					Name:   "shell gated on Ansible fact",
					Module: "ansible.builtin.shell",
					Args:   map[string]any{"cmd": "echo arch-only"},
					When:   "ansible_facts.os_family == 'Archlinux'",
				},
			},
		}},
	}
	if got := LintPlaybook(pb); len(got) != 0 {
		t.Errorf("when= should satisfy guardrail, got %d issues:\n%s", len(got), formatIssues(got))
	}
}

// Lock in that every generator we've migrated produces a clean playbook.
// New migrations should add themselves here so the regression net stays tight.
func TestLintPlaybook_migratedGenerators(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		pb   *Playbook
	}{
		{
			name: "kafka-kraft",
			pb: GenerateKafkaKRaftPlaybook("4.2.0", 1, "10.0.0.1", 9092, 9093, "1@10.0.0.1:9093",
				"test-cluster", nil, "https://example.com/k.tgz", "sha512:abc",
				DistroPackageSpec{PackageName: "default-jre-headless"}),
		},
		{
			name: "kafka-controller",
			pb: GenerateKafkaControllerPlaybook("4.2.0", 100, "10.0.0.1", 9093, "10.0.0.1:9092",
				"test-cluster", "100@10.0.0.1:9093:abc",
				"https://example.com/k.tgz", "sha512:abc",
				DistroPackageSpec{PackageName: "default-jre-headless"}),
		},
		{
			name: "kafka-broker",
			pb: GenerateKafkaBrokerPlaybook("4.2.0", 1, "10.0.0.1", 9092, "10.0.0.1:9093:abc",
				"test-cluster", nil, "https://example.com/k.tgz", "sha512:abc",
				DistroPackageSpec{PackageName: "default-jre-headless"}),
		},
		{
			name: "postgres-debian-pkg",
			pb: GeneratePostgresPlaybook("10.0.0.1", PostgresInstallParams{
				DistroFamily: "debian",
				Version:      "",
				Databases:    []string{"app"},
			}),
		},
		{
			name: "postgres-rhel-source-build",
			pb: GeneratePostgresPlaybook("10.0.0.1", PostgresInstallParams{
				DistroFamily:     "rhel",
				Version:          "15.14",
				ArtifactURL:      "https://example.com/pg.tar.bz2",
				ArtifactChecksum: "sha256:abc",
				Databases:        []string{"app"},
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			issues := LintPlaybook(tc.pb)
			if len(issues) != 0 {
				t.Errorf("%s playbook produced lint issues:\n%s", tc.name, formatIssues(issues))
			}
		})
	}
}

func formatIssues(issues []LintIssue) string {
	parts := make([]string, 0, len(issues))
	for _, i := range issues {
		parts = append(parts, "  "+i.Error())
	}
	return strings.Join(parts, "\n")
}
