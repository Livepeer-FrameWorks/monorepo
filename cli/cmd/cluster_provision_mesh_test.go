package cmd

import (
	"strings"
	"testing"
)

func TestMeshAllowedIPsCommandSupportsUnprivilegedAutomationUser(t *testing.T) {
	got := meshAllowedIPsCommand()
	for _, want := range []string{
		`[ "$(id -u)" = 0 ]`,
		"wg show wg0 allowed-ips",
		"sudo -n wg show wg0 allowed-ips",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mesh allowed-ips command %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "|| true") {
		t.Fatalf("mesh allowed-ips command must not hide WireGuard permission errors: %q", got)
	}
}
