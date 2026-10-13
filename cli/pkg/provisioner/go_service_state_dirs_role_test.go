package provisioner

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

// goServiceInstallTasks returns the named tasks of the go_service role's
// install.yml, in file order, as YAML nodes for a test playbook.
func goServiceInstallTasks(t *testing.T, names ...string) []any {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	path := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "roles", "go_service", "tasks", "install.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tasks []map[string]any
	if err := yaml.Unmarshal(raw, &tasks); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	var out []any
	for _, task := range tasks {
		if name, _ := task["name"].(string); want[name] {
			out = append(out, task)
			delete(want, name)
		}
	}
	if len(want) > 0 {
		t.Fatalf("install.yml lacks tasks %v", want)
	}
	return out
}

// A Livepeer gateway renders its keystore and password files inside its state
// directories. Applying the role twice must leave those directories converged:
// a second run (and so `cluster diff` after `release apply`) reports no change.
func TestGoServiceLivepeerStateDirsConvergeWithRenderedFiles(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not available")
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	grp, err := user.LookupGroupId(me.Gid)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	stateDir := filepath.Join(dir, "livepeer-gateway")
	stateDirs := genericServiceRoleConfig("livepeer-gateway", 8935).StateDirs
	for i, d := range stateDirs {
		stateDirs[i] = filepath.Join(stateDir, filepath.Base(d))
	}
	stateDirs[0] = stateDir
	env := map[string]string{
		"LIVEPEER_ETH_KEYSTORE_B64":      base64.StdEncoding.EncodeToString([]byte(`{"address":"00"}`)),
		"LIVEPEER_ETH_KEYSTORE_PASSWORD": "secret",
	}
	files, _, _, err := livepeerNativeFiles(env, stateDirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("rendered files = %v, want keystore and password", files)
	}

	tasks := goServiceInstallTasks(t, "Ensure service state directories", "Ensure service file directories", "Render service files")
	playbook, err := yaml.Marshal([]map[string]any{{"hosts": "localhost", "gather_facts": false, "tasks": tasks}})
	if err != nil {
		t.Fatal(err)
	}
	playbookPath := filepath.Join(dir, "p.yml")
	if err = os.WriteFile(playbookPath, playbook, 0o600); err != nil {
		t.Fatal(err)
	}
	vars, err := json.Marshal(map[string]any{
		"go_service_user":       me.Username,
		"go_service_group":      grp.Name,
		"go_service_state_dirs": stateDirs,
		"go_service_files":      files,
	})
	if err != nil {
		t.Fatal(err)
	}
	varsPath := filepath.Join(dir, "vars.json")
	if err = os.WriteFile(varsPath, vars, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) string {
		args := append([]string{"-i", "localhost,", "-c", "local", playbookPath, "-e", "@" + varsPath}, extra...)
		cmd := exec.CommandContext(t.Context(), "ansible-playbook", args...)
		cmd.Env = append(os.Environ(), "ANSIBLE_NOCOLOR=1")
		output, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("ansible-playbook %v: %v\n%s", extra, runErr, output)
		}
		return string(output)
	}

	run()
	recap := regexp.MustCompile(`localhost\s*:\s*ok=\d+\s+changed=(\d+)`)
	for _, mode := range [][]string{nil, {"--check"}} {
		output := run(mode...)
		m := recap.FindStringSubmatch(output)
		if m == nil {
			t.Fatalf("no play recap in output:\n%s", output)
		}
		if m[1] != "0" {
			t.Fatalf("second run %v changed %s task(s); the state directories do not converge:\n%s", mode, m[1], output)
		}
	}
	for _, d := range stateDirs {
		info, statErr := os.Stat(d)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if got := info.Mode().Perm(); got != 0o750 {
			t.Fatalf("%s mode = %o, want 0750", d, got)
		}
	}
}
