package provisioner

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// decodedComposeFiles returns the compose_stack_files a role vars map carries,
// decoded as the compose_stack role decodes them.
func decodedComposeFiles(t *testing.T, vars map[string]any) map[string]string {
	t.Helper()
	encoded, ok := vars["compose_stack_files"].(map[string]string)
	if !ok {
		t.Fatalf("compose_stack_files got %T, want map[string]string", vars["compose_stack_files"])
	}
	files := make(map[string]string, len(encoded))
	for path, value := range encoded {
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			t.Fatalf("compose_stack_files[%s] is not base64: %v", path, err)
		}
		files[path] = string(raw)
	}
	return files
}

func decodedComposeFilesAny(t *testing.T, vars map[string]any) map[string]any {
	t.Helper()
	files := map[string]any{}
	for path, content := range decodedComposeFiles(t, vars) {
		files[path] = content
	}
	return files
}

// A stack file that contains Jinja syntax must reach the host unchanged.
func TestComposeStackFilesSurviveAnsibleTemplating(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not available")
	}
	content := `<PatternLayout pattern="%date %level %notEmpty{%X}%n"/> {{ not_a_var }} {% raw %}`
	files := composeStackFiles(map[string]string{"metabase/log4j2.xml": content})
	dir := t.TempDir()
	out := filepath.Join(dir, "out.xml")
	playbook := filepath.Join(dir, "p.yml")
	if err := os.WriteFile(playbook, []byte(`- hosts: localhost
  gather_facts: false
  tasks:
    - ansible.builtin.copy:
        dest: "`+out+`"
        content: "{{ item.value | b64decode }}"
      loop: "{{ compose_stack_files | dict2items }}"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	vars := filepath.Join(dir, "vars.json")
	encodedVars, err := json.Marshal(map[string]any{"compose_stack_files": files})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(vars, encodedVars, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "ansible-playbook", "-i", "localhost,", "-c", "local", playbook, "-e", "@"+vars)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("ansible-playbook: %v\n%s", runErr, output)
	}
	gotBytes, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(gotBytes); got != content {
		t.Fatalf("file content = %q, want %q", got, content)
	}
}
