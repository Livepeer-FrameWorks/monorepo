package provisioner

import (
	"context"
	"strings"
	"testing"
)

func TestYugabyteRoleVarsPassesDatabaseOwnerPassword(t *testing.T) {
	vars, err := yugabyteRoleVars(context.Background(), nilHost(), ServiceConfig{
		Metadata: map[string]any{
			"postgres_password": "shared-secret",
			"databases": []map[string]string{
				{"name": "foghorn_eu", "owner": "foghorn_eu", "password": "cluster-secret"},
			},
		},
	}, mockPrivateerHelpers())
	if err != nil {
		t.Fatalf("yugabyteRoleVars: %v", err)
	}
	dbs, ok := vars["yugabyte_databases"].([]map[string]any)
	if !ok || len(dbs) != 1 {
		t.Fatalf("yugabyte_databases = %#v, want one database", vars["yugabyte_databases"])
	}
	if got := dbs[0]["password"]; got != "cluster-secret" {
		t.Fatalf("database password = %v, want cluster-secret", got)
	}
}

func TestYugabyteRoleUsesPerDatabasePasswords(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/tasks/init.yml")
	want := `password: "{{ item.password | default(yugabyte_application_password) }}"`
	if !strings.Contains(content, want) {
		t.Fatalf("yugabyte init should use per-database owner passwords when provided; missing %q:\n%s", want, content)
	}
}
