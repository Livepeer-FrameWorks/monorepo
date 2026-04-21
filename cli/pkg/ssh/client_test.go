package ssh

import (
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

// --- helpers ---

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
