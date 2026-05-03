package provisioner

import (
	"context"
	"strings"
	"testing"
)

func TestPostgresRoleSuppliesArchVarsMissingFromGalaxyRole(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/postgres/tasks/install.yml")
	for _, want := range []string{
		"Postgres | define Arch variables missing from geerlingguy.postgresql loader",
		`when: ansible_facts.os_family == "Archlinux"`,
		"postgresql_packages:",
		"- postgresql",
		"postgresql_daemon: postgresql",
		"Postgres | refresh Arch package database before dependency install",
		"community.general.pacman:",
		"update_cache: true",
		"postgres_arch_pgroot + '/data'",
		"Postgres | redirect Arch PostgreSQL unit to managed PGROOT",
		"Environment=PGROOT={{ postgres_arch_pgroot }}",
		"PIDFile={{ postgres_arch_pgroot }}/data/postmaster.pid",
		"'/usr/sbin' if ansible_facts.os_family == 'Archlinux' else",
		"postgresql_data_dir: \"{{ frameworks_postgres_data_dir }}\"",
		"postgresql_config_path: \"{{ frameworks_postgres_config_dir }}\"",
		"postgresql_bin_path: \"{{ frameworks_postgres_bin_path }}\"",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("postgres role should supply Arch-specific geerlingguy vars; missing %q:\n%s", want, content)
		}
	}
}

func TestPostgresRoleAllowsDockerBridgeClients(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/postgres/defaults/main.yml")
	for _, want := range []string{
		"Docker bridge networks used by colocated compose apps",
		`address: "172.16.0.0/12"`,
		"auth_method: scram-sha-256",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("postgres role should allow Docker bridge clients with password auth; missing %q:\n%s", want, content)
		}
	}
}

func TestPostgresRoleVarsPassesStableInstanceName(t *testing.T) {
	vars, err := postgresRoleVars(context.Background(), nilHost(), ServiceConfig{
		DeployName: "postgres-support",
		Port:       5432,
		Metadata: map[string]any{
			"postgres_password": "secret",
		},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("postgresRoleVars: %v", err)
	}
	if got := vars["postgres_instance_name"]; got != "postgres-support" {
		t.Fatalf("postgres_instance_name = %v, want postgres-support", got)
	}
}

func TestSanitizePostgresInstanceName(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{in: "Postgres Support", want: "postgres-support"},
		{in: "postgres_support!!", want: "postgres-support"},
		{in: "///", want: "postgres"},
	} {
		if got := sanitizePostgresInstanceName(tc.in); got != tc.want {
			t.Fatalf("sanitizePostgresInstanceName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
