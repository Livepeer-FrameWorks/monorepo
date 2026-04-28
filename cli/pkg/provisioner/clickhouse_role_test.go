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
