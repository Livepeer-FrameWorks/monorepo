//go:build yugabyte_role_engine

package provisioner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/cli/internal/releases"

	"gopkg.in/yaml.v3"
)

// TestYugabyteRoleAppliesEveryServiceBaseline runs the yugabyte role's init and schema task files through
// ansible-playbook, with the collections the CLI pins, against the pinned contract engine running the production
// Read Committed flag. Every platform database is created in its declared layout and receives the baseline the CLI
// renders for it, as `cluster provision` and a release that adds a database do. The engine container is laid out as
// a role-managed host: the selected release is reachable through /opt/yugabyte/current.
//
// Needs Docker, ansible-playbook, and the pinned collections (make ansible-galaxy-install). The target needs network
// access once, for the role's psycopg2 prerequisite.
func TestYugabyteRoleAppliesEveryServiceBaseline(t *testing.T) {
	repo := ybRoleRepo(t)
	container := fmt.Sprintf("fw-yb-role-schema-%d-%d", os.Getpid(), time.Now().UnixNano())
	ybRoleStartEngine(t, repo, container)

	names := releases.ServiceDatabaseNames()
	schemaDatabases := make([]SchemaDatabase, 0, len(names))
	databases := make([]map[string]any, 0, len(names))
	placements := make([]map[string]any, 0, len(names))
	colocatedByName := map[string]bool{}
	for _, name := range names {
		colocated, colocationErr := YugabyteDatabaseColocated(name)
		if colocationErr != nil {
			t.Fatal(colocationErr)
		}
		colocatedByName[name] = colocated
		schemaDatabases = append(schemaDatabases, SchemaDatabase{Name: name})
		databases = append(databases, map[string]any{
			"name": name, "owner": name, "password": "owner-" + name,
			"runtime_role": name + "_runtime", "runtime_password": "runtime-" + name, "colocated": colocated,
		})
		placements = append(placements, map[string]any{"name": name, "owner": name, "colocated": colocated})
	}
	items, cleanup, err := BuildSchemaItemsForEngine(schemaDatabases, SQLEngineYugabyte)
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(names) {
		t.Fatalf("rendered %d baselines for %d service databases", len(items), len(names))
	}

	output, err := ybRoleRunPlaybook(t, repo, container, map[string]any{
		"yugabyte_databases":           databases,
		"yugabyte_database_placements": placements,
		"yugabyte_schema_items":        items,
	}, `    - ansible.builtin.include_role:
        name: frameworks.infra.yugabyte
        tasks_from: init.yml
    - ansible.builtin.include_role:
        name: frameworks.infra.yugabyte
        tasks_from: schema.yml
`)
	if err != nil {
		t.Fatalf("yugabyte role init+schema failed: %v\n%s", err, ybRoleTail(output, 30000))
	}

	floor := releases.SchemaMigrationFloor()
	for _, name := range names {
		got := ybRoleQuery(t, container, name, "SELECT yb_is_database_colocated()::text || '|' || (SELECT floor FROM public._schema_baseline) || '|' || (SELECT count(*) FROM information_schema.tables WHERE table_schema = current_database() AND table_type = 'BASE TABLE')::text")
		parts := strings.Split(got, "|")
		if len(parts) != 3 {
			t.Fatalf("%s: unexpected probe output %q", name, got)
		}
		if want := fmt.Sprint(colocatedByName[name]); parts[0] != want {
			t.Errorf("%s: colocated = %s, want %s", name, parts[0], want)
		}
		if parts[1] != floor {
			t.Errorf("%s: baseline marker = %q, want %q: the baseline did not run to its end", name, parts[1], floor)
		}
		if parts[2] == "0" {
			t.Errorf("%s: the service schema has no tables", name)
		}
	}
}

