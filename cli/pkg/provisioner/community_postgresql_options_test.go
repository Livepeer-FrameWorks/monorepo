package provisioner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// community.postgresql 5.0.0 (pinned in ansible/requirements.yml, the first
// release without the ansible.module_utils._text/six imports ansible-core 2.24
// removes) dropped the login/host/unix_socket/port aliases of every module and
// the db/database aliases of login_db. A task still passing one fails with
// "Unsupported parameters".
var communityPostgresqlRemovedOptions = map[string]string{
	"login":       "login_user",
	"host":        "login_host",
	"unix_socket": "login_unix_socket",
	"port":        "login_port",
	"db":          "login_db",
	"database":    "login_db",
}

// postgresModuleRemovedOptions returns the removed option names a task passes
// to a community.postgresql module, or to our module built on its common
// argument spec (whose own db option is not the removed alias).
func postgresModuleRemovedOptions(module string, args *yaml.Node) []string {
	ours := module == "frameworks.infra.yugabyte_migration_apply"
	if !ours && !strings.HasPrefix(module, "community.postgresql.") && !strings.HasPrefix(module, "postgresql_") {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(args.Content); i += 2 {
		name := args.Content[i].Value
		replacement, removed := communityPostgresqlRemovedOptions[name]
		if !removed || (ours && name == "db") {
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s (use %s)", module, name, replacement))
	}
	return out
}

func walkPostgresModuleTasks(node *yaml.Node, report func(line int, msg string)) {
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			walkPostgresModuleTasks(child, report)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if value.Kind == yaml.MappingNode {
				for _, msg := range postgresModuleRemovedOptions(key.Value, value) {
					report(key.Line, msg)
				}
			}
			walkPostgresModuleTasks(value, report)
		}
	}
}

func communityPostgresqlRemovedOptionUses(t *testing.T, roots ...string) []string {
	t.Helper()
	var violations []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || (filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml") {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var doc yaml.Node
			if err := yaml.Unmarshal(body, &doc); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			rel := strings.TrimPrefix(path, "../../../")
			walkPostgresModuleTasks(&doc, func(line int, msg string) {
				violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, line, msg))
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	sort.Strings(violations)
	return violations
}

func TestAnsibleContentUsesCommunityPostgresql5Options(t *testing.T) {
	violations := communityPostgresqlRemovedOptionUses(t,
		"../../../ansible/collections/ansible_collections/frameworks/infra",
		"../../../ansible/playbooks",
	)
	if len(violations) > 0 {
		t.Fatalf("community.postgresql 5.0.0 removed these options:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCommunityPostgresqlRemovedOptionScanFlagsEachForm(t *testing.T) {
	dir := t.TempDir()
	body := `---
- name: Query with removed aliases
  community.postgresql.postgresql_query:
    port: 5432
    db: app
    login_user: postgres
- name: Our module keeps its own db option
  block:
    - name: Apply
      frameworks.infra.yugabyte_migration_apply:
        db: app
        host: 127.0.0.1
- name: Unrelated module
  ansible.builtin.uri:
    url: http://example.invalid
    port: 1
`
	if err := os.WriteFile(filepath.Join(dir, "tasks.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := communityPostgresqlRemovedOptionUses(t, dir)
	want := []string{
		dir + "/tasks.yml:10: frameworks.infra.yugabyte_migration_apply: host (use login_host)",
		dir + "/tasks.yml:3: community.postgresql.postgresql_query: db (use login_db)",
		dir + "/tasks.yml:3: community.postgresql.postgresql_query: port (use login_port)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("violations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
