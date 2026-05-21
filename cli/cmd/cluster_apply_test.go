package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func TestBuildClusterApplyReportHonorsManifestUpdateStrategy(t *testing.T) {
	maxUnavailable := 2
	canary := 0
	regionStagger := false

	manifest := &inventory.Manifest{
		Type:    "cluster",
		Profile: "test",
		Hosts: map[string]inventory.Host{
			"h1": {Name: "h1", Labels: map[string]string{"region": "eu"}},
			"h2": {Name: "h2", Labels: map[string]string{"region": "eu"}},
			"h3": {Name: "h3", Labels: map[string]string{"region": "us"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {
				Enabled: true,
				Hosts:   []string{"h1", "h2", "h3"},
				UpdateStrategy: &inventory.UpdateStrategyConfig{
					MaxUnavailable: &maxUnavailable,
					Canary:         &canary,
					RegionStagger:  &regionStagger,
				},
			},
		},
	}
	entries := []clusterDiffEntry{
		{Host: "h1", Service: "foghorn", Deploy: "foghorn", Kinds: []orchestrator.DiffKind{orchestrator.DiffBinary}},
		{Host: "h2", Service: "foghorn", Deploy: "foghorn", Kinds: []orchestrator.DiffKind{orchestrator.DiffBinary}},
		{Host: "h3", Service: "foghorn", Deploy: "foghorn", Kinds: []orchestrator.DiffKind{orchestrator.DiffBinary}},
	}

	report := buildClusterApplyReport(manifest, entries)
	if len(report.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(report.Services))
	}
	service := report.Services[0]
	if service.Strategy.MaxUnavailable != 2 || service.Strategy.Canary != 0 || service.Strategy.RegionStagger {
		t.Fatalf("strategy = %+v, want max_unavailable=2 canary=0 region_stagger=false", service.Strategy)
	}
	if len(service.Waves) != 2 {
		t.Fatalf("waves = %d, want 2: %+v", len(service.Waves), service.Waves)
	}
	if got := hostsInApplyWave(service.Waves[0]); !equalStrings(got, []string{"h1", "h2"}) {
		t.Fatalf("wave 1 hosts = %v, want [h1 h2]", got)
	}
	if got := hostsInApplyWave(service.Waves[1]); !equalStrings(got, []string{"h3"}) {
		t.Fatalf("wave 2 hosts = %v, want [h3]", got)
	}
}

func TestDiffServiceHostsUsesEffectiveVMAgentPlacement(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"metrics-1": {Name: "metrics-1"},
			"media-1":   {Name: "media-1"},
		},
	}

	got := diffServiceHosts("vmagent", inventory.ServiceConfig{}, manifest)
	if !equalStrings(got, []string{"media-1", "metrics-1"}) {
		t.Fatalf("diffServiceHosts(vmagent) = %v, want all manifest hosts", got)
	}
}

func hostsInApplyWave(w clusterApplyWave) []string {
	out := make([]string, 0, len(w.Hosts))
	for _, h := range w.Hosts {
		out = append(out, h.Host)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
