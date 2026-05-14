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

func TestRedisNamedInstanceDisablesProtectedModeWhenPasswordlessAndNonLoopback(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/redis/templates/instance.conf.j2")
	for _, want := range []string{
		"redis_loopback_only",
		"redis_password | length > 0",
		"protected-mode {{ 'yes' if (redis_password | length > 0) or redis_loopback_only else 'no' }}",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("redis named instance template should manage protected mode for internal non-loopback binds; missing %q:\n%s", want, content)
		}
	}
}

func TestPostgresRoleCreatesRequestedDatabaseExtensions(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/postgres/tasks/init.yml")
	for _, want := range []string{
		"Create database extensions",
		"community.postgresql.postgresql_ext:",
		`db: "{{ item.0.name }}"`,
		`name: "{{ item.1 }}"`,
		"subelements('extensions', skip_missing=true)",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("postgres init should create requested extensions as postgres; missing %q:\n%s", want, content)
		}
	}
}

func TestPostgresRoleCreatesOwnerRolesWithPerDatabasePassword(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/postgres/tasks/init.yml")
	want := `password: "{{ item.password | default(postgres_admin_password) }}"`
	if !strings.Contains(content, want) {
		t.Fatalf("postgres init should use per-database owner passwords when provided; missing %q:\n%s", want, content)
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

func TestPostgresRoleVarsRequestsChatwootExtensions(t *testing.T) {
	vars, err := postgresRoleVars(context.Background(), nilHost(), ServiceConfig{
		Metadata: map[string]any{
			"postgres_password": "secret",
			"databases": []map[string]string{
				{"name": "chatwoot", "owner": "chatwoot"},
			},
		},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("postgresRoleVars: %v", err)
	}
	dbs, ok := vars["postgres_databases"].([]map[string]any)
	if !ok || len(dbs) != 1 {
		t.Fatalf("postgres_databases = %#v, want one database", vars["postgres_databases"])
	}
	extensions, ok := dbs[0]["extensions"].([]string)
	if !ok || len(extensions) != 1 || extensions[0] != "pg_stat_statements" {
		t.Fatalf("chatwoot extensions = %#v, want [pg_stat_statements]", dbs[0]["extensions"])
	}
}

func TestPostgresRoleVarsPassesDatabaseOwnerPassword(t *testing.T) {
	vars, err := postgresRoleVars(context.Background(), nilHost(), ServiceConfig{
		Metadata: map[string]any{
			"postgres_password": "admin-secret",
			"databases": []map[string]string{
				{"name": "foghorn_eu", "owner": "foghorn_eu", "password": "owner-secret"},
			},
		},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("postgresRoleVars: %v", err)
	}
	dbs, ok := vars["postgres_databases"].([]map[string]any)
	if !ok || len(dbs) != 1 {
		t.Fatalf("postgres_databases = %#v, want one database", vars["postgres_databases"])
	}
	if got := dbs[0]["password"]; got != "owner-secret" {
		t.Fatalf("database password = %v, want owner-secret", got)
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
