package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeriscopeReleaseComponentsBuildDistinctCommands(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	components, err := LoadComponentsFromFile(filepath.Join(repoRoot, ".github", "release-components.json"))
	if err != nil {
		t.Fatalf("load release components: %v", err)
	}

	byName := make(map[string]ReleaseComponent, len(components.Services))
	for _, component := range components.Services {
		byName[component.Name] = component
	}
	query, queryOK := byName["periscope-query"]
	metering, meteringOK := byName["periscope-metering"]
	if !queryOK || !meteringOK {
		t.Fatalf("release components missing query=%t metering=%t", queryOK, meteringOK)
	}
	if query.Context != "api_analytics_query" || metering.Context != query.Context {
		t.Fatalf("unexpected contexts: query=%q metering=%q", query.Context, metering.Context)
	}
	if query.Cmd != "./cmd/periscope" || query.DockerCmd != query.Cmd {
		t.Fatalf("query commands = native %q docker %q", query.Cmd, query.DockerCmd)
	}
	if metering.Cmd != "./cmd/periscope-metering" || metering.DockerCmd != metering.Cmd {
		t.Fatalf("metering commands = native %q docker %q", metering.Cmd, metering.DockerCmd)
	}
	if query.HealthPort != 18004 || metering.HealthPort != 18021 {
		t.Fatalf("health ports = query %d metering %d", query.HealthPort, metering.HealthPort)
	}
	if query.Cmd == metering.Cmd {
		t.Fatal("query and metering release commands must differ")
	}

	workflow, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflowText := string(workflow)
	if !strings.Contains(workflowText, "docker_cmd:(.docker_cmd // \"\")") {
		t.Fatal("image matrix does not carry docker_cmd from the component catalog")
	}
	if !strings.Contains(workflowText, "health_port:(.health_port // 0)") {
		t.Fatal("image matrix does not carry health_port from the component catalog")
	}
	if strings.Count(workflowText, "CMD_PACKAGE={0}") != 2 {
		t.Fatalf("release workflow must pass CMD_PACKAGE to both image architectures")
	}
	if strings.Count(workflowText, "HEALTH_PORT={0}") != 2 {
		t.Fatalf("release workflow must pass HEALTH_PORT to both image architectures")
	}
}
