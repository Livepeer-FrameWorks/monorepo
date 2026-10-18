package cmd

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

// dependencyReleaseManifest runs vmagent on obs-1 and obs-2, vmalert and
// vmauth on obs-1, and nginx on edge-1 and edge-2.
func dependencyReleaseManifest() *inventory.Manifest {
	hosts := map[string]inventory.Host{}
	for _, name := range []string{"obs-1", "obs-2", "edge-1", "edge-2"} {
		hosts[name] = serviceHost(name)
	}
	return &inventory.Manifest{
		Profile: "dev",
		Hosts:   hosts,
		Observability: map[string]inventory.ServiceConfig{
			"vmagent": {Enabled: true, Mode: "native", Hosts: []string{"obs-1", "obs-2"}},
			"vmalert": {Enabled: true, Mode: "native", Host: "obs-1"},
			"vmauth":  {Enabled: true, Mode: "native", Host: "obs-1"},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"nginx": {Enabled: true, Mode: "native", Hosts: []string{"edge-1", "edge-2"}},
		},
	}
}

func dependencyEvents(events []string) []string {
	var provisions []string
	for _, event := range events {
		if strings.HasPrefix(event, "provision:") {
			provisions = append(provisions, strings.TrimPrefix(event, "provision:"))
		}
	}
	return provisions
}

func TestReleaseDependencyConvergenceConvergesOneHostAtATimeInTypeOrder(t *testing.T) {
	log := &serviceEventLog{}
	fake := &serviceReleaseProvisioner{log: log, converged: map[string]bool{"vmagent:obs-2": true}}
	run := runServiceConvergence(t, dependencyReleaseManifest(), fake, &releaseServiceOps{}, false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	want := []string{"vmagent:obs-1", "vmalert:obs-1", "vmauth:obs-1", "nginx:edge-1", "nginx:edge-2"}
	if got := dependencyEvents(run.events); !slices.Equal(got, want) {
		t.Fatalf("converged %v, want %v", got, want)
	}
}

func TestReleaseDependencyConvergenceStopsAtTheFirstFailedHost(t *testing.T) {
	log := &serviceEventLog{}
	fake := &serviceReleaseProvisioner{log: log, failing: map[string]bool{"nginx:edge-1": true}}
	run := runServiceConvergence(t, dependencyReleaseManifest(), fake, &releaseServiceOps{}, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "proxies: nginx on edge-1") {
		t.Fatalf("err = %v, want the nginx failure on edge-1", run.err)
	}
	if slices.Contains(dependencyEvents(run.events), "nginx:edge-2") {
		t.Fatalf("edge-2 changed after edge-1 failed: %v", run.events)
	}
}

func TestReleaseDependencyConvergenceDryRunChangesNothing(t *testing.T) {
	log := &serviceEventLog{}
	fake := &serviceReleaseProvisioner{log: log}
	run := runServiceConvergence(t, dependencyReleaseManifest(), fake, &releaseServiceOps{}, true)
	if run.err != nil || len(run.events) != 0 || strings.Count(run.out, "[DRY-RUN] would converge") != 6 {
		t.Fatalf("dry-run: err %v, events %v, output:\n%s", run.err, run.events, run.out)
	}
}

// Compose dependencies converge one host at a time after the agents and proxies.
func TestReleaseComposeDependencyConvergence(t *testing.T) {
	manifest := dependencyReleaseManifest()
	manifest.Hosts["app-1"] = serviceHost("app-1")
	manifest.Hosts["app-2"] = serviceHost("app-2")
	manifest.Observability["grafana"] = inventory.ServiceConfig{Enabled: true, Mode: "docker", Hosts: []string{"app-1", "app-2"}}
	manifest.Observability["metabase"] = inventory.ServiceConfig{Enabled: true, Mode: "docker", Host: "app-2"}

	log := &serviceEventLog{}
	fake := &serviceReleaseProvisioner{log: log, converged: map[string]bool{"grafana:app-1": true}, failing: map[string]bool{"metabase:app-2": true}}
	run := runServiceConvergence(t, manifest, fake, &releaseServiceOps{}, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "compose dependencies: metabase on app-2") {
		t.Fatalf("err = %v, want the metabase failure", run.err)
	}
	got := dependencyEvents(run.events)
	want := []string{"vmagent:obs-1", "vmagent:obs-2", "vmalert:obs-1", "vmauth:obs-1", "nginx:edge-1", "nginx:edge-2", "metabase:app-2"}
	if !slices.Equal(got, want) {
		t.Fatalf("converged %v, want %v", got, want)
	}

	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var out bytes.Buffer
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", manifest, planReleaseHostConvergence(plan, manifest), "")
	if !strings.Contains(out.String(), "· compose dependencies (compose up with the rendered config), one host at a time: metabase@app-2 -> grafana@app-1 -> grafana@app-2") {
		t.Fatalf("plan output:\n%s", out.String())
	}
}

func TestReleaseDependencyConvergencePlanLines(t *testing.T) {
	manifest := dependencyReleaseManifest()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var out bytes.Buffer
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", manifest, planReleaseHostConvergence(plan, manifest), "")
	for _, line := range []string{
		"· VictoriaMetrics, Alertmanager, vmagent, vmalert, and vmauth (configs validated, then reloaded; a changed unit restarts), one host at a time: vmagent@obs-1 -> vmagent@obs-2 -> vmalert@obs-1 -> vmauth@obs-1",
		"· control-plane nginx and caddy (config tested, then reloaded), one host at a time: nginx@edge-1 -> nginx@edge-2",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("plan output missing %q:\n%s", line, out.String())
		}
	}
}

// A release changes the roles of VictoriaMetrics, Alertmanager, and support
// PostgreSQL instances too (their listener binds, their pinned artifacts), so
// release apply converges them: named PostgreSQL instances right after the
// primary database (which the service database step owns, so it is not here),
// VictoriaMetrics and Alertmanager before the agents that depend on them.
func TestReleaseConvergenceIncludesMetricsStoreAlertmanagerAndPostgresInstances(t *testing.T) {
	manifest := dependencyReleaseManifest()
	manifest.Observability["victoriametrics"] = inventory.ServiceConfig{Enabled: true, Mode: "native", Host: "obs-1"}
	manifest.Observability["alertmanager"] = inventory.ServiceConfig{Enabled: true, Mode: "native", Host: "obs-1", Config: map[string]string{
		"LOOKOUT_ALERTMANAGER_TOKEN": "t", "ALERTMANAGER_HEARTBEAT_URL": "https://heartbeat.test", "ALERTMANAGER_EMAIL_TO": "ops@example.test",
		"SMTP_HOST": "smtp.example.test", "SMTP_PORT": "587", "FROM_EMAIL": "alerts@example.test",
	}}
	manifest.Infrastructure.Postgres = &inventory.PostgresConfig{
		Enabled: true, Engine: "postgres", Host: "obs-2", Port: 5432,
		Instances: []inventory.PostgresInstance{{Name: "support", Host: "obs-2", Port: 5433}},
	}
	log := &serviceEventLog{}
	fake := &serviceReleaseProvisioner{log: log, converged: map[string]bool{}}
	run := runServiceConvergence(t, manifest, fake, &releaseServiceOps{}, false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	got := dependencyEvents(run.events)
	want := []string{"postgres:obs-2", "victoriametrics:obs-1", "alertmanager:obs-1", "vmagent:obs-1", "vmagent:obs-2", "vmalert:obs-1", "vmauth:obs-1", "nginx:edge-1", "nginx:edge-2"}
	if !slices.Equal(got, want) {
		t.Fatalf("converged %v, want %v", got, want)
	}
}
