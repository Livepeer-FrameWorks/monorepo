package cmd

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"

	"github.com/spf13/cobra"
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

func restartFoghornCellManifest(cells ...string) *inventory.Manifest {
	m := &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.test",
		Hosts:      map[string]inventory.Host{},
		Clusters:   map[string]inventory.ClusterConfig{},
		Services:   map[string]inventory.ServiceConfig{},
	}
	for i, cell := range cells {
		cluster, host := "media-"+cell, "media-"+cell+"-1"
		m.Hosts[host] = inventory.Host{Name: host, ExternalIP: "10.0.1." + strconv.Itoa(i+1), Cluster: cluster}
		m.Clusters[cluster] = inventory.ClusterConfig{ControlCell: "cell-" + cell}
		m.Services["foghorn-"+cell] = inventory.ServiceConfig{
			Enabled: true, Deploy: "foghorn", Host: host, Cluster: cluster,
			Config: map[string]string{"DATABASE_URL": "postgres://foghorn_" + cell + "@db:5432/foghorn_" + cell},
		}
		m.Services["chandler-"+cell] = inventory.ServiceConfig{Enabled: true, Deploy: "chandler", Host: host, Cluster: cluster}
	}
	return m
}

func restartFoghornSharedEnv() map[string]string {
	return map[string]string{
		"SERVICE_TOKEN":                      "service-token",
		"MEDIA_AUTHORITY_SEAL_ROOT_SECRET":   strings.Repeat("ab", 32),
		"FOGHORN_STATE_ENCRYPTION_KEY":       strings.Repeat("cd", 32),
		"FOGHORN_BALANCER_CAPABILITY_SECRET": "balancer",
	}
}

// Restarting a Foghorn renders the planner's task for it, so on a manifest with several control cells each Foghorn
// receives its own cell's media-authority seal key, exactly as provision renders it.
func TestRestartRoleConfigScopesFoghornToItsCellOnMultiCellManifest(t *testing.T) {
	manifest := restartFoghornCellManifest("eu", "us")
	sharedEnv := restartFoghornSharedEnv()
	rc := resolvedClusterWithEnv(manifest, sharedEnv)
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	keyIDs := map[string]string{}
	for _, cell := range []string{"eu", "us"} {
		serviceName := "foghorn-" + cell
		host := manifest.Hosts["media-"+cell+"-1"]
		targets, err := buildServiceRoleTargets(&cobra.Command{}, rc, serviceName, "foghorn", host)
		if err != nil {
			t.Fatalf("restart role config for %s: %v", serviceName, err)
		}
		if len(targets) != 1 {
			t.Fatalf("restart role config for %s = %d targets, want 1", serviceName, len(targets))
		}
		got := targets[0]
		if got.task.ClusterID != "media-"+cell {
			t.Fatalf("restart task for %s has cluster %q, want media-%s", serviceName, got.task.ClusterID, cell)
		}
		if cellID := got.config.EnvVars["MEDIA_AUTHORITY_CELL_ID"]; cellID != "cell-"+cell {
			t.Fatalf("restart of %s renders MEDIA_AUTHORITY_CELL_ID %q, want cell-%s", serviceName, cellID, cell)
		}
		keyIDs[cell] = got.config.EnvVars["MEDIA_AUTHORITY_SEAL_KEY_ID"]

		var planned *orchestrator.Task
		for _, task := range plan.AllTasks {
			if task.ServiceID == serviceName {
				planned = task
			}
		}
		if planned == nil || !reflect.DeepEqual(got.task, planned) {
			t.Fatalf("restart of %s used task %+v, want the planner task %+v", serviceName, got.task, planned)
		}
		provisioned, err := renderTaskConfig(planned, manifest, false, map[string]any{}, ".", sharedEnv, nil, nil)
		if err != nil {
			t.Fatalf("provision render %s: %v", serviceName, err)
		}
		if !reflect.DeepEqual(provisioned.EnvVars, got.config.EnvVars) {
			t.Fatalf("%s restart env differs from provision:\nprovision %v\nrestart   %v", serviceName, provisioned.EnvVars, got.config.EnvVars)
		}
	}
	if keyIDs["eu"] == "" || keyIDs["eu"] == keyIDs["us"] {
		t.Fatalf("Foghorn seal keys not scoped per cell: eu=%q us=%q", keyIDs["eu"], keyIDs["us"])
	}
}

func TestRestartRoleConfigRendersFoghornOnSingleCellManifest(t *testing.T) {
	manifest := restartFoghornCellManifest("eu")
	rc := resolvedClusterWithEnv(manifest, restartFoghornSharedEnv())
	targets, err := buildServiceRoleTargets(&cobra.Command{}, rc, "foghorn-eu", "foghorn", manifest.Hosts["media-eu-1"])
	if err != nil {
		t.Fatalf("restart role config: %v", err)
	}
	if len(targets) != 1 || targets[0].task.ClusterID != "media-eu" {
		t.Fatalf("restart targets = %+v, want one task in media-eu", targets)
	}
	if cellID := targets[0].config.EnvVars["MEDIA_AUTHORITY_CELL_ID"]; cellID != "cell-eu" {
		t.Fatalf("MEDIA_AUTHORITY_CELL_ID = %q, want cell-eu", cellID)
	}
	if targets[0].config.EnvVars["MEDIA_AUTHORITY_SEAL_KEY_ID"] == "" {
		t.Fatal("single-cell Foghorn restart rendered no seal key")
	}
}

// The command timeout covers the full rollout readiness wait plus the
// restart itself.
func TestRestartCommandTimeoutExceedsRolloutReadiness(t *testing.T) {
	if restartCommandTimeout <= provisioner.RolloutReadinessTimeout {
		t.Fatalf("restartCommandTimeout = %s, must exceed RolloutReadinessTimeout %s", restartCommandTimeout, provisioner.RolloutReadinessTimeout)
	}
}
