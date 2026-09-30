package templates

import (
	"os"
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

// mistLaunchSurfaces returns every surface that launches MistController on
// an edge: the container image's s6 run script, the native systemd unit and
// the native macOS launchd wrapper.
func mistLaunchSurfaces(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"container s6 run":       readFile(t, repositoryPath(t, "edge/rootfs/etc/s6-overlay/s6-rc.d/mistserver/run")),
		"native systemd unit":    readFile(t, repositoryPath(t, mistserverRoleDir+"/templates/mistserver.service.j2")),
		"native launchd wrapper": readFile(t, repositoryPath(t, mistserverRoleDir+"/tasks/configure-darwin.yml")),
	}
}

// TestMistControllerCommandLineCarriesNoCredential: the process list shows a
// command line to every local user, so no launch surface may pass the API
// account (-a user:password) or reference the password there.
func TestMistControllerCommandLineCarriesNoCredential(t *testing.T) {
	t.Parallel()

	for name, content := range mistLaunchSurfaces(t) {
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
}

// TestMistControllerReceivesAPIAccountFromEnvironment: MistController takes
// its API account from MIST_API_USERNAME/MIST_API_PASSWORD, so every launch
// path must put both in its environment from an owner-only secrets file and
// must not run a pre-start seed helper or strip the variables first.
func TestMistControllerReceivesAPIAccountFromEnvironment(t *testing.T) {
	t.Parallel()
	surfaces := mistLaunchSurfaces(t)

	for name, content := range surfaces {
		for _, forbidden := range []string{"python", "account-seed", "unset MIST_API", "env -u MIST_API"} {
			if strings.Contains(content, forbidden) {
				t.Errorf("%s must hand MistController the account through its environment, found %q:\n%s", name, forbidden, content)
			}
		}
	}
	if _, err := os.Stat(repositoryPath(t, mistserverRoleDir+"/files/mistserver-account-seed.py")); !os.IsNotExist(err) {
		t.Errorf("mistserver role still ships the account seed helper (stat err=%v)", err)
	}

	// Container: the compose env (secrets file + username) reaches the s6
	// run script because the image keeps the container environment.
	s6 := surfaces["container s6 run"]
	if !strings.HasPrefix(s6, "#!/command/with-contenv sh\n") {
		t.Errorf("s6 run script must run with the container environment:\n%s", s6)
	}
	if !strings.Contains(readFile(t, repositoryPath(t, "edge/Dockerfile")), "S6_KEEP_ENV=1") {
		t.Error("edge image must keep the container environment (S6_KEEP_ENV=1) for MistController")
	}
	vars := fixedEdgeVars()
	files, err := RenderEdgeTemplates(vars)
	if err != nil {
		t.Fatalf("RenderEdgeTemplates: %v", err)
	}
	secrets, ok := fileByPath(files, ".edge-secrets.env")
	if !ok {
		t.Fatal(".edge-secrets.env not rendered")
	}
	if !strings.Contains(string(secrets.Content), "\nMIST_API_PASSWORD="+vars.MistAPIPassword+"\n") || secrets.Mode != 0o600 {
		t.Errorf(".edge-secrets.env must carry MIST_API_PASSWORD with mode 0600 (mode %o):\n%s", secrets.Mode, secrets.Content)
	}
	goCompose, _ := fileByPath(files, "docker-compose.edge.yml")
	for name, compose := range map[string]string{
		"go container compose":      string(goCompose.Content),
		"ansible container compose": readFile(t, ansibleTemplatePath(t, "compose.yml.j2")),
	} {
		if !strings.Contains(compose, "      - .edge-secrets.env\n") || !strings.Contains(compose, "      - MIST_API_USERNAME=frameworks\n") {
			t.Errorf("%s must give the edge container the secrets env file and MIST_API_USERNAME:\n%s", name, compose)
		}
	}
	ansibleSecrets := readFile(t, repositoryPath(t, "ansible/collections/ansible_collections/frameworks/infra/roles/edge/tasks/install-container.yml"))
	if !regexp.MustCompile(`dest: "\{\{ edge_base_dir \}\}/\.edge-secrets\.env"\n    content: \|\n      MIST_API_PASSWORD=\{\{ edge_effective_mist_api_password \}\}\n    mode: "0600"`).MatchString(ansibleSecrets) {
		t.Error("ansible container install must write MIST_API_PASSWORD to .edge-secrets.env with mode 0600")
	}

	// Native: both env files carry the account and are owner-only.
	envFileLines := "      MIST_API_USERNAME={{ mistserver_api_user }}\n      MIST_API_PASSWORD={{ mistserver_api_password }}\n"
	for name, path := range map[string]string{
		"linux env file":  mistserverRoleDir + "/tasks/configure-linux.yml",
		"darwin env file": mistserverRoleDir + "/tasks/configure-darwin.yml",
	} {
		tasks := readFile(t, repositoryPath(t, path))
		start := strings.Index(tasks, "- name: Render env file\n")
		if start < 0 {
			t.Fatalf("%s: no env file task", name)
		}
		task := tasks[start:]
		if end := strings.Index(task[1:], "\n- name:"); end >= 0 {
			task = task[:end+1]
		}
		if !strings.Contains(task, envFileLines) || !strings.Contains(task, `mode: "0600"`) || !strings.Contains(task, "no_log: true") {
			t.Errorf("%s must write MIST_API_USERNAME/MIST_API_PASSWORD owner-only:\n%s", name, task)
		}
	}

	// systemd loads the env file into MistController's environment.
	unit := surfaces["native systemd unit"]
	envAt := strings.Index(unit, "EnvironmentFile={{ mistserver_env_file }}\n")
	if envAt < 0 || envAt > strings.Index(unit, "ExecStart=") {
		t.Errorf("systemd unit must load the env file for MistController:\n%s", unit)
	}

	// launchd: the wrapper exports the env file, then execs MistController
	// (the plist itself is world-readable and carries no secret).
	darwin := surfaces["native launchd wrapper"]
	exportAt := strings.Index(darwin, "*=*) export \"$line\" ;;")
	readAt := strings.Index(darwin, "done < \"{{ mistserver_darwin_conf_dir }}/mistserver.env\"")
	execAt := strings.Index(darwin, "exec \"{{ mistserver_darwin_base_dir }}/mistserver/bin/MistController\"")
	if exportAt < 0 || readAt < 0 || execAt < 0 || exportAt > execAt || readAt > execAt {
		t.Errorf("launchd wrapper must export the env file before exec:\n%s", darwin)
	}
}
