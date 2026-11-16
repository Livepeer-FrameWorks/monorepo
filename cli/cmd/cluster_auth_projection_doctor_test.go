package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
)

func TestAuthProjectionDoctorUsesHostSSHForLoopbackAlias(t *testing.T) {
	bin := t.TempDir()
	called := filepath.Join(bin, "called")
	script := `#!/usr/bin/env bash
set -eu
if [ "${1:-}" = -G ]; then
  printf 'hostname 127.0.0.1\n'
  exit 0
fi
case "$*" in
  *projection-host*http://127.0.0.1:18027/metrics*) ;;
  *) exit 99 ;;
esac
touch "$AUTH_PROJECTION_SSH_CALLED"
printf 'auth_session_revocation_feed_connected 1\nauth_session_revocation_snapshot_synchronized 1\nauth_session_revocation_projection_ready 1\n\n200'
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AUTH_PROJECTION_SSH_CALLED", called)
	pool := fwssh.NewPool(time.Second, "")
	t.Cleanup(func() { _ = pool.Close() })
	result := doctorAuthProjection(context.Background(), pool, inventory.Host{Name: "projection-host", ExternalIP: "127.0.0.1"}, inventory.ServiceConfig{}, "foghorn", 18008)
	if _, err := os.Stat(called); err != nil {
		t.Fatalf("projection bypassed the manifest host's SSH transport: %+v", result)
	}
	if !result.OK {
		t.Fatalf("healthy host projection refused: %+v", result)
	}
}

func TestAuthProjectionDoctorRejectsFallbackOnlyOrUnhydratedReplica(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		connected, snapshot, ready int
		wantOK                     bool
	}{
		{"healthy", 1, 1, 1, true}, {"fallback only", 0, 1, 1, false}, {"cold", 1, 0, 0, false}, {"stale", 1, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf("auth_session_revocation_feed_connected %d\nauth_session_revocation_snapshot_synchronized %d\nauth_session_revocation_projection_ready %d\n", tc.connected, tc.snapshot, tc.ready)
			var commands []string
			runner, err := replyRunnerFor(func(_, _ string) (string, int) { return body + "\n200", 0 }, &commands)(inventory.Host{Name: "core"})
			if err != nil {
				t.Fatal(err)
			}
			result := doctorAuthProjectionWithRunner(context.Background(), runner, "127.0.0.1", 18003, &health.CheckResult{})
			if result.OK != tc.wantOK {
				t.Fatalf("unexpected projection result: %+v", result)
			}
			if len(commands) != 1 || !strings.Contains(commands[0], "/metrics") {
				t.Fatal("projection health was not actually inspected")
			}
		})
	}
	if err := validateAuthProjectionMetrics("go_goroutines 10\n"); err == nil {
		t.Fatal("missing projection instrumentation passed")
	}
}

func TestDoctorRegistersAuthProjectionForEveryBackendAlias(t *testing.T) {
	for _, service := range []string{"quartermaster", "purser", "periscope-query", "bosun", "lookout", "foghorn", "skipper"} {
		rc := &resolvedCluster{Manifest: &inventory.Manifest{Hosts: map[string]inventory.Host{"core": {Name: "core", ExternalIP: "192.0.2.1"}}, Services: map[string]inventory.ServiceConfig{"instance": {Enabled: true, Deploy: service, Host: "core"}}}}
		found := false
		for _, section := range doctorSections(rc, nil, nil, "", "", false) {
			for _, check := range section.checks {
				if strings.HasPrefix(check.name, "Session revocation projection") {
					found = true
					if outcome := check.run(context.Background()); outcome.passed {
						t.Fatal("unreachable replica passed its auth projection check")
					}
				}
			}
		}
		if !found {
			t.Fatalf("%s auth projection check absent", service)
		}
	}
}
