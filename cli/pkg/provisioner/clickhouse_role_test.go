package provisioner

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestClickHouseRoleUsesScopedDeb822Repository(t *testing.T) {
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install-debian.yml")
	for _, want := range []string{
		"ansible.builtin.deb822_repository",
		"https://packages.clickhouse.com/deb",
		"signed_by: \"{{ clickhouse_repository_key_url }}\"",
		"python3-debian",
		"allow_downgrades: true",
	} {
		if !strings.Contains(install, want) {
			t.Fatalf("ClickHouse Debian installer missing %q:\n%s", want, install)
		}
	}
	for _, forbidden := range []string{
		"ansible.builtin.apt_key",
		"ansible.builtin.apt_repository",
	} {
		if strings.Contains(install, forbidden) {
			t.Fatalf("ClickHouse Debian installer still uses %q:\n%s", forbidden, install)
		}
	}

	wrapper := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml")
	if strings.Contains(wrapper, "tasks_from: install-Debian.yml") {
		t.Fatalf("ClickHouse wrapper still imports the legacy upstream installer:\n%s", wrapper)
	}
	if strings.Contains(wrapper, "ansible.builtin.apt:\n        name: clickhouse-keeper") {
		t.Fatalf("ClickHouse wrapper installs the Keeper package that conflicts with clickhouse-server:\n%s", wrapper)
	}
	if !strings.Contains(install, "/lib/systemd/system/clickhouse-server.service") {
		t.Fatalf("ClickHouse Debian installer does not verify the server unit:\n%s", install)
	}

	molecule := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/molecule/default/molecule.yml")
	for _, image := range []string{
		"geerlingguy/docker-ubuntu2404-ansible:latest",
		"geerlingguy/docker-ubuntu2604-ansible:latest",
	} {
		if !strings.Contains(molecule, image) {
			t.Fatalf("ClickHouse Molecule scenario missing %q:\n%s", image, molecule)
		}
	}
}

func TestClickHouseRoleVarsUsesSharedCredentials(t *testing.T) {
	config := ServiceConfig{
		Version: "24.8.9.95",
		Port:    9000,
		Metadata: map[string]any{
			"clickhouse_password":          "writer-pass",
			"clickhouse_readonly_password": "reader-pass",
			"databases":                    []string{"periscope"},
		},
	}

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}

	if got := vars["clickhouse_default_password"]; got != "writer-pass" {
		t.Fatalf("clickhouse_default_password = %v, want writer-pass", got)
	}
	if got := vars["clickhouse_readonly_password"]; got != "reader-pass" {
		t.Fatalf("clickhouse_readonly_password = %v, want reader-pass", got)
	}
	dbs, ok := vars["clickhouse_databases"].([]string)
	if !ok || len(dbs) != 1 || dbs[0] != "periscope" {
		t.Fatalf("clickhouse_databases = %#v, want [periscope]", vars["clickhouse_databases"])
	}
}

func TestClickHouseRoleVarsPassesNamedCollections(t *testing.T) {
	collections := []map[string]any{
		{
			"name": "quartermaster_pg",
			"settings": map[string]any{
				"host":     "10.66.0.10",
				"port":     5432,
				"database": "quartermaster",
				"user":     "frameworks_analytics_ro",
				"password": "secret",
			},
		},
	}
	config := ServiceConfig{
		Version: "26.3.10.62",
		Metadata: map[string]any{
			"named_collections":             collections,
			"clickhouse_analytics_password": "metabase-secret",
		},
	}

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	got, ok := vars["clickhouse_named_collections"].([]map[string]any)
	if !ok || len(got) != 1 || got[0]["name"] != "quartermaster_pg" {
		t.Fatalf("clickhouse_named_collections = %#v, want the quartermaster_pg collection", vars["clickhouse_named_collections"])
	}
	if vars["clickhouse_analytics_password"] != "metabase-secret" {
		t.Fatalf("clickhouse_analytics_password not passed through: %#v", vars["clickhouse_analytics_password"])
	}

	// Absent metadata must leave the var unset so the ansible default ([])
	// removes a previously managed drop-in.
	vars, err = clickhouseRoleVars(context.Background(), inventory.Host{}, ServiceConfig{Version: "26.3.10.62", Metadata: map[string]any{}}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	if _, ok := vars["clickhouse_named_collections"]; ok {
		t.Fatalf("clickhouse_named_collections should be unset without metadata")
	}
}

func TestClickHouseRoleVarsResolvesVersionFromReleaseManifest(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
infrastructure:
  - name: clickhouse
    version: "26.3.10.62"
    image: clickhouse/clickhouse-server:26.3.10.62
    digest: sha256:clickhousedigest
`)

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, ServiceConfig{
		Version: "stable",
		Metadata: map[string]any{
			"gitops_repository": repo,
			"platform_channel":  "stable",
		},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	if got := vars["clickhouse_version"]; got != "26.3.10.62" {
		t.Fatalf("clickhouse_version = %v, want 26.3.10.62", got)
	}
}

func TestClickHouseRoleVarsDefaultsListenHostsToLocalAndMesh(t *testing.T) {
	config := ServiceConfig{
		Version: "26.3.10.62",
		Metadata: map[string]any{
			"advertised_host": "10.66.0.12",
		},
	}

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	listenHosts, ok := vars["clickhouse_listen_hosts"].([]string)
	if !ok {
		t.Fatalf("clickhouse_listen_hosts = %#v, want []string", vars["clickhouse_listen_hosts"])
	}
	want := []string{"127.0.0.1", "10.66.0.12"}
	if len(listenHosts) != len(want) {
		t.Fatalf("clickhouse_listen_hosts = %#v, want %#v", listenHosts, want)
	}
	for i := range want {
		if listenHosts[i] != want[i] {
			t.Fatalf("clickhouse_listen_hosts = %#v, want %#v", listenHosts, want)
		}
	}
}

func TestClickHouseRoleVarsRespectsExplicitListenHost(t *testing.T) {
	config := ServiceConfig{
		Version: "26.3.10.62",
		Metadata: map[string]any{
			"advertised_host": "10.66.0.12",
			"listen_host":     "::",
		},
	}

	vars, err := clickhouseRoleVars(context.Background(), inventory.Host{}, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("clickhouseRoleVars: %v", err)
	}
	if got := vars["clickhouse_listen_host"]; got != "::" {
		t.Fatalf("clickhouse_listen_host = %v, want ::", got)
	}
	if _, ok := vars["clickhouse_listen_hosts"]; ok {
		t.Fatalf("clickhouse_listen_hosts should not be set when listen_host is explicit")
	}
}
