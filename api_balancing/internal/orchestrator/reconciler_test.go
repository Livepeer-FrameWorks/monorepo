package orchestrator

import (
	"testing"

	"frameworks/api_balancing/internal/state"
)

func TestParseRolloutPlanRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	if _, err := parseRolloutPlan(`{"batch_size":"wide"}`); err == nil {
		t.Fatal("parseRolloutPlan succeeded with invalid field type")
	}
}

func TestParseRolloutPlanRejectsInvalidDrainDeadline(t *testing.T) {
	t.Parallel()

	if _, err := parseRolloutPlan(`{"drain_deadline":"soon"}`); err == nil {
		t.Fatal("parseRolloutPlan succeeded with invalid drain_deadline")
	}
}

func TestParseRolloutPlanRejectsUnsupportedCapacityFloor(t *testing.T) {
	t.Parallel()

	if _, err := parseRolloutPlan(`{"capacity_floor_percent":80}`); err == nil {
		t.Fatal("parseRolloutPlan succeeded with unsupported capacity floor")
	}
}

func TestParseRolloutPlanRejectsUnknownKey(t *testing.T) {
	t.Parallel()

	if _, err := parseRolloutPlan(`{"batch_size":2,"max_parallel":4}`); err == nil {
		t.Fatal("parseRolloutPlan succeeded with unknown key")
	}
}

func TestParseRolloutPlanRejectsCamelCaseTypo(t *testing.T) {
	t.Parallel()

	if _, err := parseRolloutPlan(`{"capacityFloor":2}`); err == nil {
		t.Fatal("parseRolloutPlan succeeded with camelCase typo")
	}
}

func TestParseRolloutPlanAppliesDefaults(t *testing.T) {
	t.Parallel()

	plan, err := parseRolloutPlan(`{}`)
	if err != nil {
		t.Fatalf("parseRolloutPlan: %v", err)
	}
	if plan.BatchSize != 1 || plan.CanaryCount != 1 || plan.MaxFailed != 0 {
		t.Fatalf("defaults = batch %d canary %d max_failed %d", plan.BatchSize, plan.CanaryCount, plan.MaxFailed)
	}
}

func TestParseRolloutPlanDefaultsMaxFailedWhenErrorAbortEnabled(t *testing.T) {
	t.Parallel()

	plan, err := parseRolloutPlan(`{"error_abort":true}`)
	if err != nil {
		t.Fatalf("parseRolloutPlan: %v", err)
	}
	if plan.MaxFailed != 1 {
		t.Fatalf("max_failed = %d, want 1", plan.MaxFailed)
	}
}

func TestDesiredComponentsForWarmupSkipsConfigSchema(t *testing.T) {
	t.Parallel()

	components := desiredComponentsForWarmup(map[string]releaseComponent{
		"mist":          {Version: "v1.2.3", ArtifactURL: "https://example.test/mist.tgz", Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"config_schema": {Version: "4"},
	})
	if len(components) != 1 {
		t.Fatalf("components len = %d, want 1", len(components))
	}
	if components[0].GetComponent() != "mist" {
		t.Fatalf("component = %q, want mist", components[0].GetComponent())
	}
}

func TestDesiredComponentsFromExpectedUsesPersistedSet(t *testing.T) {
	t.Parallel()

	components := desiredComponentsFromExpected(map[string]string{
		"mist":     "v1.2.3",
		"helmsman": "",
	})
	if len(components) != 1 {
		t.Fatalf("components len = %d, want 1", len(components))
	}
	if components[0].GetComponent() != "mist" || components[0].GetVersion() != "v1.2.3" {
		t.Fatalf("component = %s/%s, want mist/v1.2.3", components[0].GetComponent(), components[0].GetVersion())
	}
}

func TestEligibleNodesSkipsManuallyFencedNodes(t *testing.T) {
	t.Parallel()

	nodes := []*state.NodeState{
		{NodeID: "normal", ClusterID: "cluster-a", IsHealthy: true, OperationalMode: state.NodeModeNormal, DeployMode: "native", OS: "linux", Arch: "amd64"},
		{NodeID: "legacy-empty-mode", ClusterID: "cluster-a", IsHealthy: true, DeployMode: "native", OS: "linux", Arch: "amd64"},
		{NodeID: "docker", ClusterID: "cluster-a", IsHealthy: true, DeployMode: "docker", OS: "linux", Arch: "amd64"},
		{NodeID: "unknown-platform", ClusterID: "cluster-a", IsHealthy: true, DeployMode: "native"},
		{NodeID: "draining", ClusterID: "cluster-a", IsHealthy: true, OperationalMode: state.NodeModeDraining, DeployMode: "native", OS: "linux", Arch: "amd64"},
		{NodeID: "maintenance", ClusterID: "cluster-a", IsHealthy: true, OperationalMode: state.NodeModeMaintenance, DeployMode: "native", OS: "linux", Arch: "amd64"},
		{NodeID: "other-cluster", ClusterID: "cluster-b", IsHealthy: true, OperationalMode: state.NodeModeNormal, DeployMode: "native", OS: "linux", Arch: "amd64"},
	}

	eligible := eligibleNodes(nodes, "cluster-a")
	if len(eligible) != 2 {
		t.Fatalf("eligible len = %d, want 2", len(eligible))
	}
	if eligible[0].NodeID != "legacy-empty-mode" || eligible[1].NodeID != "normal" {
		t.Fatalf("eligible nodes = %q, %q; want legacy-empty-mode, normal", eligible[0].NodeID, eligible[1].NodeID)
	}
}

func TestReleaseComponentForNodeSelectsPlatformArtifact(t *testing.T) {
	t.Parallel()

	component := releaseComponent{
		Version: "v1.2.3",
		Artifacts: map[string]releaseArtifact{
			"linux/amd64":  {ArtifactURL: "https://example.test/linux.tgz", Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			"darwin/arm64": {ArtifactURL: "https://example.test/darwin.tgz", Checksum: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
	selected, ok := releaseComponentForNode(component, &state.NodeState{OS: "darwin", Arch: "arm64"})
	if !ok {
		t.Fatal("releaseComponentForNode returned ok=false")
	}
	if selected.ArtifactURL != "https://example.test/darwin.tgz" {
		t.Fatalf("artifact_url = %q, want darwin artifact", selected.ArtifactURL)
	}
}
