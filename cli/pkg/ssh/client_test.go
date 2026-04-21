package ssh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestBuildSSHArgs_DefaultAcceptNew(t *testing.T) {
	t.Parallel()
	cfg := &ConnectionConfig{Address: "1.2.3.4", User: "root", Port: 22}
	args := BuildSSHArgs(cfg, Resolution{Target: "root@1.2.3.4"})
	if !containsPair(args, "-o", "StrictHostKeyChecking=accept-new") {
		t.Errorf("expected accept-new by default; got %v", args)
	}
	if containsFlag(args, "-i") {
		t.Errorf("expected no -i when KeyPath is empty; got %v", args)
	}
	if containsFlag(args, "-l") {
		t.Errorf("BuildSSHArgs must never emit -l (user belongs in Resolution.Target or alias config); got %v", args)
	}
}

func TestBuildSSHArgs_KeyOverrideAlwaysWins(t *testing.T) {
	t.Parallel()
	cfg := &ConnectionConfig{Address: "1.2.3.4", User: "root", KeyPath: "/tmp/id_ed25519"}
	// Explicit --ssh-key must be honored even when targeting a verified alias.
	args := BuildSSHArgs(cfg, Resolution{Target: "frameworks-central-eu-1", AliasVerified: true})
	if !containsPair(args, "-i", "/tmp/id_ed25519") {
		t.Errorf("expected -i /tmp/id_ed25519 even with verified alias; got %v", args)
	}
}

func TestBuildSSHArgs_InsecureSkipVerifyRequiresBoth(t *testing.T) {
	t.Parallel()
	cfg := &ConnectionConfig{Address: "1.2.3.4", User: "root", InsecureSkipVerify: true}
	args := BuildSSHArgs(cfg, Resolution{Target: "root@1.2.3.4"})
	if !containsPair(args, "-o", "StrictHostKeyChecking=no") {
		t.Errorf("expected StrictHostKeyChecking=no; got %v", args)
	}
	// StrictHostKeyChecking=no alone still writes to known_hosts + warns;
	// /dev/null suppresses both.
	if !containsPair(args, "-o", "UserKnownHostsFile=/dev/null") {
		t.Errorf("expected UserKnownHostsFile=/dev/null alongside StrictHostKeyChecking=no; got %v", args)
	}
	if containsPair(args, "-o", "StrictHostKeyChecking=accept-new") {
		t.Errorf("accept-new must not coexist with insecure mode; got %v", args)
	}
}

func TestBuildSSHArgs_KnownHostsOverride(t *testing.T) {
	t.Parallel()
	cfg := &ConnectionConfig{Address: "1.2.3.4", KnownHostsPath: "/tmp/kh"}
	args := BuildSSHArgs(cfg, Resolution{Target: "root@1.2.3.4"})
	if !containsPair(args, "-o", "UserKnownHostsFile=/tmp/kh") {
		t.Errorf("expected UserKnownHostsFile=/tmp/kh; got %v", args)
	}
	if !containsPair(args, "-o", "StrictHostKeyChecking=accept-new") {
		t.Errorf("custom known_hosts should still use accept-new by default; got %v", args)
	}
}

func TestBuildSSHArgs_NonStandardPort(t *testing.T) {
	t.Parallel()
	cfg := &ConnectionConfig{Address: "1.2.3.4", Port: 2222}
	args := BuildSSHArgs(cfg, Resolution{Target: "root@1.2.3.4"})
	if !containsPair(args, "-p", "2222") {
		t.Errorf("expected -p 2222; got %v", args)
	}

	cfg.Port = 22
	args = BuildSSHArgs(cfg, Resolution{Target: "root@1.2.3.4"})
	if containsFlag(args, "-p") {
		t.Errorf("expected no -p for default port 22; got %v", args)
	}
}

func TestBuildSCPArgs_UsesCapitalP(t *testing.T) {
	t.Parallel()
	cfg := &ConnectionConfig{Address: "1.2.3.4", Port: 2222}
	args := BuildSCPArgs(cfg, Resolution{Target: "root@1.2.3.4"}, "/local", "/remote")
	if !containsPair(args, "-P", "2222") {
		t.Errorf("scp takes -P (capital), not -p; got %v", args)
	}
	// Final two args are source and destination
	if len(args) < 2 || args[len(args)-2] != "/local" || args[len(args)-1] != "root@1.2.3.4:/remote" {
		t.Errorf("expected trailing /local root@1.2.3.4:/remote; got %v", args)
	}
}

