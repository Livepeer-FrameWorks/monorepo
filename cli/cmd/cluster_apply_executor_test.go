package cmd

import (
	"bytes"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func TestBuildExecutorPlan_ReloadVsRestart(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"h1": {Name: "h1"},
			"h2": {Name: "h2"},
		},
	}
	// "bridge" supports SIGHUP reload (servicedefs), so env-only diffs reload
	// while a binary diff forces a restart.
	svc := clusterApplyService{
		Service: "bridge",
		Waves: []clusterApplyWave{
			{Index: 0, Hosts: []clusterApplyHost{{Host: "h1", Kinds: []orchestrator.DiffKind{orchestrator.DiffEnv}}}},
			{Index: 1, Hosts: []clusterApplyHost{{Host: "h2", Kinds: []orchestrator.DiffKind{orchestrator.DiffBinary}}}},
		},
	}

	plan, inputs, err := buildExecutorPlan(svc, manifest)
	if err != nil {
		t.Fatalf("buildExecutorPlan error: %v", err)
	}
	if plan.Service != "bridge" {
		t.Errorf("plan.Service = %q, want bridge", plan.Service)
	}
	if len(plan.Waves) != 2 {
		t.Fatalf("plan.Waves = %d, want 2", len(plan.Waves))
	}
	if got := inputs["bridge@h1"].Action; got != orchestrator.ActionReload {
		t.Errorf("h1 (env-only) action = %q, want reload", got)
	}
	if got := inputs["bridge@h2"].Action; got != orchestrator.ActionRestart {
		t.Errorf("h2 (binary) action = %q, want restart", got)
	}
	// Resolved host is carried from the manifest, not re-resolved later.
	if inputs["bridge@h1"].Host.Name != "h1" {
		t.Errorf("input host not resolved from manifest: %+v", inputs["bridge@h1"].Host)
	}
}

func TestBuildExecutorPlan_NonReloadServiceAlwaysRestarts(t *testing.T) {
	manifest := &inventory.Manifest{Hosts: map[string]inventory.Host{"h1": {Name: "h1"}}}
	// "mistserver" is not a SIGHUP-reload service, so even an env-only diff
	// must restart.
	svc := clusterApplyService{
		Service: "mistserver",
		Waves: []clusterApplyWave{
			{Index: 0, Hosts: []clusterApplyHost{{Host: "h1", Kinds: []orchestrator.DiffKind{orchestrator.DiffEnv}}}},
		},
	}
	_, inputs, err := buildExecutorPlan(svc, manifest)
	if err != nil {
		t.Fatalf("buildExecutorPlan error: %v", err)
	}
	if got := inputs["mistserver@h1"].Action; got != orchestrator.ActionRestart {
		t.Errorf("non-reload service action = %q, want restart", got)
	}
}

func TestBuildExecutorPlan_MissingHostErrors(t *testing.T) {
	manifest := &inventory.Manifest{Hosts: map[string]inventory.Host{"h1": {Name: "h1"}}}
	svc := clusterApplyService{
		Service: "bridge",
		Waves: []clusterApplyWave{
			{Index: 0, Hosts: []clusterApplyHost{{Host: "ghost"}}},
		},
	}
	if _, _, err := buildExecutorPlan(svc, manifest); err == nil {
		t.Fatal("expected error for host absent from manifest")
	}
}

func TestRenderClusterApplyText_Empty(t *testing.T) {
	var buf bytes.Buffer
	renderClusterApplyText(&buf, clusterApplyReport{Cluster: "eu-1"})
	out := buf.String()
	if !strings.Contains(out, "Cluster: eu-1") {
		t.Errorf("missing cluster header: %q", out)
	}
	if !strings.Contains(out, "No services have changes to roll out.") {
		t.Errorf("empty report should state no services: %q", out)
	}
}

func TestRenderClusterApplyText_PopulatedAndSkipped(t *testing.T) {
	rep := clusterApplyReport{
		Cluster: "eu-1",
		Services: []clusterApplyService{{
			Service:  "foghorn",
			Strategy: orchestrator.UpdateStrategy{MaxUnavailable: 2, Canary: 1},
			Waves: []clusterApplyWave{
				{Index: 0, Hosts: []clusterApplyHost{{Host: "h1", Kinds: []orchestrator.DiffKind{orchestrator.DiffBinary}, Details: []string{"bin changed"}}}},
			},
		}},
		Skipped: []clusterDiffEntry{{
			Host:    "h9",
			Service: "postgres",
			Kinds:   []orchestrator.DiffKind{orchestrator.DiffInfra},
			Details: map[orchestrator.DiffKind]string{orchestrator.DiffInfra: "infra diff"},
		}},
	}
	var buf bytes.Buffer
	renderClusterApplyText(&buf, rep)
	out := buf.String()
	for _, want := range []string{
		"Service: foghorn",
		"max_unavailable=2",
		"canary=1",
		"h1",
		"bin changed",
		"Skipped",
		"postgres",
		"infra diff",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}
