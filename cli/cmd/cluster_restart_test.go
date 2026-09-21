package cmd

import (
	"testing"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
)

func TestRestartResolvesEveryDeclaredServiceHost(t *testing.T) {
	m := &inventory.Manifest{
		Hosts:    map[string]inventory.Host{"a": {}, "b": {}},
		Services: map[string]inventory.ServiceConfig{"bridge": {Enabled: true, Host: "ignored", Hosts: []string{"a", "b", "a"}}},
	}
	hosts, err := resolveRestartHosts(m, "bridge")
	if err != nil || len(hosts) != 2 || hosts[0].Name != "a" || hosts[1].Name != "b" {
		t.Fatalf("hosts = %+v, %v", hosts, err)
	}
	delete(m.Hosts, "b")
	if _, err := resolveRestartHosts(m, "bridge"); err == nil {
		t.Fatal("unknown host accepted")
	}
}

// restart --validate gates on the release detected on the host; a host whose
// version cannot be read gates on liveness.
func TestProbeInstalledReleaseUsesDetectedVersion(t *testing.T) {
	base := provisioner.ServiceConfig{Version: "v0.3.11", Metadata: map[string]any{"version": "v0.3.11"}}
	got := probeInstalledRelease(base, &detect.ServiceState{Exists: true, Running: true, Version: "v0.3.10"})
	if !got.ProbeInstalled || got.InstalledVersion != "v0.3.10" {
		t.Fatalf("probeInstalledRelease = %+v, want the detected v0.3.10", got)
	}
	if got := probeInstalledRelease(base, nil); !got.ProbeInstalled || got.InstalledVersion != "" {
		t.Fatalf("without detection = %+v, want ProbeInstalled with no version", got)
	}
}

// The command timeout covers the full rollout readiness wait plus the
// restart itself.
func TestRestartCommandTimeoutExceedsRolloutReadiness(t *testing.T) {
	if restartCommandTimeout <= provisioner.RolloutReadinessTimeout {
		t.Fatalf("restartCommandTimeout = %s, must exceed RolloutReadinessTimeout %s", restartCommandTimeout, provisioner.RolloutReadinessTimeout)
	}
}
