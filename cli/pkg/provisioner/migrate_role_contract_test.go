package provisioner

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type roleTask struct {
	Name    string         `yaml:"name"`
	Query   map[string]any `yaml:"community.postgresql.postgresql_query"`
	Apply   map[string]any `yaml:"frameworks.infra.yugabyte_migration_apply"`
	Include map[string]any `yaml:"ansible.builtin.include_tasks"`
	Fail    map[string]any `yaml:"ansible.builtin.fail"`
	Until   string         `yaml:"until"`
	Loop    string         `yaml:"loop"`
	When    any            `yaml:"when"`
	NoLog   bool           `yaml:"no_log"`
	Vars    map[string]any `yaml:"vars"`
}

func parseRoleTasks(t *testing.T, role, file string) []roleTask {
	t.Helper()
	var tasks []roleTask
	if err := yaml.Unmarshal([]byte(databaseRoleTaskFile(t, role, file)), &tasks); err != nil {
		t.Fatalf("parse %s/%s: %v", role, file, err)
	}
	return tasks
}

func roleTaskIndex(tasks []roleTask, prefix string) int {
	for i, task := range tasks {
		if strings.HasPrefix(task.Name, prefix) {
			return i
		}
	}
	return -1
}

// TestMigrationRolesRepairInvalidIndexesOnlyInsideTheLockedApply pins that in both roles an invalid index left by an
// interrupted concurrent build is repaired only by its own item's apply, inside the migration advisory lock. A build
// in progress is invalid too, so a repair outside the lock could drop an index another migrator is still building;
// inside it, the item also rechecks the ledger so an index another migrator finished is kept.
func TestMigrationRolesRepairInvalidIndexesOnlyInsideTheLockedApply(t *testing.T) {
	for _, role := range []string{"postgres", "yugabyte"} {
		t.Run(role, func(t *testing.T) {
			for _, file := range []string{"migrate.yml", "migrate_item.yml"} {
				for _, task := range parseRoleTasks(t, role, file) {
					if strings.Contains(strings.ToLower(task.Name), "invalid index") {
						t.Fatalf("%s task %q repairs indexes outside the locked apply", file, task.Name)
					}
					if query, _ := task.Query["query"].(string); strings.Contains(query, "DROP INDEX") && !strings.HasPrefix(task.Name, "Apply migration") {
						t.Fatalf("%s task %q drops indexes outside the locked apply", file, task.Name)
					}
				}
			}
		})
	}
}

// TestPostgresMigrationRoleAppliesEachItemInOneLockedTransaction pins the PostgreSQL apply: one transaction per
// transactional item, the repair between its own timeouts inside the advisory lock, item timeouts for transactional
// items only, and a whole-item replay only on the server's lock-timeout error rather than any message containing the
// words.
func TestPostgresMigrationRoleAppliesEachItemInOneLockedTransaction(t *testing.T) {
	for _, role := range []string{"postgres"} {
		t.Run(role, func(t *testing.T) {
			itemTasks := parseRoleTasks(t, role, "migrate_item.yml")
			apply := roleTaskIndex(itemTasks, "Apply migration")
			if apply < 0 {
				t.Fatal("no apply task")
			}
			applyTask := itemTasks[apply]
			query, _ := applyTask.Query["query"].(string)
			if autocommit := fmt.Sprint(applyTask.Query["autocommit"]); autocommit != "{{ not (item.transactional | default(true)) }}" {
				t.Fatalf("apply autocommit = %s, want one transaction for a transactional item", autocommit)
			}
			lockAt := strings.Index(query, "pg_advisory_lock(")
			repairAt := strings.Index(query, "invalid_index_repair_sql,")
			statementsAt := strings.Index(query, "+ item.statements")
			if lockAt < 0 || repairAt < 0 || statementsAt < 0 || lockAt > repairAt || repairAt > statementsAt {
				t.Fatalf("apply query runs the repair at %d, the lock at %d, and the statements at %d; want lock, repair, statements:\n%s", repairAt, lockAt, statementsAt, query)
			}
			repair, _ := applyTask.Vars["invalid_index_repair_sql"].(string)
			// A plain DROP INDEX takes an ACCESS EXCLUSIVE table lock, so the repair runs between its own timeouts. A
			// statement's timeout is armed when it starts, so they are set before the repair and reset before the
			// concurrent build that follows.
			order := []string{"SET statement_timeout = '60s'", "SET lock_timeout = '5s'", "invalid_index_repair_sql,", "RESET lock_timeout", "RESET statement_timeout", "+ item.statements"}
			at := -1
			for _, step := range order {
				next := strings.Index(query, step)
				if next <= at {
					t.Fatalf("apply query runs %q out of order (want %v):\n%s", step, order, query)
				}
				at = next
			}
			for _, want := range []string{
				"IF NOT EXISTS (SELECT 1 FROM _migrations",
				"version = '{{ item.version }}' AND phase = '{{ item.phase }}' AND seq = {{ item.sequence }}",
				"WHERE NOT i.indisvalid",
				"item.invalid_index_guards",
				"EXECUTE 'DROP INDEX IF EXISTS ' || target",
			} {
				if !strings.Contains(repair, want) {
					t.Fatalf("invalid-index repair lacks %q:\n%s", want, repair)
				}
			}
			if !strings.Contains(query, `(item.transactional | default(true)) | ternary(["SET lock_timeout = '5s'", "SET statement_timeout = '15min'"], [])`) {
				t.Fatalf("apply query does not limit timeouts to transactional items:\n%s", query)
			}
			// lock_timeout appears in the transactional branch and in the repair's own set and reset, nowhere else.
			if strings.Count(query, "lock_timeout") != 3 {
				t.Fatalf("apply query sets lock_timeout outside the transactional branch and the repair:\n%s", query)
			}
			until := applyTask.Until
			if !strings.Contains(until, "or not (item.transactional | default(true))") ||
				!strings.Contains(until, "'canceling statement due to lock timeout' not in") {
				t.Fatalf("apply until = %q, want replays only for transactional lock timeouts", until)
			}
		})
	}
}

