package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
)

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
