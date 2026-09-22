package provisioner

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type roleTask struct {
	Name  string         `yaml:"name"`
	Query map[string]any `yaml:"community.postgresql.postgresql_query"`
	Until string         `yaml:"until"`
	Loop  string         `yaml:"loop"`
	Vars  map[string]any `yaml:"vars"`
}

// TestMigrationRolesBoundOnlyTransactionalItemsAndRepairInvalidIndexes pins the migration apply contract in both
// roles: timeouts and lock-timeout replays apply to transactional items only, the replay matches the server's
// lock-timeout error rather than any message containing the words, and an invalid index left by an interrupted
// concurrent build is repaired by its own item inside the migration advisory lock. A build in progress is invalid too,
// so a repair outside the lock could drop an index another migrator is still building; inside it, the item also
// rechecks the ledger so an index another migrator finished is kept.
func TestMigrationRolesBoundOnlyTransactionalItemsAndRepairInvalidIndexes(t *testing.T) {
	for _, role := range []string{"postgres", "yugabyte"} {
		t.Run(role, func(t *testing.T) {
			var tasks []roleTask
			if err := yaml.Unmarshal([]byte(databaseRoleTaskFile(t, role, "migrate.yml")), &tasks); err != nil {
				t.Fatalf("parse migrate.yml: %v", err)
			}
			apply := -1
			for i, task := range tasks {
				if strings.Contains(strings.ToLower(task.Name), "invalid index") {
					t.Fatalf("task %q repairs indexes outside the locked apply", task.Name)
				}
				if query, _ := task.Query["query"].(string); strings.Contains(query, "DROP INDEX") && !strings.HasPrefix(task.Name, "Apply pending migrations") {
					t.Fatalf("task %q drops indexes outside the locked apply", task.Name)
				}
				if strings.HasPrefix(task.Name, "Apply pending migrations") {
					apply = i
				}
			}
			if apply < 0 {
				t.Fatal("no apply task")
			}
			applyTask := tasks[apply]
			query, _ := applyTask.Query["query"].(string)
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
