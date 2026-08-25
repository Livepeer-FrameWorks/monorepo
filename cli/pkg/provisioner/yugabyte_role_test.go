package provisioner

import (
	"context"
	"strings"
	"testing"
)

func TestYugabyteRoleVarsPassesDatabaseOwnerPassword(t *testing.T) {
	vars, err := yugabyteRoleVars(context.Background(), nilHost(), ServiceConfig{
		Metadata: map[string]any{
			"postgres_password":         "shared-secret",
			"postgres_runtime_password": "runtime-default",
			"databases": []map[string]string{
				{"name": "foghorn_eu", "owner": "foghorn_eu", "password": "cluster-secret", "runtime_role": "foghorn_app", "runtime_password": "runtime-secret"},
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
		t.Fatal("database owner password did not match metadata")
	}
	if got := dbs[0]["runtime_role"]; got != "foghorn_app" {
		t.Fatalf("runtime role = %v, want foghorn_app", got)
	}
	if got := dbs[0]["runtime_password"]; got != "runtime-secret" {
		t.Fatal("database runtime password did not match metadata")
	}
}

func TestYugabyteRoleVarsRejectsRuntimeOwnerCollision(t *testing.T) {
	_, err := yugabyteRoleVars(context.Background(), nilHost(), ServiceConfig{
		Metadata: map[string]any{
			"databases": []map[string]string{{
				"name": "quartermaster", "owner": "quartermaster", "runtime_role": "quartermaster",
			}},
		},
	}, mockPrivateerHelpers())
	if err == nil || !strings.Contains(err.Error(), "runtime role \"quartermaster\" must differ from owner") {
		t.Fatalf("yugabyteRoleVars() error = %v, want owner/runtime collision", err)
	}
}

func TestYugabyteRoleUsesPerDatabasePasswords(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/tasks/init.yml")
	for _, want := range []string{
		`password: "{{ item.password | default(yugabyte_application_password) }}"`,
		`password: "{{ item.runtime_password | default(yugabyte_runtime_password) }}"`,
		`!= (item.password | default(yugabyte_application_password))`,
		`!= (item.owner | default(item.name, true))`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("yugabyte init should keep owner/runtime credentials independent; missing %q:\n%s", want, content)
		}
	}
}
