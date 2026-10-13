package provisioner

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// roleSettingsProbe answers the runtime-role settings query from bounded and
// records every statement.
type roleSettingsProbe struct {
	fakeServiceDatabaseProbe
	existing   []string
	bounded    map[string]bool
	queries    []string
	statements []string
}

func (p *roleSettingsProbe) textRows(_ context.Context, database, query string) ([]string, error) {
	if database != "postgres" {
		panic("role settings read outside the maintenance database: " + database)
	}
	p.queries = append(p.queries, query)
	var rows []string
	for _, role := range p.existing {
		if strings.Contains(query, "'"+role+"'") && !p.bounded[role] {
			rows = append(rows, role)
		}
	}
	return rows, nil
}

func (p *roleSettingsProbe) exec(_ context.Context, _ string, statement string) error {
	p.statements = append(p.statements, statement)
	if role, _, ok := strings.Cut(strings.TrimPrefix(statement, `ALTER ROLE "`), `"`); ok && strings.Contains(statement, "idle_in_transaction_session_timeout") {
		p.bounded[role] = true
	}
	return nil
}

// A runtime role without the limits gets both; a role that carries them and a
// role that does not exist are left alone; a rerun alters nothing.
func TestEnsureRuntimeRoleSessionLimitsAltersOnlyUnboundedRoles(t *testing.T) {
	probe := &roleSettingsProbe{existing: []string{"commodore_runtime", "purser_runtime"}, bounded: map[string]bool{"commodore_runtime": true}}
	roles := []string{"purser_runtime", "commodore_runtime", "lookout_runtime", "purser_runtime"}

	altered, err := ensureRuntimeRoleSessionLimits(context.Background(), probe, roles, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(altered, []string{"purser_runtime"}) {
		t.Fatalf("altered = %v, want purser_runtime", altered)
	}
	want := []string{
		`ALTER ROLE "purser_runtime" SET statement_timeout = '60s'`,
		`ALTER ROLE "purser_runtime" SET idle_in_transaction_session_timeout = '60s'`,
	}
	if !reflect.DeepEqual(probe.statements, want) {
		t.Fatalf("statements = %q\nwant %q", probe.statements, want)
	}
	query := probe.queries[0]
	for _, fragment := range []string{
		"r.rolname IN ('commodore_runtime', 'lookout_runtime', 'purser_runtime')",
		"s.setdatabase = 0",
		"s.setconfig @> ARRAY['statement_timeout=60s', 'idle_in_transaction_session_timeout=60s']::text[]",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("settings query lacks %q:\n%s", fragment, query)
		}
	}

	probe.statements = nil
	again, err := ensureRuntimeRoleSessionLimits(context.Background(), probe, roles, false)
	if err != nil || len(again) != 0 || len(probe.statements) != 0 {
		t.Fatalf("rerun: altered %v, statements %v, err %v; want nothing", again, probe.statements, err)
	}
}

func TestEnsureRuntimeRoleSessionLimitsDryRunOnlyReads(t *testing.T) {
	probe := &roleSettingsProbe{existing: []string{"purser_runtime"}, bounded: map[string]bool{}}
	planned, err := ensureRuntimeRoleSessionLimits(context.Background(), probe, []string{"purser_runtime"}, true)
	if err != nil || !reflect.DeepEqual(planned, []string{"purser_runtime"}) || len(probe.statements) != 0 {
		t.Fatalf("dry-run: planned %v, statements %v, err %v", planned, probe.statements, err)
	}
}

func TestEnsureRuntimeRoleSessionLimitsRefusesUnsafeRoleNames(t *testing.T) {
	probe := &roleSettingsProbe{bounded: map[string]bool{}}
	if _, err := ensureRuntimeRoleSessionLimits(context.Background(), probe, []string{`x"; DROP ROLE y; --`}, false); err == nil {
		t.Fatal("an unsafe role name was accepted")
	}
	if len(probe.queries) != 0 || len(probe.statements) != 0 {
		t.Fatalf("unsafe role name reached SQL: %v %v", probe.queries, probe.statements)
	}
}

// A Postgres server is reached on its own port's local socket, so a named
// instance on another port is read and altered there, not on the default
// server's.
func TestPostgresAdminProbeConnectsToTheServersPort(t *testing.T) {
	pool := ssh.NewPool(time.Second, "")
	defer pool.Close()
	host := inventory.Host{Name: "apps-1", ExternalIP: "192.0.2.20"}
	for port, want := range map[int]string{0: "-p 5432 ", 5434: "-p 5434 "} {
		probe, err := newServiceDatabaseProbe(pool, host, &inventory.PostgresConfig{Enabled: true, Engine: "postgres", Port: port})
		if err != nil {
			t.Fatal(err)
		}
		psql, ok := probe.(psqlStateProbe)
		if !ok {
			t.Fatalf("probe = %T, want the psql probe", probe)
		}
		if command := psql.command("postgres", "SELECT 1"); !strings.Contains(command, "sudo -u postgres psql -X -v ON_ERROR_STOP=1 "+want) {
			t.Fatalf("port %d: command %q does not connect with %q", port, command, want)
		}
	}
}

// The CLI and the roles' init.yml attach the same limits, so a release and a
// provision leave a runtime role with one set of settings.
func TestRuntimeRoleSessionLimitsMatchTheDatabaseRoles(t *testing.T) {
	for _, role := range []string{"postgres", "yugabyte"} {
		raw, err := os.ReadFile(ansibleTreePath(t, "collections", "ansible_collections", "frameworks", "infra", "roles", role, "vars", "main.yml"))
		if err != nil {
			t.Fatal(err)
		}
		vars := string(raw)
		for _, limit := range RuntimeRoleSessionLimits {
			setting, value, _ := strings.Cut(limit, "=")
			if line := role + "_runtime_" + setting + ": " + value; !strings.Contains(vars, line) {
				t.Errorf("roles/%s/vars/main.yml lacks %q", role, line)
			}
		}
	}
}