// TestYugabyteRoleMigrationRefusesAbortedConcurrentIndex applies a concurrent index migration item through the role's
// migrate_item.yml while roles are being created elsewhere on the cluster. The CREATE ROLE aborts the index build and
// the engine's retry of CREATE INDEX CONCURRENTLY IF NOT EXISTS then reports success over the invalid index, so the
// item must fail without writing its ledger row; the rerun repairs the index and records the item.
func TestYugabyteRoleMigrationRefusesAbortedConcurrentIndex(t *testing.T) {
	repo := ybRoleRepo(t)
	container := fmt.Sprintf("fw-yb-role-migrate-%d-%d", os.Getpid(), time.Now().UnixNano())
	ybRoleStartEngine(t, repo, container)

	const database = "role_migrate"
	ybRoleQuery(t, container, "yugabyte", "CREATE DATABASE "+database+" WITH COLOCATION = true")
	ybRoleQuery(t, container, database, "CREATE TABLE public.t (id BIGINT PRIMARY KEY, v INT)")
	ybRoleQuery(t, container, database, "INSERT INTO public.t SELECT g, g % 1000 FROM generate_series(1, 300000) AS g")
	ybRoleQuery(t, container, database, YugabyteMigrationLedgerDDL)
	item := map[string]any{
		"db": database, "owner": "yugabyte", "version": "v0.0.1", "phase": "expand", "sequence": 1,
		"filename": "001_t_v.notx.sql", "checksum": "test", "transactional": false,
		"statements":           []string{"CREATE INDEX CONCURRENTLY IF NOT EXISTS t_v ON public.t (v)"},
		"invalid_index_guards": []string{"public.t_v"},
		"precheck_query":       "", "precheck_path": "",
	}
	tasks := `    - ansible.builtin.include_role:
        name: frameworks.infra.yugabyte
        tasks_from: ysql_prereqs.yml
    - ansible.builtin.include_role:
        name: frameworks.infra.yugabyte
        tasks_from: migrate_item.yml
`
	state := func() string {
		return ybRoleQuery(t, container, database, "SELECT coalesce((SELECT i.indisvalid::text FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = 't_v'), 'absent') || '|' || (SELECT count(*) FROM _migrations)::text")
	}

	stop := make(chan struct{})
	churned := make(chan int)
	go func() {
		created := 0
		for {
			select {
			case <-stop:
				churned <- created
				return
			default:
			}
			created++
			_ = exec.Command("docker", "exec", container, "/home/yugabyte/bin/ysqlsh", "-X", "-h", "127.0.0.1", "-qc",
				fmt.Sprintf("CREATE ROLE role_migrate_churn_%d", created)).Run()
			time.Sleep(200 * time.Millisecond)
		}
	}()
	output, err := ybRoleRunPlaybook(t, repo, container, map[string]any{"item": item}, tasks)
	close(stop)
	created := <-churned
	if err == nil {
		t.Fatalf("the item succeeded while %d roles were created during its index build; index|ledger = %s\n%s", created, state(), ybRoleTail(output, 8000))
	}
	if got := state(); got != "false|0" {
		t.Fatalf("after the refused item, index|ledger = %s, want false|0\n%s", got, ybRoleTail(output, 8000))
	}

	output, err = ybRoleRunPlaybook(t, repo, container, map[string]any{"item": item}, tasks)
	if err != nil {
		t.Fatalf("rerun without concurrent DDL failed: %v\n%s", err, ybRoleTail(output, 8000))
	}
	if got := state(); got != "true|1" {
		t.Fatalf("after the rerun, index|ledger = %s, want true|1", got)
	}
}

// ybRoleRepo returns the repository root after checking the tools the role tests drive are present.
func ybRoleRepo(t *testing.T) string {
	t.Helper()
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, lookErr := exec.LookPath(ybRolePlaybookBinary()); lookErr != nil {
		t.Fatalf("%s not found: %v", ybRolePlaybookBinary(), lookErr)
	}
	for _, collection := range []string{"postgresql", "docker"} {
		if _, statErr := os.Stat(filepath.Join(repo, "ansible", ".cache", "collections", "ansible_collections", "community", collection)); statErr != nil {
			t.Fatalf("community.%s is not installed under ansible/.cache/collections; run make ansible-galaxy-install", collection)
		}
	}
	return repo
}

func ybRolePlaybookBinary() string {
	if binary := os.Getenv("FRAMEWORKS_ANSIBLE_PLAYBOOK"); binary != "" {
		return binary
	}
	return "ansible-playbook"
}

