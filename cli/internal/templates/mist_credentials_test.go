package templates

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const mistserverRoleDir = "ansible/collections/ansible_collections/frameworks/infra/roles/mistserver"

// mistControllerInvocation returns the MistController command in a launch
// surface, joined across shell line continuations.
func mistControllerInvocation(t *testing.T, name, content string) string {
	t.Helper()
	joined := strings.ReplaceAll(content, "\\\n", " ")
	var found []string
	for _, line := range strings.Split(joined, "\n") {
		if strings.Contains(line, "bin/MistController") && !strings.Contains(line, "test -") && !strings.Contains(line, "path:") {
			found = append(found, line)
		}
	}
	if len(found) == 0 {
		t.Fatalf("%s: no MistController invocation found", name)
	}
	return strings.Join(found, "\n")
}

var mistAccountFlag = regexp.MustCompile(`(^|\s)(-a|--account)(\s|=|$)`)

// TestMistControllerCommandLineCarriesNoCredential covers every surface that
// launches MistController on an edge: the container image's s6 run script,
// the native systemd unit and the native macOS launchd wrapper. The process
// list shows a command line to every local user, so none of them may pass
// the API account (-a user:password) or reference the password there.
func TestMistControllerCommandLineCarriesNoCredential(t *testing.T) {
	t.Parallel()

	surfaces := map[string]string{
		"container s6 run":       readFile(t, repositoryPath(t, "edge/rootfs/etc/s6-overlay/s6-rc.d/mistserver/run")),
		"native systemd unit":    readFile(t, repositoryPath(t, mistserverRoleDir+"/templates/mistserver.service.j2")),
		"native launchd wrapper": readFile(t, repositoryPath(t, mistserverRoleDir+"/tasks/configure-darwin.yml")),
	}
	for name, content := range surfaces {
		invocation := mistControllerInvocation(t, name, content)
		if mistAccountFlag.MatchString(invocation) {
			t.Errorf("%s passes an account flag to MistController:\n%s", name, invocation)
		}
		if strings.Contains(invocation, "MIST_API_PASSWORD") || strings.Contains(invocation, "MIST_API_USERNAME") {
			t.Errorf("%s puts the Mist API credential on MistController's command line:\n%s", name, invocation)
		}
	}

	// The launchd plist runs the wrapper, and both container renderers (Go
	// and Ansible) launch the edge image, whose s6 run script is checked
	// above; none may start MistController themselves or carry the password.
	files, err := RenderEdgeTemplates(fixedEdgeVars())
	if err != nil {
		t.Fatalf("RenderEdgeTemplates: %v", err)
	}
	goCompose, ok := fileByPath(files, "docker-compose.edge.yml")
	if !ok {
		t.Fatal("docker-compose.edge.yml not rendered")
	}
	for name, content := range map[string]string{
		"native launchd plist":      readFile(t, repositoryPath(t, mistserverRoleDir+"/templates/mistserver.plist.j2")),
		"go container compose":      string(goCompose.Content),
		"ansible container compose": readFile(t, ansibleTemplatePath(t, "compose.yml.j2")),
	} {
		if strings.Contains(content, "MistController") || strings.Contains(content, "MIST_API_PASSWORD") {
			t.Errorf("%s must not launch MistController or carry the Mist API password:\n%s", name, content)
		}
	}

	// Each native surface seeds the account from the environment before
	// MistController starts.
	unit := surfaces["native systemd unit"]
	if !strings.Contains(unit, "ExecStartPre=+{{ mistserver_account_seed_python }} {{ mistserver_account_seed_script }} {{ mistserver_config_file }}") {
		t.Errorf("systemd unit must seed the Mist API account before start:\n%s", unit)
	}
	if strings.Index(unit, "mistserver_account_seed_script") > strings.Index(unit, "ExecStart=") {
		t.Error("systemd unit must seed the account before ExecStart")
	}
	darwin := surfaces["native launchd wrapper"]
	seedAt := strings.Index(darwin, "libexec/mistserver-account-seed.py\" \\")
	execAt := strings.Index(darwin, "exec \"{{ mistserver_darwin_base_dir }}/mistserver/bin/MistController\"")
	if seedAt < 0 || execAt < 0 || seedAt > execAt {
		t.Errorf("launchd wrapper must seed the Mist API account before exec:\n%s", darwin)
	}
}

// TestMistAccountSeedHelper runs the native pre-start helper against a
// config file and checks it stores the account the way MistController -a
// does (account.<user>.password = MD5 hex), keeps the rest of the config,
// writes mode 0600, leaves a matching config untouched, and refuses to run
// without a password.
func TestMistAccountSeedHelper(t *testing.T) {
	t.Parallel()
	python, lookErr := exec.LookPath("python3")
	if lookErr != nil {
		t.Skip("python3 not available")
	}
	script := repositoryPath(t, mistserverRoleDir+"/files/mistserver-account-seed.py")
	dir := t.TempDir()
	conf := filepath.Join(dir, "mistserver.conf")
	if err := os.WriteFile(conf, []byte(`{"config":{"controller":{"port":4242}},"streams":{"live":{"source":"push://"}},"account":{"ops":{"password":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(password string) error {
		cmd := exec.CommandContext(t.Context(), python, script, conf)
		cmd.Env = append(os.Environ(), "MIST_API_USERNAME=frameworks", "MIST_API_PASSWORD="+password)
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			return &seedError{err: runErr, out: string(out)}
		}
		return nil
	}

	if err := run("secret"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	raw := readFile(t, conf)
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("seeded config is not JSON: %v", err)
	}
	accounts, _ := data["account"].(map[string]any)
	frameworks, _ := accounts["frameworks"].(map[string]any)
	if frameworks["password"] != "5ebe2294ecd0e0f08eab7690d2a6ee69" {
		t.Fatalf("frameworks account = %v", accounts["frameworks"])
	}
	if accounts["ops"] == nil || data["streams"] == nil || data["config"] == nil {
		t.Fatalf("seed dropped existing config: %s", raw)
	}
	if strings.Contains(raw, "secret") {
		t.Fatal("seed wrote the plaintext password")
	}
	info, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("seeded config mode = %v, want 0600", info.Mode().Perm())
	}

	before, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if err = run("secret"); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	after, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || readFile(t, conf) != raw {
		t.Fatal("seed rewrote a config that already holds the account")
	}

	if err := run(""); err == nil {
		t.Fatal("seed must refuse an empty password")
	}
}

type seedError struct {
	err error
	out string
}

func (e *seedError) Error() string { return e.err.Error() + ": " + e.out }