func TestParseSSHGHostname(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "user root\nhostname 1.2.3.4\nport 22\n", "1.2.3.4"},
		{"dns", "hostname prod-bastion.example.com\n", "prod-bastion.example.com"},
		{"with comments", "# comment\n\nhostname 9.9.9.9\n", "9.9.9.9"},
		{"missing", "user root\nport 22\n", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSSHGHostname(tc.in)
			if got != tc.want {
				t.Errorf("parseSSHGHostname(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClientRunUsesPortableShC(t *testing.T) {
	oldExec := execCommandContext
	execCommandContext = testExecCommandContext
	defer func() { execCommandContext = oldExec }()

	client := &Client{
		config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
		resolution: Resolution{Target: "root@1.2.3.4"},
	}

	result, err := client.Run(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Command is ShellQuoted so OpenSSH's space-rejoin doesn't split it.
	if !strings.Contains(result.Stdout, " sh -c 'echo hello'") {
		t.Fatalf("expected remote argv to use `sh -c 'echo hello'`, got %q", result.Stdout)
	}
	if strings.Contains(result.Stdout, " -lc ") {
		t.Fatalf("expected remote argv to avoid `-lc`, got %q", result.Stdout)
	}
}

// TestClientRunQuotesMultiWordCommand pins the argv-quoting fix for the
// production geoip provisioning failure: without ShellQuote, OpenSSH joined
// argv with spaces and the remote `sh -c` consumed only the first word,
// pushing the rest into positional params. The quoted form keeps the command
// as a single token across the wire.
func TestClientRunQuotesMultiWordCommand(t *testing.T) {
	oldExec := execCommandContext
	execCommandContext = testExecCommandContext
	defer func() { execCommandContext = oldExec }()

	client := &Client{
		config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
		resolution: Resolution{Target: "root@1.2.3.4"},
	}

	result, err := client.Run(context.Background(), "mkdir -p /var/lib/GeoIP")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if !strings.Contains(result.Stdout, " sh -c 'mkdir -p /var/lib/GeoIP'") {
		t.Fatalf("expected command to be single-quoted on the wire, got %q", result.Stdout)
	}
}

// TestClientRunErrorIncludesContext verifies the §2 enrichment: a failing
// remote command surfaces target, command, exit code, and stderr instead of
// a bare "exit status N".
func TestClientRunErrorIncludesContext(t *testing.T) {
	oldExec := execCommandContext
	execCommandContext = testFailingExecCommandContext(2, "mkdir: missing operand")
	defer func() { execCommandContext = oldExec }()

	client := &Client{
		config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
		resolution: Resolution{Target: "root@1.2.3.4"},
	}

	result, err := client.Run(context.Background(), "mkdir -p /var/lib/GeoIP")
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	msg := err.Error()
	for _, want := range []string{"root@1.2.3.4", "mkdir -p /var/lib/GeoIP", "exited 2", "mkdir: missing operand"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q; got: %s", want, msg)
		}
	}
	if result == nil || result.ExitCode != 2 {
		t.Errorf("expected result.ExitCode=2, got %+v", result)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Error("expected errors.As(err, &*exec.ExitError) to still unwrap")
	}
}

// TestWrapRunErrorFormat locks the human-readable shape so log-scraping and
// expected-output tests don't silently drift.
func TestWrapRunErrorFormat(t *testing.T) {
	cases := []struct {
		name     string
		exit     int
		stderr   string
		wantSubs []string
	}{
		{"exit and stderr", 1, "Permission denied", []string{"exited 1", "Permission denied"}},
		{"exit no stderr", 1, "", []string{"exited 1", "(no stderr)"}},
		{"spawn error with stderr", -1, "some noise", []string{"some noise"}},
		{"spawn error no stderr", -1, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := wrapRunError("root@1.2.3.4", "mkdir -p /x", tc.exit, tc.stderr, errors.New("exit status 1"))
			msg := err.Error()
			if !strings.Contains(msg, "root@1.2.3.4") || !strings.Contains(msg, "mkdir -p /x") {
				t.Errorf("error missing target or command: %s", msg)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(msg, sub) {
					t.Errorf("error missing %q; got: %s", sub, msg)
				}
			}
		})
	}
}

func TestWrapRunErrorCapsStderr(t *testing.T) {
	big := strings.Repeat("x", 5000)
	err := wrapRunError("root@1.2.3.4", "cmd", 1, big, errors.New("exit status 1"))
	msg := err.Error()
	if !strings.Contains(msg, "truncated") {
		t.Errorf("expected truncation marker in capped error; got: %s", msg[:200])
	}
	if len(msg) > 4000 {
		t.Errorf("error message still unbounded: %d bytes", len(msg))
	}
}

// --- helpers ---

func testExecCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcessSSH", "--", name}
	cs = append(cs, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

// testFailingExecCommandContext returns a fake exec.CommandContext that exits
// with the given code after emitting stderr. Used to exercise the error path
// in Client.Run without a real ssh invocation.
func testFailingExecCommandContext(exitCode int, stderr string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := []string{"-test.run=TestHelperProcessSSH", "--", name}
		cs = append(cs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			fmt.Sprintf("GO_HELPER_EXIT=%d", exitCode),
			"GO_HELPER_STDERR="+stderr,
		)
		return cmd
	}
}

func TestHelperProcessSSH(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep == -1 || sep+1 >= len(args) {
		os.Exit(2)
	}

	_, _ = os.Stdout.WriteString(strings.Join(args[sep+1:], " "))
	if stderr := os.Getenv("GO_HELPER_STDERR"); stderr != "" {
		_, _ = os.Stderr.WriteString(stderr)
	}
	exit := 0
	if s := os.Getenv("GO_HELPER_EXIT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			exit = n
		}
	}
	os.Exit(exit)
}

func containsFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
