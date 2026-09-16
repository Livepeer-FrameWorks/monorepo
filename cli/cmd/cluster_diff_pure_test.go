package cmd

import (
	"errors"
	"testing"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"

	"github.com/spf13/cobra"
)

func TestStringSliceFlag(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().StringSlice("repos", nil, "")

	// Unset flag → nil.
	if got := stringSliceFlag(cmd, "repos"); got != nil {
		t.Errorf("unset flag = %v, want nil", got)
	}
	// Flag that doesn't exist → nil.
	if got := stringSliceFlag(cmd, "absent"); got != nil {
		t.Errorf("absent flag = %v, want nil", got)
	}

	// Empty entries (e.g. a trailing/double comma) are dropped.
	if err := cmd.Flags().Set("repos", "a,,b"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := stringSliceFlag(cmd, "repos")
	want := []string{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestClassifyClusterDiffTargetUsesAuthoritativePlan(t *testing.T) {
	t.Parallel()

	desired := &detect.Fingerprint{Files: map[detect.FileKind]detect.ExpectedFile{
		detect.FileKindEnv: {Path: "/etc/frameworks/service.env", SHA256: "expected"},
	}}
	target := diffProbeTarget{
		service: "service", hostName: "host", desired: desired,
		modeled: []detect.FileKind{detect.FileKindEnv},
	}

	entry := classifyClusterDiffTarget(target, authoritativeDiff{changed: false}, map[string]string{
		"/etc/frameworks/service.env": "different",
	}, "")
	if len(entry.Kinds) != 0 {
		t.Fatalf("Ansible no-change must suppress fingerprint-only drift, got %v", entry.Kinds)
	}

	entry = classifyClusterDiffTarget(target, authoritativeDiff{changed: true}, map[string]string{
		"/etc/frameworks/service.env": "different",
	}, "")
	if len(entry.Kinds) != 1 || entry.Kinds[0] != orchestrator.DiffEnv {
		t.Fatalf("confirmed typed drift = %v, want env", entry.Kinds)
	}

	target.desired = nil
	target.modeled = nil
	entry = classifyClusterDiffTarget(target, authoritativeDiff{changed: true}, nil, "")
	if len(entry.Kinds) != 1 || entry.Kinds[0] != orchestrator.DiffInfra {
		t.Fatalf("unmodeled confirmed drift = %v, want infra", entry.Kinds)
	}

	entry = classifyClusterDiffTarget(target, authoritativeDiff{
		changed: true,
		tasks:   []string{"Report pending reinstall under --check"},
	}, nil, "")
	if len(entry.Kinds) != 1 || entry.Kinds[0] != orchestrator.DiffBinary {
		t.Fatalf("binary reinstall task = %v, want binary", entry.Kinds)
	}
	if got := entry.Details[orchestrator.DiffBinary]; got != "changed tasks: Report pending reinstall under --check" {
		t.Fatalf("binary detail = %q", got)
	}

	entry = classifyClusterDiffTarget(target, authoritativeDiff{
		changed: true,
		tasks: []string{
			"Report pending Privateer reinstall under --check",
			"Ensure privateer directories",
		},
	}, nil, "")
	if len(entry.Kinds) != 2 || entry.Kinds[0] != orchestrator.DiffBinary || entry.Kinds[1] != orchestrator.DiffInfra {
		t.Fatalf("mixed binary and untyped tasks = %v, want binary,infra", entry.Kinds)
	}

	entry = classifyClusterDiffTarget(target, authoritativeDiff{err: errors.New("check failed")}, nil, "")
	if len(entry.Kinds) != 1 || entry.Kinds[0] != orchestrator.DiffUnknown {
		t.Fatalf("failed authoritative check = %v, want unknown", entry.Kinds)
	}
}

func TestNewClusterDiffTaskCarriesEffectiveServiceCluster(t *testing.T) {
	t.Parallel()

	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {Cluster: "regional-eu"},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu": {Deploy: "foghorn", Cluster: "media-eu-1"},
		},
	}
	task := newClusterDiffTask("foghorn", "foghorn-eu", "regional-eu-1", orchestrator.PhaseApplications, manifest)
	if task.ClusterID != "media-eu-1" {
		t.Fatalf("ClusterID = %q, want media-eu-1", task.ClusterID)
	}
}
