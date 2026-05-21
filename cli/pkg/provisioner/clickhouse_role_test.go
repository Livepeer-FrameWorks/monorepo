package provisioner

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
)

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
