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

	services := ybRoleServices(t)
	items, cleanup, err := BuildSchemaItemsForEngine(services.schemaDatabases, SQLEngineYugabyte)
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(services.names) {
		t.Fatalf("rendered %d baselines for %d service databases", len(items), len(services.names))
	}
	ybRoleProvision(t, repo, container, services, items)

	floor := releases.SchemaMigrationFloor()
	for _, name := range services.names {
		got := ybRoleQuery(t, container, name, "SELECT yb_is_database_colocated()::text || '|' || (SELECT floor FROM public._schema_baseline) || '|' || (SELECT count(*) FROM information_schema.tables WHERE table_schema = current_database() AND table_type = 'BASE TABLE')::text")
		parts := strings.Split(got, "|")
		if len(parts) != 3 {
			t.Fatalf("%s: unexpected probe output %q", name, got)
		}
		if want := fmt.Sprint(services.colocated[name]); parts[0] != want {
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

// TestYugabyteRoleFreshInstallAppliesEveryMigration provisions every platform database in its declared layout from its
// current baseline through the role, then applies every embedded migration up to the pending release, one phase at a
// time through migrate.yml, as `cluster provision` and the releases that follow it do. A fresh database replays the
// post-floor migrations over a baseline that already holds their effect, including files that write rows and then
// alter a table of the same colocated database. Every item must apply and be recorded, and every index must be valid.
func TestYugabyteRoleFreshInstallAppliesEveryMigration(t *testing.T) {
	repo := ybRoleRepo(t)
	container := fmt.Sprintf("fw-yb-role-fresh-%d-%d", os.Getpid(), time.Now().UnixNano())
	ybRoleStartEngine(t, repo, container)

	services := ybRoleServices(t)
	items, cleanup, err := BuildSchemaItemsForEngine(services.schemaDatabases, SQLEngineYugabyte)
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	ybRoleProvision(t, repo, container, services, items)

	target := ybRolePendingRelease(t)
	applied := map[string]int{}
	for _, phase := range []string{"expand", "postdeploy", "contract"} {
		phaseItems, buildErr := BuildMigrationItemsForEngine(services.schemaDatabases, phase, target, SQLEngineYugabyte)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		ybRoleMigrate(t, repo, container, phase, phaseItems)
		for _, item := range phaseItems {
			applied[item["db"].(string)]++
		}
	}
	ybRoleRequireApplied(t, container, services.names, applied)
}

// TestYugabyteRoleUpgradeAppliesPendingMigrations builds every platform database in its declared layout from the
// baseline of the latest shipped tag, as an existing cluster holds it, and upgrades it through the role with every
// migration newer than that tag, one phase at a time. Commodore's v0.3.11 expand 006 inserts a row and then alters a
// table of the same colocated database, which a single-transaction apply loses to a 40001 abort.
func TestYugabyteRoleUpgradeAppliesPendingMigrations(t *testing.T) {
	repo := ybRoleRepo(t)
	fromTag := ybRoleShippedTag(t, repo)
	container := fmt.Sprintf("fw-yb-role-upgrade-%d-%d", os.Getpid(), time.Now().UnixNano())
	ybRoleStartEngine(t, repo, container)

	services := ybRoleServices(t)
	ybRoleProvision(t, repo, container, services, ybRoleTaggedSchemaItems(t, repo, fromTag, services.names))

	target := ybRolePendingRelease(t)
	applied := map[string]int{}
	for _, phase := range []string{"expand", "postdeploy", "contract"} {
		phaseItems, buildErr := BuildMigrationItemsForEngine(services.schemaDatabases, phase, target, SQLEngineYugabyte)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		pending := make([]map[string]any, 0, len(phaseItems))
		for _, item := range phaseItems {
			if compareSemver(item["version"].(string), fromTag) > 0 {
				pending = append(pending, item)
				applied[item["db"].(string)]++
			}
		}
		ybRoleMigrate(t, repo, container, phase, pending)
	}
	if applied["commodore"] == 0 {
		t.Fatalf("no commodore migration is newer than %s; the upgrade proves nothing about colocated DML-then-DDL files", fromTag)
	}
	ybRoleRequireApplied(t, container, services.names, applied)
}

// TestYugabyteRoleMigrationRerunConvergesAfterPartialFailure applies commodore's v0.3.11 expand 019 to a colocated
// commodore database and makes its last statement fail after the statement before it dropped a trigger. The item must
// fail without a ledger row, and once the cause is removed its rerun must complete from the partially applied state
// and record the item, although the file drops that trigger without IF EXISTS.
func TestYugabyteRoleMigrationRerunConvergesAfterPartialFailure(t *testing.T) {
	repo := ybRoleRepo(t)
	container := fmt.Sprintf("fw-yb-role-rerun-%d-%d", os.Getpid(), time.Now().UnixNano())
	ybRoleStartEngine(t, repo, container)

	services := ybRoleServices(t, "commodore")
	items, cleanup, err := BuildSchemaItemsForEngine(services.schemaDatabases, SQLEngineYugabyte)
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	ybRoleProvision(t, repo, container, services, items)
	if !services.colocated["commodore"] {
		t.Fatal("commodore is not declared colocated")
	}
	ybRoleQuery(t, container, "commodore", YugabyteMigrationLedgerDDL)

	expand, err := BuildMigrationItemsForEngine(services.schemaDatabases, "expand", ybRolePendingRelease(t), SQLEngineYugabyte)
	if err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	for _, candidate := range expand {
		if candidate["version"] == "v0.3.11" && candidate["filename"] == "019_restream_media_authority_refresh.sql" {
			item = candidate
		}
	}
	if item == nil {
		t.Fatal("commodore v0.3.11 expand 019 is not offered")
	}
	tasks := `    - ansible.builtin.include_role:
        name: frameworks.infra.yugabyte
        tasks_from: ysql_prereqs.yml
    - ansible.builtin.include_role:
        name: frameworks.infra.yugabyte
        tasks_from: migrate_item.yml
`
	// The file's last statement creates a trigger on the function below. Moving the function out of its name fails that
	// statement after the DROP TRIGGER before it, on the same table, has run; the triggers already bound to it keep it.
	const function = "commodore.live_stream_child_media_authority_changed()"
	ybRoleQuery(t, container, "commodore", "ALTER FUNCTION "+function+" RENAME TO live_stream_child_media_authority_changed_moved")
	state := func() string {
		return ybRoleQuery(t, container, "commodore", "SELECT (SELECT count(*) FROM pg_trigger WHERE tgname = 'trg_push_target_media_authority' AND tgrelid = 'commodore.push_targets'::regclass)::text || '|' || (SELECT count(*) FROM _migrations WHERE version = 'v0.3.11' AND phase = 'expand' AND seq = 19)::text")
	}
	output, err := ybRoleRunPlaybook(t, repo, container, map[string]any{"item": item}, tasks)
	if err == nil {
		t.Fatalf("the item succeeded although its trigger function was missing\n%s", ybRoleTail(output, 8000))
	}
	if got := state(); got != "0|0" {
		t.Fatalf("after the failed item, trigger|ledger = %s, want 0|0: the item must fail after dropping the trigger and before its ledger row\n%s", got, ybRoleTail(output, 8000))
	}

	ybRoleQuery(t, container, "commodore", "ALTER FUNCTION commodore.live_stream_child_media_authority_changed_moved() RENAME TO live_stream_child_media_authority_changed")
	output, err = ybRoleRunPlaybook(t, repo, container, map[string]any{"item": item}, tasks)
	if err != nil {
		t.Fatalf("rerun after the partial failure failed: %v\n%s", err, ybRoleTail(output, 8000))
	}
	if got := state(); got != "1|1" {
		t.Fatalf("after the rerun, trigger|ledger = %s, want 1|1", got)
	}
}

// TestYugabyteRoleMigrationReplaysOnlyTheStatementThatFailedRetryably applies an item whose second statement fails
// with a lock timeout (55P03) on its first execution and a serialization failure (40001) on its second, and succeeds on
// its third; the attempt count lives in a sequence, which no rollback resets. That statement is replayed, the
// statement before it ran once and is not repeated, and the item is recorded. The failures are raised by the
// statement itself: on the pinned engine, lock_timeout does not end a wait for a row lock.
func TestYugabyteRoleMigrationReplaysOnlyTheStatementThatFailedRetryably(t *testing.T) {
	repo := ybRoleRepo(t)
	container := fmt.Sprintf("fw-yb-role-retry-%d-%d", os.Getpid(), time.Now().UnixNano())
	ybRoleStartEngine(t, repo, container)

	const database = "role_retry"
	ybRoleQuery(t, container, "yugabyte", "CREATE DATABASE "+database+" WITH COLOCATION = true")
	ybRoleQuery(t, container, database, "CREATE TABLE public.t (id BIGINT PRIMARY KEY, v INT)")
	ybRoleQuery(t, container, database, "CREATE TABLE public.applied (id BIGSERIAL PRIMARY KEY, note TEXT)")
	ybRoleQuery(t, container, database, "INSERT INTO public.t VALUES (1, 0)")
	ybRoleQuery(t, container, database, "CREATE SEQUENCE public.attempts")
	ybRoleQuery(t, container, database, YugabyteMigrationLedgerDDL)
	flaky := `DO $$
DECLARE attempt bigint := nextval('public.attempts');
BEGIN
  IF attempt = 1 THEN RAISE EXCEPTION 'injected lock timeout' USING ERRCODE = '55P03'; END IF;
  IF attempt = 2 THEN RAISE EXCEPTION 'injected serialization failure' USING ERRCODE = '40001'; END IF;
  UPDATE public.t SET v = 1 WHERE id = 1;
END $$`
	item := map[string]any{
		"db": database, "owner": "yugabyte", "version": "v0.0.1", "phase": "expand", "sequence": 1,
		"filename": "001_retry.sql", "checksum": "test", "transactional": true,
		"statements":           []string{"INSERT INTO public.applied (note) VALUES ('first statement')", flaky},
		"invalid_index_guards": []string{},
		"precheck_query":       "", "precheck_path": "",
	}
	output, err := ybRoleRunPlaybook(t, repo, container, map[string]any{"item": item}, `    - ansible.builtin.include_role:
        name: frameworks.infra.yugabyte
        tasks_from: ysql_prereqs.yml
    - ansible.builtin.include_role:
        name: frameworks.infra.yugabyte
        tasks_from: migrate_item.yml
`)
	if err != nil {
		t.Fatalf("item failed although its statement succeeds on the third attempt: %v\n%s", err, ybRoleTail(output, 8000))
	}
	for _, code := range []string{"SQLSTATE 55P03, attempt 2 of 7", "SQLSTATE 40001, attempt 3 of 7"} {
		if !strings.Contains(output, code) {
			t.Fatalf("no replay reported for %q:\n%s", code, ybRoleTail(output, 8000))
		}
	}
	if got := ybRoleQuery(t, container, database, "SELECT (SELECT count(*) FROM public.applied)::text || '|' || (SELECT v FROM public.t WHERE id = 1)::text || '|' || (SELECT count(*) FROM _migrations)::text"); got != "1|1|1" {
		t.Fatalf("first statement runs|updated value|ledger rows = %s, want 1|1|1", got)
	}
}

// ybRoleServiceSet is the platform databases a test provisions, in their declared layouts, in the shapes the role's
// init and schema tasks and the migration item builders take.
type ybRoleServiceSet struct {
	names           []string
	schemaDatabases []SchemaDatabase
	databases       []map[string]any
	placements      []map[string]any
	colocated       map[string]bool
}

// ybRoleServices returns the named platform databases, or every one when none is named.
func ybRoleServices(t *testing.T, only ...string) ybRoleServiceSet {
	t.Helper()
	names := only
	if len(names) == 0 {
		names = releases.ServiceDatabaseNames()
	}
	set := ybRoleServiceSet{names: names, colocated: map[string]bool{}}
	for _, name := range names {
		colocated, err := YugabyteDatabaseColocated(name)
		if err != nil {
			t.Fatal(err)
		}
		set.colocated[name] = colocated
		set.schemaDatabases = append(set.schemaDatabases, SchemaDatabase{Name: name})
		set.databases = append(set.databases, map[string]any{
			"name": name, "owner": name, "password": "owner-" + name,
			"runtime_role": name + "_runtime", "runtime_password": "runtime-" + name, "colocated": colocated,
		})
		set.placements = append(set.placements, map[string]any{"name": name, "owner": name, "colocated": colocated})
	}
	return set
}

// ybRoleProvision creates the databases and applies the given baseline items through the role's init and schema tasks.
func ybRoleProvision(t *testing.T, repo, container string, services ybRoleServiceSet, schemaItems []map[string]any) {
	t.Helper()
	output, err := ybRoleRunPlaybook(t, repo, container, map[string]any{
		"yugabyte_databases":           services.databases,
		"yugabyte_database_placements": services.placements,
		"yugabyte_schema_items":        schemaItems,
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
}

// ybRoleMigrate applies one phase's items through the role's migrate.yml, which reads each ledger, reconciles
// ownership, and applies the pending items in order.
func ybRoleMigrate(t *testing.T, repo, container, phase string, items []map[string]any) {
	t.Helper()
	if len(items) == 0 {
		return
	}
	output, err := ybRoleRunPlaybook(t, repo, container, map[string]any{"yugabyte_migrate_items": items}, `    - ansible.builtin.include_role:
        name: frameworks.infra.yugabyte
        tasks_from: migrate.yml
`)
	if err != nil {
		t.Fatalf("yugabyte role %s migrations (%d items) failed: %v\n%s", phase, len(items), err, ybRoleTail(output, 20000))
	}
}

// ybRoleRequireApplied requires each database's ledger to hold exactly the items applied to it and every index in
// it to be valid and ready.
func ybRoleRequireApplied(t *testing.T, container string, names []string, applied map[string]int) {
	t.Helper()
	for _, name := range names {
		// migrate.yml creates the ledger only in a database it applies something to.
		ledger := "0"
		if ybRoleQuery(t, container, name, "SELECT to_regclass('public._migrations') IS NOT NULL") == "t" {
			ledger = ybRoleQuery(t, container, name, "SELECT count(*) FROM _migrations")
		}
		invalid := ybRoleQuery(t, container, name, "SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND (NOT i.indisvalid OR NOT i.indisready)")
		if got, want := ledger+"|"+invalid, fmt.Sprintf("%d|0", applied[name]); got != want {
			t.Errorf("%s: ledger rows|invalid indexes = %s, want %s", name, got, want)
		}
	}
}

// ybRolePendingRelease is the newest release the catalog declares, which the migrations target.
func ybRolePendingRelease(t *testing.T) string {
	t.Helper()
	catalog, err := releases.CatalogOrError()
	if err != nil || len(catalog) == 0 {
		t.Fatalf("release catalog: %v (%d releases)", err, len(catalog))
	}
	return catalog[len(catalog)-1].Version
}

// ybRoleShippedTag is FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG or, like the Makefile's default, the newest final vX.Y.Z tag
// reachable from HEAD.
func ybRoleShippedTag(t *testing.T, repo string) string {
	t.Helper()
	if tag := strings.TrimSpace(os.Getenv("FRAMEWORKS_SCHEMA_VERIFY_FROM_TAG")); tag != "" {
		return tag
	}
	out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "tag", "--merged", "HEAD", "--sort=-v:refname").Output()
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	for _, tag := range strings.Fields(string(out)) {
		if releases.ValidateVersion(tag) == nil && releases.BaseVersion(tag) == tag {
			return tag
		}
	}
	t.Fatal("no final vX.Y.Z tag is reachable from HEAD")
	return ""
}

// ybRoleTaggedSchemaItems renders each database's baseline as of tag, with the database's current layout, as schema
// items. A database the tag has no baseline for is created by the release from its current baseline.
func ybRoleTaggedSchemaItems(t *testing.T, repo, tag string, names []string) []map[string]any {
	t.Helper()
	dir := t.TempDir()
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		file := "pkg/database/sql/schema/" + name + ".sql"
		baseline, err := exec.CommandContext(t.Context(), "git", "-C", repo, "show", tag+":"+file).Output()
		if err != nil {
			current, readErr := os.ReadFile(filepath.Join(repo, file))
			if readErr != nil {
				t.Fatalf("%s has no baseline at %s and none now: %v", name, tag, readErr)
			}
			baseline = current
		}
		rendered, err := yugabyteSQLForSource(name, string(baseline))
		if err != nil {
			t.Fatalf("render %s baseline at %s: %v", name, tag, err)
		}
		path := filepath.Join(dir, name+".sql")
		if err = os.WriteFile(path, []byte(rendered), 0o600); err != nil {
			t.Fatal(err)
		}
		items = append(items, map[string]any{"db": name, "schema": name, "owner": name, "runtime_role": name + "_runtime", "src": filepath.ToSlash(path), "reapply": false})
	}
	return items
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
