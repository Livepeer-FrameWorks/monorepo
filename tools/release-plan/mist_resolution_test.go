package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const mistResolver = "scripts/resolve-mist-release.sh"

// A release bakes the Mist release resolve-mistserver picks when the tag is pushed. The dev stack
// edge must stage that same release, so both resolve it through one script and neither carries a
// Mist version or checksum of its own that could fall behind.
func TestStackEdgeStagesTheMistReleaseTheReleaseWorkflowResolves(t *testing.T) {
	job, ok := readReleaseWorkflow(t).Jobs["resolve-mistserver"]
	if !ok {
		t.Fatal("resolve-mistserver job not found in release workflow")
	}
	resolves := false
	for _, step := range job.Steps {
		if strings.Contains(step.Run, mistResolver) {
			resolves = true
		}
	}
	if !resolves {
		t.Fatalf("resolve-mistserver must resolve Mist through %s", mistResolver)
	}
	workflow, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(workflow), "mistserver/releases/latest") {
		t.Fatalf("release workflow queries the latest Mist release itself instead of through %s", mistResolver)
	}

	stack, err := os.ReadFile("../../scripts/edge-dev-dist.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stack), mistResolver) {
		t.Fatalf("scripts/edge-dev-dist.sh must stage the Mist release %s resolves", mistResolver)
	}
	if pin := regexp.MustCompile(`(?m)\bv[0-9]+\.[0-9]+\.[0-9]+\b`).Find(stack); pin != nil {
		t.Fatalf("scripts/edge-dev-dist.sh hardcodes Mist release %s, which drifts from the release the platform bakes", pin)
	}
	if digest := regexp.MustCompile(`\b[0-9a-f]{64}\b`).Find(stack); digest != nil {
		t.Fatalf("scripts/edge-dev-dist.sh hardcodes artifact checksum %s instead of reading the resolved release index", digest)
	}
}