// TestYugabyteMigrationRoleAppliesStatementByStatement pins the YugabyteDB apply: the role hands each item to
// frameworks.infra.yugabyte_migration_apply, which holds the advisory lock on one autocommit session, repairs invalid
// guarded indexes between their own timeouts, runs the statements one at a time as the owner with the item timeouts and
// a per-statement replay for transactional items only, verifies guarded indexes, and writes the ledger row last.
func TestYugabyteMigrationRoleAppliesStatementByStatement(t *testing.T) {
	itemTasks := parseRoleTasks(t, "yugabyte", "migrate_item.yml")
	apply := roleTaskIndex(itemTasks, "Apply migration")
	if apply < 0 {
		t.Fatal("no apply task")
	}
	task := itemTasks[apply]
	if task.Apply == nil || task.Query != nil || task.Until != "" {
		t.Fatalf("apply task must call frameworks.infra.yugabyte_migration_apply without a whole-item replay: %+v", task)
	}
	for key, want := range map[string]string{
		"db":                   "{{ item.db }}",
		"owner":                "{{ item.owner }}",
		"statements":           "{{ item.statements }}",
		"bounded":              "{{ item.transactional | default(true) }}",
		"lock_timeout":         "5s",
		"statement_timeout":    "15min",
		"retries":              "6",
		"retry_delay":          "10",
		"invalid_index_repair": "{{ (item.invalid_index_guards | default([]) | length > 0) | ternary(invalid_index_repair_sql, '') }}",
		"invalid_index_verify": "{{ (item.invalid_index_guards | default([]) | length > 0) | ternary(invalid_index_verify_sql, '') }}",
		"ledger_insert":        "{{ migration_ledger_sql }}",
	} {
		if got := fmt.Sprint(task.Apply[key]); got != want {
			t.Errorf("apply %s = %q, want %q", key, got, want)
		}
	}
	if !strings.Contains(fmt.Sprint(task.When), "not ansible_check_mode") {
		t.Fatalf("apply runs in check mode: when %v", task.When)
	}
	repair, _ := task.Vars["invalid_index_repair_sql"].(string)
	for _, want := range []string{
		"IF NOT EXISTS (SELECT 1 FROM _migrations",
		"version = '{{ item.version }}' AND phase = '{{ item.phase }}' AND seq = {{ item.sequence }}",
		"WHERE NOT i.indisvalid",
		"item.invalid_index_guards",
		"EXECUTE 'DROP INDEX IF EXISTS ' || target",
	} {
		if !strings.Contains(repair, want) {
			t.Fatalf("invalid-index repair lacks %q:\n%s", want, repair)
		}
	}
	if ledger, _ := task.Vars["migration_ledger_sql"].(string); !strings.Contains(ledger, "INSERT INTO _migrations") {
		t.Fatalf("ledger insert = %q", ledger)
	}

	module := yugabyteMigrationModuleSource(t)
	if !strings.Contains(module, "connect_to_db(module, conn_params, autocommit=True)") {
		t.Fatal("the module must run every statement in autocommit")
	}
	order := []string{
		"pg_advisory_lock(hashtext('frameworks_migrations'), hashtext(%s))",
		`run("SET statement_timeout = '60s'")`,
		`run("SET lock_timeout = '5s'")`,
		`run(params["invalid_index_repair"])`,
		`run("RESET lock_timeout")`,
		`run("RESET statement_timeout")`,
		`if params["bounded"]:`,
		"set_config('lock_timeout', %s, false)",
		"set_config('statement_timeout', %s, false)",
		`run('SET ROLE "%s"'`,
		`for index, statement in enumerate(params["statements"], start=1):`,
		"run(statement)",
		`if not params["bounded"] or attempt >= params["retries"] or not is_retryable(exc):`,
		`run("RESET ROLE")`,
		`run(params["invalid_index_verify"])`,
		`run(params["ledger_insert"])`,
		"pg_advisory_unlock(hashtext('frameworks_migrations'), hashtext(%s))",
	}
	at := -1
	for _, step := range order {
		next := strings.Index(module, step)
		if next <= at {
			t.Fatalf("module runs %q out of order (want %q)", step, order)
		}
		at = next
	}
	if !strings.Contains(module, `RETRYABLE_SQLSTATES = ("55P03", "40001", "40P01")`) {
		t.Fatal("the module must replay a statement only on a lock timeout, serialization failure, or deadlock")
	}
}

