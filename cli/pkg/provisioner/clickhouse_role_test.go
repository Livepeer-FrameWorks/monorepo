package provisioner

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"

	"gopkg.in/yaml.v3"
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

// idealista.clickhouse defaults to trace level and ten 1000M files, which
// fills a data host's log disk within days.
func TestClickHouseRoleBoundsTheServerLog(t *testing.T) {
	var tasks []struct {
		Name string         `yaml:"name"`
		Vars map[string]any `yaml:"vars"`
	}
	install := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml")
	if err := yaml.Unmarshal([]byte(install), &tasks); err != nil {
		t.Fatalf("parse install.yml: %v", err)
	}
	for _, task := range tasks {
		if task.Name != "Configure ClickHouse via idealista.clickhouse" {
			continue
		}
		logger, ok := task.Vars["clickhouse_logger"].(map[string]any)
		if !ok {
			t.Fatalf("idealista.clickhouse import does not override clickhouse_logger: %v", task.Vars)
		}
		if logger["level"] != "information" || logger["size"] != "200M" || logger["count"] != 10 {
			t.Fatalf("clickhouse_logger = %v, want information, 200M, 10", logger)
		}
		for _, key := range []string{"log", "errorlog", "console"} {
			if _, ok := logger[key]; !ok {
				t.Fatalf("clickhouse_logger replaces the upstream map and must set %s: %v", key, logger)
			}
		}
		return
	}
	t.Fatal("install.yml no longer imports idealista.clickhouse config")
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
platform_version: v9.9.9
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

// idealista.clickhouse appends "(event_date)" to the part_log and
// query_thread_log partition_by values while its defaults already read
// toYYYYMM(event_date); ClickHouse then rejects toYYYYMM(event_date)(event_date)
// on every system.part_log flush and writes the stack trace to the error log.
func TestClickHouseRoleOverridesBrokenSystemLogPartitionKeys(t *testing.T) {
	task := roleTaskByName(t, "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml",
		"Write managed ClickHouse system log partition keys")
	spec, ok := moduleSpec(task, "copy")
	if !ok {
		t.Fatalf("system log partition override is not a copy task: %v", task)
	}
	if spec["dest"] != "/etc/clickhouse-server/config.d/system-log-partitions.xml" {
		t.Fatalf("override must be a config.d drop-in: %v", spec["dest"])
	}
	content, _ := spec["content"].(string)
	for _, table := range []string{"part_log", "query_thread_log"} {
		want := "<" + table + ">\n    <partition_by>toYYYYMM(event_date)</partition_by>\n  </" + table + ">"
		if !strings.Contains(content, want) {
			t.Errorf("drop-in does not set %s partition_by to toYYYYMM(event_date):\n%s", table, content)
		}
	}
	if task["notify"] != "clickhouse restart" {
		t.Errorf("system log tables are created at startup; the drop-in must notify clickhouse restart: %v", task["notify"])
	}
}

// idealista.clickhouse re-renders config.xml with its openSSL block on every
// run; stripping the block from config.xml afterwards changed the file, and
// restarted ClickHouse, on every apply. A config.d drop-in removes the block
// from the merged configuration instead and leaves config.xml as rendered.
func TestClickHouseRoleRemovesOpenSSLThroughADropIn(t *testing.T) {
	const file = "ansible/collections/ansible_collections/frameworks/infra/roles/clickhouse/tasks/install.yml"
	var tasks []any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, file)), &tasks); err != nil {
		t.Fatal(err)
	}
	walkRoleBlocks(tasks, nil, func(task map[string]any, _ []map[string]any) {
		if spec, ok := moduleSpec(task, "replace"); ok && stringValue(spec["path"]) == "/etc/clickhouse-server/config.xml" {
			t.Errorf("%q edits the config.xml idealista.clickhouse renders", task["name"])
		}
	})
	disabled := roleTaskByName(t, file, "Remove the openSSL block through a drop-in when ClickHouse TLS is disabled")
	spec, _ := moduleSpec(disabled, "copy")
	if spec["dest"] != "/etc/clickhouse-server/config.d/no-openssl.xml" || !strings.Contains(stringValue(spec["content"]), `<openSSL remove="remove"/>`) {
		t.Fatalf("drop-in must remove openSSL from the merged config: %v", spec)
	}
	enabled := roleTaskByName(t, file, "Remove the openSSL removal drop-in when ClickHouse TLS is enabled")
	if spec, _ := moduleSpec(enabled, "file"); spec["state"] != "absent" {
		t.Fatalf("TLS-enabled hosts must keep openSSL: %v", spec)
	}
}
