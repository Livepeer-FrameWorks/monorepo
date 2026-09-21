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

func TestRestoreStopTasksPreserveServiceDefinitions(t *testing.T) {
	for _, role := range []string{"go_service", "compose_stack"} {
		t.Run(role, func(t *testing.T) {
			main := databaseRoleTaskFile(t, role, "main.yml")
			if !strings.Contains(main, "ansible.builtin.import_tasks: stop.yml\n  tags: [stop, never]") {
				t.Fatal("stop tasks must be explicitly selectable and excluded from ordinary provisioning")
			}
			stop := databaseRoleTaskFile(t, role, "stop.yml")
			if !strings.Contains(stop, "state: stopped") {
				t.Fatal("stop must preserve installed resources")
			}
			for _, forbidden := range []string{"state: absent", "enabled: false", "failed_when:", "ignore_errors:", "remove_volumes:", "remove_images:"} {
				if strings.Contains(stop, forbidden) {
					t.Fatalf("stop contains destructive or error-suppressing option %q", forbidden)
				}
			}
		})
	}
	for _, role := range []string{"chatwoot", "listmonk"} {
		main := databaseRoleTaskFile(t, role, "main.yml")
		if !strings.Contains(main, "tasks_from: stop.yml") || !strings.Contains(main, "tags: [stop, never]") {
			t.Fatalf("%s must delegate stop to compose_stack", role)
		}
	}
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
			setRole := strings.Index(migration, `SET ROLE "' ~ item.owner ~ '"`)
			body := strings.Index(migration, `+ item.statements`)
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