func yugabyteMigrationModuleSource(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "plugins", "modules", "yugabyte_migration_apply.py")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestMigrationRolesRunEachPrecheckReadOnlyImmediatelyBeforeItsItem pins where prechecks run: migrate.yml applies
// pending items one at a time through migrate_item.yml, carrying the migrate tag into it, and never runs a precheck
// itself, so no precheck runs ahead of the items before it. Inside the item, the precheck runs in a READ ONLY
// transaction, a visible fail task refuses the item with the offending rows, and only then does the apply start.
func TestMigrationRolesRunEachPrecheckReadOnlyImmediatelyBeforeItsItem(t *testing.T) {
	for _, role := range []string{"postgres", "yugabyte"} {
		t.Run(role, func(t *testing.T) {
			tasks := parseRoleTasks(t, role, "migrate.yml")
			include := roleTaskIndex(tasks, "Apply pending migrations")
			if include < 0 {
				t.Fatal("migrate.yml has no apply include")
			}
			includeTask := tasks[include]
			if includeTask.Include["file"] != "migrate_item.yml" {
				t.Fatalf("apply task includes %v, want migrate_item.yml", includeTask.Include)
			}
			apply, _ := includeTask.Include["apply"].(map[string]any)
			if tags := fmt.Sprint(apply["tags"]); tags != "[migrate]" {
				t.Fatalf("included item tasks carry tags %s, want [migrate]: tags on an include do not reach its tasks", tags)
			}
			if want := "{{ " + role + "_pending_migrate_items | default([]) }}"; includeTask.Loop != want {
				t.Fatalf("apply include loops over %q, want %q", includeTask.Loop, want)
			}
			if !strings.Contains(fmt.Sprint(includeTask.When), "not ansible_check_mode") {
				t.Fatalf("apply include runs in check mode: when %v", includeTask.When)
			}
			if strings.Contains(databaseRoleTaskFile(t, role, "migrate.yml"), "precheck_query") {
				t.Fatal("migrate.yml references prechecks; they run only inside migrate_item.yml, immediately before their item")
			}

			itemTasks := parseRoleTasks(t, role, "migrate_item.yml")
			precheck := roleTaskIndex(itemTasks, "Run migration precheck")
			refuse := roleTaskIndex(itemTasks, "Refuse migration its precheck rejects")
			applyItem := roleTaskIndex(itemTasks, "Apply migration")
			if precheck != 0 || refuse != 1 || applyItem != 2 || len(itemTasks) != 3 {
				t.Fatalf("migrate_item.yml runs precheck %d, refusal %d, apply %d of %d tasks; want exactly precheck, refusal, apply", precheck, refuse, applyItem, len(itemTasks))
			}
			precheckTask := itemTasks[precheck]
			queries, _ := precheckTask.Query["query"].([]any)
			if len(queries) != 2 || queries[0] != "SET TRANSACTION READ ONLY" || queries[1] != "{{ item.precheck_query }}" {
				t.Fatalf("precheck queries = %#v, want SET TRANSACTION READ ONLY then the item's precheck in one transaction", queries)
			}
			if _, autocommit := precheckTask.Query["autocommit"]; autocommit {
				t.Fatal("precheck sets autocommit; SET TRANSACTION READ ONLY must open the transaction the precheck runs in")
			}
			if when := fmt.Sprint(precheckTask.When); !strings.Contains(when, "item.precheck_query | default('') | length > 0") {
				t.Fatalf("precheck when = %s, want it to run only for items with a precheck", when)
			}

			refuseTask := itemTasks[refuse]
			if refuseTask.NoLog {
				t.Fatal("precheck refusal is no_log; the operator must see the offending rows")
			}
			msg, _ := refuseTask.Fail["msg"].(string)
			for _, want := range []string{"precheck_rows[0].precheck_total", "map(attribute='precheck_row')", "item.precheck_path", "item.filename"} {
				if !strings.Contains(msg, want) {
					t.Fatalf("precheck refusal message lacks %q:\n%s", want, msg)
				}
			}
			if rows := fmt.Sprint(refuseTask.Vars["precheck_rows"]); !strings.Contains(rows, role+"_migration_precheck.query_all_results[1]") {
				t.Fatalf("precheck refusal reads rows from %s, want the precheck's second query result", rows)
			}
		})
	}
}
