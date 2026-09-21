package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyPrintsWorkflowOutputs(t *testing.T) {
	cases := map[string]string{
		"v0.3.4":         "channel=stable\nimage_track=latest\n",
		"v0.3.5-rc1":     "channel=rc\nimage_track=rc\n",
		"v0.3.5-beta.2":  "channel=rc\nimage_track=rc\n",
		"v0.3.5+build.9": "channel=stable\nimage_track=latest\n",
	}
	for tag, want := range cases {
		got, err := classify(tag)
		if err != nil || got != want {
			t.Errorf("classify(%q) = %q, %v; want %q", tag, got, err, want)
		}
	}
	for _, bad := range []string{"0.3.4", "v0.3.5-", "release-1"} {
		if _, err := classify(bad); err == nil {
			t.Errorf("classify(%q) must fail", bad)
		}
	}
}

// Image tags use the shared classifier; channel pointers use the monotonic
// release planner, which also advances candidate without regressing stable.
func TestReleaseWorkflowUsesSharedClassifier(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(data)
	for _, forbidden := range []string{`*-beta*)`, `*-rc*)`, `TRACK="latest"`, `channel="stable"`} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release workflow still classifies tags itself (found %q)", forbidden)
		}
	}
	if got := strings.Count(workflow, "needs.classify-release.outputs.image_track"); got != 3 {
		t.Errorf("image manifests reading the shared image track = %d, want 3 (services, edge, webapps)", got)
	}
	for _, required := range []string{"cd ../monorepo/tools/release-plan", `--tag "${GITHUB_REF_NAME}"`, "--update-channels"} {
		if !strings.Contains(workflow, required) {
			t.Errorf("update-env must use the monotonic channel writer (missing %q)", required)
		}
	}
	if !strings.Contains(workflow, "scripts/release-channel") {
		t.Error("release workflow does not run scripts/release-channel")
	}
}
