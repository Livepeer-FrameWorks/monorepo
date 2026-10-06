package provisioner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ansible-core 2.24 removes three things our roles could use: facts injected
// as top-level ansible_* variables (INJECT_FACTS_AS_VARS), the internal "vars"
// dictionary, and the ansible.module_utils compatibility shims. Facts are read
// through ansible_facts, other variables directly or through lookup('vars').

// ansibleSpecialVariables are Ansible's magic and connection variables: the
// ansible_* names that are not facts and stay top-level.
var ansibleSpecialVariables = map[string]bool{
	"ansible_become": true, "ansible_become_exe": true, "ansible_become_flags": true,
	"ansible_become_method": true, "ansible_become_password": true, "ansible_become_user": true,
	"ansible_check_mode": true, "ansible_collection_name": true, "ansible_config_file": true,
	"ansible_connection": true, "ansible_dependent_role_names": true, "ansible_diff_mode": true,
	"ansible_facts": true, "ansible_failed_result": true, "ansible_failed_task": true,
	"ansible_forks": true, "ansible_host": true, "ansible_index_var": true,
	"ansible_inventory_sources": true, "ansible_limit": true, "ansible_loop": true,
	"ansible_loop_var": true, "ansible_parent_role_names": true, "ansible_parent_role_paths": true,
	"ansible_password": true, "ansible_pipelining": true, "ansible_play_batch": true,
	"ansible_play_hosts": true, "ansible_play_hosts_all": true, "ansible_play_name": true,
	"ansible_play_role_names": true, "ansible_playbook_python": true, "ansible_port": true,
	"ansible_private_key_file": true, "ansible_python_interpreter": true, "ansible_remote_tmp": true,
	"ansible_role_name": true, "ansible_role_names": true, "ansible_run_tags": true,
	"ansible_search_path": true, "ansible_shell_executable": true, "ansible_shell_type": true,
	"ansible_skip_tags": true, "ansible_ssh_common_args": true, "ansible_ssh_extra_args": true,
	"ansible_ssh_private_key_file": true, "ansible_timeout": true, "ansible_user": true,
	"ansible_verbosity": true, "ansible_version": true,
}

var (
	ansibleVarToken     = regexp.MustCompile(`\bansible_[a-z0-9_]+\b`)
	internalVarsDict    = regexp.MustCompile(`\bvars\s*(\[|\.[A-Za-z_])`)
	jinjaSpan           = regexp.MustCompile(`(?s)\{\{.*?\}\}|\{%.*?%\}`)
	deprecatedModuleUse = regexp.MustCompile(`ansible\.module_utils\.(_text|six|common\._collections_compat|compat\.)`)
)

// Keys whose YAML value is a bare Jinja expression.
var ansibleExpressionKeys = map[string]bool{
	"when": true, "failed_when": true, "changed_when": true, "until": true, "that": true,
}

func ansibleExpressionViolations(expr string) []string {
	var out []string
	for _, tok := range ansibleVarToken.FindAllString(expr, -1) {
		if !ansibleSpecialVariables[tok] {
			out = append(out, fmt.Sprintf("injected fact %s (use ansible_facts.%s)", tok, strings.TrimPrefix(tok, "ansible_")))
		}
	}
	if internalVarsDict.MatchString(expr) {
		out = append(out, "internal vars dictionary (use the variable or lookup('vars', ...))")
	}
	return out
}

func yamlExpressionViolations(node *yaml.Node, bareExpression bool, report func(line int, msg string)) {
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			yamlExpressionViolations(child, bareExpression, report)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			yamlExpressionViolations(value, ansibleExpressionKeys[key.Value], report)
		}
	case yaml.ScalarNode:
		exprs := jinjaSpan.FindAllString(node.Value, -1)
		if bareExpression {
			exprs = append(exprs, node.Value)
		}
		for _, expr := range exprs {
			for _, msg := range ansibleExpressionViolations(expr) {
				report(node.Line, msg)
			}
		}
	}
}

// ansibleCoreDeprecations lists every use of a feature ansible-core 2.24
// removes in our collection and playbooks.
func ansibleCoreDeprecations(t *testing.T, roots ...string) []string {
	t.Helper()
	var violations []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(path, "../../../")
			report := func(line int, msg string) {
				violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, line, msg))
			}
			switch filepath.Ext(path) {
			case ".yml", ".yaml":
				var doc yaml.Node
				if err := yaml.Unmarshal(body, &doc); err != nil {
					return fmt.Errorf("%s: %w", rel, err)
				}
				yamlExpressionViolations(&doc, false, report)
			case ".j2":
				for _, idx := range jinjaSpan.FindAllStringIndex(string(body), -1) {
					line := strings.Count(string(body[:idx[0]]), "\n") + 1
					for _, msg := range ansibleExpressionViolations(string(body[idx[0]:idx[1]])) {
						report(line, msg)
					}
				}
			case ".py":
				for i, line := range strings.Split(string(body), "\n") {
					if deprecatedModuleUse.MatchString(line) {
						report(i+1, "deprecated module_utils import (use ansible.module_utils.common.text.converters or the Python standard library)")
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	sort.Strings(violations)
	return violations
}

func TestAnsibleContentAvoidsFeaturesRemovedInAnsibleCore224(t *testing.T) {
	violations := ansibleCoreDeprecations(t,
		"../../../ansible/collections/ansible_collections/frameworks/infra",
		"../../../ansible/playbooks",
	)
	if len(violations) > 0 {
		t.Fatalf("ansible-core 2.24 removes these:\n%s", strings.Join(violations, "\n"))
	}
}

func TestAnsibleCoreDeprecationScanFlagsEachForm(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"tasks.yml": `---
- name: Uses an injected fact in a bare conditional
  ansible.builtin.debug:
    msg: "{{ ansible_facts.os_family }} {{ ansible_check_mode }}"
  when:
    - ansible_os_family == "Debian"
    - inventory_hostname in ansible_play_hosts
- name: Templates an injected fact and the internal vars dictionary
  ansible.builtin.set_fact:
    home: "{{ ansible_env.HOME }}"
    port: "{{ vars['service_port'] }}"
    marker: "ansible_pid=$$ is shell text, not a variable"
`,
		"template.j2": "host={{ ansible_hostname }}\nok={{ ansible_facts.hostname }}\n",
		"module.py":   "from ansible.module_utils._text import to_native\nfrom ansible.module_utils.common.text.converters import to_text\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := ansibleCoreDeprecations(t, dir)
	want := []string{
		dir + "/module.py:1: deprecated module_utils import (use ansible.module_utils.common.text.converters or the Python standard library)",
		dir + "/tasks.yml:10: injected fact ansible_env (use ansible_facts.env)",
		dir + "/tasks.yml:11: internal vars dictionary (use the variable or lookup('vars', ...))",
		dir + "/tasks.yml:6: injected fact ansible_os_family (use ansible_facts.os_family)",
		dir + "/template.j2:1: injected fact ansible_hostname (use ansible_facts.hostname)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("violations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
