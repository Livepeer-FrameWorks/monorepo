package orchestrator

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
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

func TestReconcileEligibleNodesRedrivesOnlyOrchestratedDrain(t *testing.T) {
	mock := installMockDB(t)
	now := time.Now()
	nodes := []*state.NodeState{
		{NodeID: "normal", ClusterID: "cluster-a", IsHealthy: true, OperationalMode: state.NodeModeNormal, DeployMode: "native", OS: "linux", Arch: "amd64"},
		{NodeID: "rollout-drain", ClusterID: "cluster-a", IsHealthy: true, OperationalMode: state.NodeModeDraining, OperationalModeSetBy: "update-orchestrator", DeployMode: "native", OS: "linux", Arch: "amd64"},
		{NodeID: "manual-drain", ClusterID: "cluster-a", IsHealthy: true, OperationalMode: state.NodeModeDraining, OperationalModeSetBy: "operator", DeployMode: "native", OS: "linux", Arch: "amd64"},
		{NodeID: "maintenance", ClusterID: "cluster-a", IsHealthy: true, OperationalMode: state.NodeModeMaintenance, DeployMode: "native", OS: "linux", Arch: "amd64"},
	}

	mock.ExpectQuery(`FROM foghorn\.node_update_state`).
		WithArgs("rollout-drain").
		WillReturnRows(sqlmock.NewRows(loadProgressColumns()).
			AddRow("stable:v1.2.3", "draining", now.Add(time.Hour), now, "{}"))
	eligible, err := reconcileEligibleNodes(context.Background(), nodes, "cluster-a")
	if err != nil {
		t.Fatalf("reconcileEligibleNodes: %v", err)
	}
	if len(eligible) != 2 {
		t.Fatalf("eligible len = %d, want 2", len(eligible))
	}
	if eligible[0].NodeID != "normal" || eligible[1].NodeID != "rollout-drain" {
		t.Fatalf("eligible nodes = %q, %q; want normal, rollout-drain", eligible[0].NodeID, eligible[1].NodeID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
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
	selected, ok := releaseComponentForNode("mist", component, &state.NodeState{OS: "darwin", Arch: "arm64"})
	if !ok {
		t.Fatal("releaseComponentForNode returned ok=false")
	}
	if selected.ArtifactURL != "https://example.test/darwin.tgz" {
		t.Fatalf("artifact_url = %q, want darwin artifact", selected.ArtifactURL)
	}
}

func TestReleaseComponentForNodeSelectsExactONNXVariant(t *testing.T) {
	t.Parallel()

	component := releaseComponent{
		Version: "v1.2.3",
		Artifacts: map[string]releaseArtifact{
			"linux/amd64": {ArtifactURL: "https://example.test/cpu.tgz", Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		Variants: map[string]releaseVariant{
			"cuda": {Artifacts: map[string]releaseArtifact{
				"linux/amd64": {ArtifactURL: "https://example.test/cuda.tgz", Checksum: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			}},
		},
	}
	selected, ok := releaseComponentForNode("mist", component, &state.NodeState{OS: "linux", Arch: "amd64", ONNXProfile: "cuda"})
	if !ok {
		t.Fatal("releaseComponentForNode returned ok=false")
	}
	if selected.ArtifactURL != "https://example.test/cuda.tgz" || selected.ONNXProfile != "cuda" {
		t.Fatalf("selected = %#v, want exact CUDA variant", selected)
	}
	if _, ok := releaseComponentForNode("mist", component, &state.NodeState{OS: "linux", Arch: "amd64", ONNXProfile: "tensorrt"}); ok {
		t.Fatal("missing TensorRT variant must not silently fall back to CPU")
	}
	helm := releaseComponent{Version: "v1.2.3", Artifacts: component.Artifacts}
	if _, ok := releaseComponentForNode("mist", helm, &state.NodeState{OS: "linux", Arch: "amd64", ONNXProfile: "cuda"}); ok {
		t.Fatal("legacy Mist component must not downgrade an accelerator node to CPU")
	}
	if _, ok := releaseComponentForNode("helmsman", helm, &state.NodeState{OS: "linux", Arch: "amd64", ONNXProfile: "cuda"}); !ok {
		t.Fatal("profile-independent components must still select their platform artifact")
	}
}
