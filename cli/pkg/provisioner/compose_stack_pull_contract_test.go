package provisioner

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func ansibleTreePath(t *testing.T, parts ...string) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(append([]string{filepath.Dir(current), "..", "..", "..", "ansible"}, parts...)...)
}

func composeStackRolePath(t *testing.T, parts ...string) string {
	t.Helper()
	return ansibleTreePath(t, append([]string{"collections", "ansible_collections", "frameworks", "infra", "roles", "compose_stack"}, parts...)...)
}

// runComposePull runs the role's pull script against a fake docker that fails
// its first `failures` invocations with failMessage, then succeeds.
func runComposePull(t *testing.T, failures int, failMessage, policy string) (exitCode int, stdout string, calls int, args string) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	arguments := filepath.Join(dir, "args")
	fakeDocker := "#!/bin/sh\n" +
		"echo \"$*\" > " + arguments + "\n" +
		"n=$(cat " + counter + " 2>/dev/null || echo 0)\n" +
		"n=$((n + 1))\n" +
		"echo \"$n\" > " + counter + "\n" +
		"if [ \"$n\" -le " + strconv.Itoa(failures) + " ]; then\n" +
		"  echo ' Pulling '\n" +
		"  echo '" + failMessage + "' >&2\n" +
		"  exit 18\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), "sh", composeStackRolePath(t, "files", "compose-pull.sh"), "/opt/frameworks/chartroom", "3", policy)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "COMPOSE_PULL_BACKOFF_SECONDS=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	exitCode = 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run compose-pull.sh: %v", err)
	}
	raw, _ := os.ReadFile(counter)
	calls, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	rawArgs, _ := os.ReadFile(arguments)
	return exitCode, out.String(), calls, strings.TrimSpace(string(rawArgs))
}

// The rc5 staging rollout lost chartroom on two hosts to one DNS timeout during
// the pull; a transient failure must be retried, not fail the service.
func TestComposePullRetriesATransientFailure(t *testing.T) {
	code, stdout, calls, _ := runComposePull(t, 1, "dial tcp: lookup production.cloudfront.docker.com on 127.0.0.53:53: i/o timeout", "policy")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 after a retry (stdout %q)", code, stdout)
	}
	if calls != 2 {
		t.Fatalf("docker compose pull ran %d times, want 2", calls)
	}
}

func TestComposePullReportsTheCauseWhenEveryAttemptFails(t *testing.T) {
	cause := "dial tcp: lookup production.cloudfront.docker.com on 127.0.0.53:53: i/o timeout"
	code, stdout, calls, _ := runComposePull(t, 99, cause, "policy")
	if code == 0 {
		t.Fatal("exit = 0, want a failure when every pull fails")
	}
	if calls != 3 {
		t.Fatalf("docker compose pull ran %d times, want the 3 bounded attempts", calls)
	}
	if got := strings.TrimSpace(stdout); got != "pull failed: "+cause {
		t.Fatalf("stdout = %q, want the pull verdict with its cause", got)
	}
}

func TestComposePullPreservesPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy, want string
	}{
		{"policy", "pull --quiet"},
		{"missing", "pull --quiet --policy missing"},
		{"always", "pull --quiet --policy always"},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			code, _, calls, args := runComposePull(t, 0, "", tc.policy)
			if code != 0 || calls != 1 || !strings.HasSuffix(args, tc.want) {
				t.Fatalf("policy %s: code=%d calls=%d args=%q, want suffix %q", tc.policy, code, calls, args, tc.want)
			}
		})
	}
}

// A pull failure must be reported before the apply block, whose rescue can only
// say "failed to become healthy" with the old container's logs.
func TestComposeStackPullsBeforeApplying(t *testing.T) {
	data, err := os.ReadFile(composeStackRolePath(t, "tasks", "install.yml"))
	if err != nil {
		t.Fatal(err)
	}
	tasks := string(data)
	pull := strings.Index(tasks, "compose-pull.sh")
	pullFailure := strings.Index(tasks, "Fail when compose stack images cannot be pulled")
	apply := strings.Index(tasks, "community.docker.docker_compose_v2:")
	if pull < 0 || pullFailure < 0 || apply < 0 {
		t.Fatalf("install.yml must pull (%d), report pull failures (%d) and apply (%d)", pull, pullFailure, apply)
	}
	if pull >= apply || pullFailure >= apply {
		t.Fatal("images must be pulled, and pull failures reported, before the compose stack is applied")
	}
	if !strings.Contains(tasks, "pull: never") {
		t.Fatal("apply must not repeat the registry pull after the retried pre-pull")
	}
}

// Release applies over the VPN failed on a 12s sudo-prompt wait (Ansible's 10s
// default timeout + 2s) and on a single unreachable SSH attempt.
func TestAnsibleConfigToleratesSlowConnections(t *testing.T) {
	data, err := os.ReadFile(ansibleTreePath(t, "ansible.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(data)
	setting := func(section, key string) int {
		t.Helper()
		start := strings.Index(cfg, "["+section+"]")
		if start < 0 {
			t.Fatalf("ansible.cfg has no [%s] section", section)
		}
		body := cfg[start+len(section)+2:]
		if next := strings.Index(body, "\n["); next >= 0 {
			body = body[:next]
		}
		match := regexp.MustCompile(`(?m)^\s*` + key + `\s*=\s*(\d+)`).FindStringSubmatch(body)
		if match == nil {
			t.Fatalf("ansible.cfg [%s] does not set %s", section, key)
		}
		value, _ := strconv.Atoi(match[1])
		return value
	}
	if timeout := setting("defaults", "timeout"); timeout < 30 {
		t.Fatalf("[defaults] timeout = %d, want at least 30", timeout)
	}
	if retries := setting("ssh_connection", "retries"); retries < 3 {
		t.Fatalf("[ssh_connection] retries = %d, want at least 3", retries)
	}
}
