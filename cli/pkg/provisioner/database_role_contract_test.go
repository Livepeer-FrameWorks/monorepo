package provisioner

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func databaseRoleTaskFile(t *testing.T, engine, file string) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "roles", engine, "tasks", file)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestDatabaseRolesApplyOwnershipBeforeRuntimeGrants(t *testing.T) {
	for _, engine := range []string{"postgres", "yugabyte"} {
		t.Run(engine, func(t *testing.T) {
			schema := databaseRoleTaskFile(t, engine, "schema.yml")
			apply := strings.Index(schema, "Apply baseline schemas")
			ownership := strings.Index(schema, "Grant baseline schema ownership")
			runtimeGrants := strings.Index(schema, "Grant least-privilege runtime access")
			if apply < 0 || ownership < 0 || runtimeGrants < 0 || apply >= ownership || ownership >= runtimeGrants {
				t.Fatalf("%s schema task order must be baseline -> ownership -> runtime grants", engine)
			}
			for _, required := range []string{
				"REVOKE CREATE ON SCHEMA",
				"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES",
				"ALTER DEFAULT PRIVILEGES FOR ROLE",
			} {
				if !strings.Contains(schema, required) {
					t.Fatalf("%s runtime grants missing %q", engine, required)
				}
			}
		})
	}
}

func TestDatabaseMigrationsRunAsDeclaredOwner(t *testing.T) {
	for _, engine := range []string{"postgres", "yugabyte"} {
		t.Run(engine, func(t *testing.T) {
			migration := databaseRoleTaskFile(t, engine, "migrate.yml")
			setRole := strings.Index(migration, `SET ROLE "{{ item.owner }}"`)
			body := strings.Index(migration, `"{{ item.sql }}"`)
			resetRole := strings.Index(migration, `"RESET ROLE"`)
			ledger := strings.LastIndex(migration, "INSERT INTO _migrations")
			if setRole < 0 || body < 0 || resetRole < 0 || ledger < 0 || setRole >= body || body >= resetRole || resetRole >= ledger {
				t.Fatalf("%s migration task must execute body as owner and reset before ledger write", engine)
			}
			if !strings.Contains(migration, "Migration owner identifiers must be simple SQL identifiers") {
				t.Fatalf("%s migration task does not validate the SET ROLE identifier", engine)
			}
		})
	}
}
