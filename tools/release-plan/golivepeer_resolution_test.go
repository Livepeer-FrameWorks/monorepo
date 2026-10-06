package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const goLivepeerResolver = "scripts/resolve-golivepeer-release.sh"

// A release bakes the go-livepeer release the release workflow picks when the tag is pushed. The
// dev stack must run that same release, so both resolve it through one script and the stack carries
// no go-livepeer version of its own that could fall behind.
func TestStackRunsTheGoLivepeerReleaseTheReleaseWorkflowResolves(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), goLivepeerResolver) {
		t.Fatalf("release workflow must resolve go-livepeer through %s", goLivepeerResolver)
	}
	if strings.Contains(string(workflow), "go-livepeer/releases/latest") {
		t.Fatalf("release workflow queries the latest go-livepeer release itself instead of through %s", goLivepeerResolver)
	}

	up, err := os.ReadFile("../../scripts/stack/up.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(up), goLivepeerResolver) {
		t.Fatalf("scripts/stack/up.sh must run the go-livepeer release %s resolves", goLivepeerResolver)
	}

	compose, err := os.ReadFile("../../docker-compose.stack.yml")
	if err != nil {
		t.Fatal(err)
	}
	if pin := regexp.MustCompile(`go-livepeer:v[0-9]+\.[0-9]+\.[0-9]+`).Find(compose); pin != nil {
		t.Fatalf("docker-compose.stack.yml hardcodes %s, which drifts from the release the platform bakes", pin)
	}
}