// ybRoleRunPlaybook runs tasks against the engine container with the repository's ansible.cfg and collections. The
// extra vars carry the connection the role uses and are merged with vars.
func ybRoleRunPlaybook(t *testing.T, repo, container string, vars map[string]any, tasks string) (string, error) {
	t.Helper()
	extraVars := map[string]any{
		"yugabyte_ysql_port":          5433,
		"yugabyte_superuser_role":     "yugabyte",
		"yugabyte_superuser_password": "",
		// The engine image is AlmaLinux 8, whose python3-psycopg2 serves the platform Python rather than the
		// interpreter Ansible runs modules with.
		"yugabyte_postgresql_python_packages_by_family": map[string][]string{"RedHat": {"python3.11-psycopg2"}},
	}
	for key, value := range vars {
		extraVars[key] = value
	}
	work := t.TempDir()
	encoded, err := json.Marshal(extraVars)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"vars.json":     string(encoded),
		"inventory.ini": container + " ansible_connection=community.docker.docker ansible_python_interpreter=/usr/bin/python3\n",
		"playbook.yml":  "- hosts: all\n  gather_facts: true\n  tasks:\n" + tasks,
	}
	for name, content := range files {
		if err = os.WriteFile(filepath.Join(work, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	run := exec.CommandContext(ctx, ybRolePlaybookBinary(), "-i", filepath.Join(work, "inventory.ini"),
		"-e", "@"+filepath.Join(work, "vars.json"), filepath.Join(work, "playbook.yml"))
	run.Dir = work
	run.Env = append(os.Environ(),
		"ANSIBLE_CONFIG="+filepath.Join(repo, "ansible", "ansible.cfg"),
		"ANSIBLE_COLLECTIONS_PATH="+filepath.Join(repo, "ansible", "collections")+string(os.PathListSeparator)+
			filepath.Join(repo, "ansible", ".cache", "collections"),
		"ANSIBLE_NOCOLOR=1",
	)
	var output bytes.Buffer
	run.Stdout, run.Stderr = &output, &output
	err = run.Run()
	return output.String(), err
}

// ybRoleStartEngine starts the pinned contract engine with the production tserver flag that changes behaviour and
// its YSQL listener on 127.0.0.1, where the role connects, and links /opt/yugabyte/current to the image's release.
func ybRoleStartEngine(t *testing.T, repo, container string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repo, "config", "schema-contract-engines.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var engines struct {
		ContractEngines []struct {
			Name   string `yaml:"name"`
			Image  string `yaml:"image"`
			Digest string `yaml:"digest"`
		} `yaml:"contract_engines"`
	}
	if err = yaml.Unmarshal(raw, &engines); err != nil {
		t.Fatal(err)
	}
	image := ""
	for _, engine := range engines.ContractEngines {
		if engine.Name == "yugabyte" && engine.Image != "" && engine.Digest != "" {
			image = engine.Image + "@" + engine.Digest
		}
	}
	if image == "" {
		t.Fatal("config/schema-contract-engines.yaml pins no yugabyte image")
	}
	t.Cleanup(func() {
		if out, rmErr := exec.Command("docker", "rm", "-f", container).CombinedOutput(); rmErr != nil {
			t.Logf("remove %s: %v: %s", container, rmErr, out)
		}
	})
	if out, runErr := exec.Command("docker", "run", "-d", "--name", container, "--hostname", container,
		"--tmpfs", "/var/lib/frameworks-yugabyte-role-data", image, "bash", "-c",
		"exec bin/yugabyted start --background=false --ui=false --callhome=false --base_dir=/var/lib/frameworks-yugabyte-role-data --advertise_address=127.0.0.1 --tserver_flags=yb_enable_read_committed_isolation=true",
	).CombinedOutput(); runErr != nil {
		t.Fatalf("start %s: %v: %s", container, runErr, out)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if exec.Command("docker", "exec", container, "/home/yugabyte/bin/ysqlsh", "-h", "127.0.0.1", "-tAc", "SELECT 1").Run() == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", "--tail", "60", container).CombinedOutput()
			t.Fatalf("%s did not accept YSQL connections within 3 minutes:\n%s", container, logs)
		}
		time.Sleep(2 * time.Second)
	}
	if out, linkErr := exec.Command("docker", "exec", container, "sh", "-c",
		"mkdir -p /opt/yugabyte && ln -sfn /home/yugabyte /opt/yugabyte/current").CombinedOutput(); linkErr != nil {
		t.Fatalf("link the selected release in %s: %v: %s", container, linkErr, out)
	}
}

func ybRoleQuery(t *testing.T, container, database, sql string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", container, "/home/yugabyte/bin/ysqlsh", "-X", "-h", "127.0.0.1",
		"-d", database, "-v", "ON_ERROR_STOP=1", "-tAc", sql).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", database, err, out)
	}
	return strings.TrimSpace(string(out))
}

func ybRoleTail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return "...\n" + s[len(s)-limit:]
}
