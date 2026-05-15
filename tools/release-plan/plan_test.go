package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlanCarryForwardWhenSourceHashMatches simulates a release where a
// baseline manifest contains a recorded source_hash, and the next release
// has identical source. Decision: carry_forward, with the carried service
// entry preserving image+digest+service_version verbatim.
func TestPlanCarryForwardWhenSourceHashMatches(t *testing.T) {
	monorepo := writeFakeMonorepo(t, map[string]string{
		".github/release-components.json":    `{"services":[{"name":"toy","context":"toy","cmd":"./cmd/toy","dockerfile":"toy/Dockerfile","cgo":false,"darwin_binary":true}]}`,
		".go-version":                        "1.26.2",
		".github/workflows/release.yml":      "name: release\n",
		"pkg/go.mod":                         "module github.com/Livepeer-FrameWorks/monorepo/pkg\n\ngo 1.26.2\n",
		"toy/go.mod":                         "module example.com/toy\n\ngo 1.26.2\n\nrequire github.com/Livepeer-FrameWorks/monorepo/pkg v0.0.0\n\nreplace github.com/Livepeer-FrameWorks/monorepo/pkg => ../pkg\n",
		"toy/cmd/toy/main.go":                "package main\nfunc main() {}\n",
		"toy/Dockerfile":                     "FROM scratch\n",
		"tools/release-plan/release-plan.go": "package main // stub for HashWorkflowFiles\n",
	})

	// Resolve the source hash for this fixture, then write a matching
	// baseline manifest that triggers carry-forward.
	components, err := LoadComponentsFromFile(filepath.Join(monorepo, ".github", "release-components.json"))
	if err != nil {
		t.Fatal(err)
	}

	gitopsDir := t.TempDir()
	releasesDir := filepath.Join(gitopsDir, "releases")
	if mkdirErr := os.MkdirAll(releasesDir, 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	// First pass: planner with no baseline computes the source_hash for toy.
	planner := NewPlanner(monorepo, gitopsDir, "v0.2.40", components)
	plan, err := planner.Plan()
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if d := plan.Decisions["toy"]; d.Action != ActionBuild || d.SourceHash == "" {
		t.Fatalf("first pass: expected build with non-empty hash, got %+v", d)
	}
	knownHash := plan.Decisions["toy"].SourceHash

	// Write a baseline manifest at v0.2.39 with toy carrying knownHash.
	baselineYAML := `platform_version: v0.2.39
services:
  - name: toy
    service_version: v0.2.39
    image: example.com/frameworks-toy:v0.2.39
    digest: sha256:deadbeef
    source_hash: ` + knownHash + `
native_binaries:
  - name: toy
    artifacts:
      - arch: linux-amd64
        file: toy-v0.2.39-linux-amd64.tar.gz
        url: https://example.com/releases/toy-v0.2.39-linux-amd64.tar.gz
        checksum: sha256:cafe
`
	if writeErr := os.WriteFile(filepath.Join(releasesDir, "v0.2.39.yaml"), []byte(baselineYAML), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	// Second pass: same source tree, v0.2.39 baseline now exists with matching hash.
	planner2 := NewPlanner(monorepo, gitopsDir, "v0.2.40", components)
	plan2, err := planner2.Plan()
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	d := plan2.Decisions["toy"]
	if d.Action != ActionCarryForward {
		t.Fatalf("expected carry_forward, got %s (hash=%s baseline=%s)", d.Action, d.SourceHash, d.BaselineSourceHash)
	}
	if d.BaselineTag != "v0.2.39" {
		t.Fatalf("BaselineTag = %q, want v0.2.39", d.BaselineTag)
	}
	if d.CarriedService == nil || d.CarriedService.Digest != "sha256:deadbeef" {
		t.Fatalf("CarriedService = %+v, want digest sha256:deadbeef carried forward", d.CarriedService)
	}
	if d.CarriedService.ServiceVersion != "v0.2.39" {
		t.Fatalf("CarriedService.ServiceVersion = %q, want v0.2.39 preserved", d.CarriedService.ServiceVersion)
	}
	if d.CarriedNativeBinary == nil || len(d.CarriedNativeBinary.Artifacts) == 0 {
		t.Fatalf("CarriedNativeBinary not carried")
	}
	if plan2.Summary.CarryForwardCount != 1 || plan2.Summary.BuildCount != 0 {
		t.Fatalf("summary = %+v, want carry=1 build=0", plan2.Summary)
	}
}

func TestPlanCarryForwardBinaryOnlyService(t *testing.T) {
	monorepo := writeFakeMonorepo(t, map[string]string{
		".github/release-components.json":    `{"services":[{"name":"toy","context":"toy","cmd":"./cmd/toy","dockerfile":"","cgo":false,"darwin_binary":true}]}`,
		".go-version":                        "1.26.2",
		".github/workflows/release.yml":      "name: release\n",
		"pkg/go.mod":                         "module github.com/Livepeer-FrameWorks/monorepo/pkg\n\ngo 1.26.2\n",
		"toy/go.mod":                         "module example.com/toy\n\ngo 1.26.2\n\nrequire github.com/Livepeer-FrameWorks/monorepo/pkg v0.0.0\n\nreplace github.com/Livepeer-FrameWorks/monorepo/pkg => ../pkg\n",
		"toy/cmd/toy/main.go":                "package main\nfunc main() {}\n",
		"tools/release-plan/release-plan.go": "package main\n",
	})

	components, err := LoadComponentsFromFile(filepath.Join(monorepo, ".github", "release-components.json"))
	if err != nil {
		t.Fatal(err)
	}
	gitopsDir := t.TempDir()
	if mkdirErr := os.MkdirAll(filepath.Join(gitopsDir, "releases"), 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	plan1, err := NewPlanner(monorepo, gitopsDir, "v0.2.40", components).Plan()
	if err != nil {
		t.Fatal(err)
	}
	knownHash := plan1.Decisions["toy"].SourceHash

	baselineYAML := `platform_version: v0.2.39
native_binaries:
  - name: toy
    source_hash: ` + knownHash + `
    artifacts:
      - arch: linux-amd64
        file: toy-v0.2.39-linux-amd64.tar.gz
        url: https://example.com/releases/toy-v0.2.39-linux-amd64.tar.gz
        checksum: sha256:cafe
`
	if writeErr := os.WriteFile(filepath.Join(gitopsDir, "releases", "v0.2.39.yaml"), []byte(baselineYAML), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	plan2, err := NewPlanner(monorepo, gitopsDir, "v0.2.40", components).Plan()
	if err != nil {
		t.Fatal(err)
	}
	d := plan2.Decisions["toy"]
	if d.Action != ActionCarryForward {
		t.Fatalf("expected carry_forward, got %s (hash=%s baseline=%s)", d.Action, d.SourceHash, d.BaselineSourceHash)
	}
	if d.CarriedService != nil {
		t.Fatalf("CarriedService = %+v, want nil for binary-only service", d.CarriedService)
	}
	if d.CarriedNativeBinary == nil || d.CarriedNativeBinary.SourceHash != knownHash {
		t.Fatalf("CarriedNativeBinary = %+v, want source_hash %s", d.CarriedNativeBinary, knownHash)
	}
	if plan2.Summary.CarryForwardCount != 1 || plan2.Summary.BuildCount != 0 {
		t.Fatalf("summary = %+v, want carry=1 build=0", plan2.Summary)
	}
}

// TestPlanBuildsWhenBaselineLacksSourceHash covers a baseline manifest
// without source_hash. Every component must be built, not silently carried.
func TestPlanBuildsWhenBaselineLacksSourceHash(t *testing.T) {
	monorepo := writeFakeMonorepo(t, map[string]string{
		".github/release-components.json":    `{"services":[{"name":"toy","context":"toy","cmd":"./cmd/toy","dockerfile":"toy/Dockerfile","cgo":false,"darwin_binary":true}]}`,
		".go-version":                        "1.26.2",
		".github/workflows/release.yml":      "name: release\n",
		"pkg/go.mod":                         "module github.com/Livepeer-FrameWorks/monorepo/pkg\n\ngo 1.26.2\n",
		"toy/go.mod":                         "module example.com/toy\n\ngo 1.26.2\n\nrequire github.com/Livepeer-FrameWorks/monorepo/pkg v0.0.0\n\nreplace github.com/Livepeer-FrameWorks/monorepo/pkg => ../pkg\n",
		"toy/cmd/toy/main.go":                "package main\nfunc main() {}\n",
		"toy/Dockerfile":                     "FROM scratch\n",
		"tools/release-plan/release-plan.go": "package main // stub for HashWorkflowFiles\n",
	})

	gitopsDir := t.TempDir()
	releasesDir := filepath.Join(gitopsDir, "releases")
	if err := os.MkdirAll(releasesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	baselineYAML := `platform_version: v0.2.39
services:
  - name: toy
    service_version: v0.2.39
    image: example.com/frameworks-toy:v0.2.39
    digest: sha256:deadbeef
`
	if err := os.WriteFile(filepath.Join(releasesDir, "v0.2.39.yaml"), []byte(baselineYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	components, _ := LoadComponentsFromFile(filepath.Join(monorepo, ".github", "release-components.json"))
	planner := NewPlanner(monorepo, gitopsDir, "v0.2.40", components)
	plan, err := planner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	d := plan.Decisions["toy"]
	if d.Action != ActionBuild {
		t.Fatalf("expected build (baseline lacks source_hash), got %s", d.Action)
	}
}

// TestPlanRebuildsWhenSourceChanged: same monorepo as the carry-forward
// test, but the source file is modified after the baseline records its
// hash. Decision must flip to build.
func TestPlanRebuildsWhenSourceChanged(t *testing.T) {
	monorepo := writeFakeMonorepo(t, map[string]string{
		".github/release-components.json":    `{"services":[{"name":"toy","context":"toy","cmd":"./cmd/toy","dockerfile":"toy/Dockerfile","cgo":false,"darwin_binary":true}]}`,
		".go-version":                        "1.26.2",
		".github/workflows/release.yml":      "name: release\n",
		"pkg/go.mod":                         "module github.com/Livepeer-FrameWorks/monorepo/pkg\n\ngo 1.26.2\n",
		"toy/go.mod":                         "module example.com/toy\n\ngo 1.26.2\n\nrequire github.com/Livepeer-FrameWorks/monorepo/pkg v0.0.0\n\nreplace github.com/Livepeer-FrameWorks/monorepo/pkg => ../pkg\n",
		"toy/cmd/toy/main.go":                "package main\nfunc main() {}\n",
		"toy/Dockerfile":                     "FROM scratch\n",
		"tools/release-plan/release-plan.go": "package main // stub for HashWorkflowFiles\n",
	})

	components, _ := LoadComponentsFromFile(filepath.Join(monorepo, ".github", "release-components.json"))
	gitopsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitopsDir, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Record baseline hash from current source tree.
	plan1, err := NewPlanner(monorepo, gitopsDir, "v0.2.40", components).Plan()
	if err != nil {
		t.Fatal(err)
	}
	hash := plan1.Decisions["toy"].SourceHash

	baselineYAML := `platform_version: v0.2.39
services:
  - name: toy
    image: example.com/frameworks-toy:v0.2.39
    digest: sha256:deadbeef
    source_hash: ` + hash + `
`
	if writeErr := os.WriteFile(filepath.Join(gitopsDir, "releases", "v0.2.39.yaml"), []byte(baselineYAML), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	// Mutate the source.
	if writeErr := os.WriteFile(filepath.Join(monorepo, "toy", "cmd", "toy", "main.go"), []byte("package main\nfunc main() { _ = 1 }\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	plan2, err := NewPlanner(monorepo, gitopsDir, "v0.2.40", components).Plan()
	if err != nil {
		t.Fatal(err)
	}
	d := plan2.Decisions["toy"]
	if d.Action != ActionBuild {
		t.Fatalf("expected build after source change, got %s", d.Action)
	}
	if d.BaselineSourceHash == "" || d.BaselineSourceHash == d.SourceHash {
		t.Fatalf("expected differing baseline_source_hash, got baseline=%q current=%q", d.BaselineSourceHash, d.SourceHash)
	}
}

// TestPlanWebappCarryForwardWhenSourceHashMatches mirrors the Go-service
// carry-forward test for webapps. Confirms ComputeWebappSourceHash is
// deterministic and that the planner copies the interface BOM forward.
func TestPlanWebappCarryForwardWhenSourceHashMatches(t *testing.T) {
	monorepo := writeFakeMonorepo(t, map[string]string{
		".github/release-components.json":    `{"services":[],"webapps":[{"name":"toyapp","context":"webapp","env_prefix":"VITE","build_dir":"build"}]}`,
		".go-version":                        "1.26.2",
		".github/workflows/release.yml":      "name: release\n",
		"tools/release-plan/release-plan.go": "package main\n",
		"pnpm-workspace.yaml":                "packages:\n  - 'webapp'\n",
		"pnpm-lock.yaml":                     "lockfileVersion: '9.0'\n",
		"package.json":                       `{"name":"root","private":true}`,
		"webapp/package.json":                `{"name":"toyapp","version":"0.0.1"}`,
		"webapp/Dockerfile":                  "FROM node:24-alpine\n",
		"webapp/src/main.ts":                 "console.log('hi')\n",
		"webapp/vite.config.ts":              "export default {}\n",
	})

	components, err := LoadComponentsFromFile(filepath.Join(monorepo, ".github", "release-components.json"))
	if err != nil {
		t.Fatal(err)
	}
	gitopsDir := t.TempDir()
	if mkdirErr := os.MkdirAll(filepath.Join(gitopsDir, "releases"), 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	// First pass: no baseline, compute hash for toyapp.
	plan1, err := NewPlanner(monorepo, gitopsDir, "v0.2.40", components).Plan()
	if err != nil {
		t.Fatal(err)
	}
	if d := plan1.Decisions["toyapp"]; d.Action != ActionBuild || d.SourceHash == "" || d.Kind != KindInterface {
		t.Fatalf("first pass: expected interface build with hash, got %+v", d)
	}
	knownHash := plan1.Decisions["toyapp"].SourceHash

	// Write a baseline manifest at v0.2.39 with toyapp under interfaces[].
	baseline := `platform_version: v0.2.39
interfaces:
  - name: toyapp
    image: example.com/frameworks-toyapp:v0.2.39
    digest: sha256:facefeed
    source_hash: ` + knownHash + `
`
	if writeErr := os.WriteFile(filepath.Join(gitopsDir, "releases", "v0.2.39.yaml"), []byte(baseline), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	plan2, err := NewPlanner(monorepo, gitopsDir, "v0.2.40", components).Plan()
	if err != nil {
		t.Fatal(err)
	}
	d := plan2.Decisions["toyapp"]
	if d.Action != ActionCarryForward {
		t.Fatalf("expected carry_forward, got %s (hash=%s baseline=%s)", d.Action, d.SourceHash, d.BaselineSourceHash)
	}
	if d.CarriedInterface == nil || d.CarriedInterface.Digest != "sha256:facefeed" {
		t.Fatalf("CarriedInterface = %+v, want digest sha256:facefeed", d.CarriedInterface)
	}
	if d.CarriedInterface.Name != "toyapp" {
		t.Fatalf("CarriedInterface.Name = %q, want toyapp", d.CarriedInterface.Name)
	}
}

// writeFakeMonorepo materializes a synthetic monorepo tree from a flat
// map of path -> content. Returns the temp root.
func writeFakeMonorepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestPlanOutputJSONShapeIsStable keeps the planner wire shape explicit.
func TestPlanOutputJSONShapeIsStable(t *testing.T) {
	out := &PlanOutput{
		PlatformVersion: "v0.2.40",
		Track:           "stable",
		BaselineTag:     "v0.2.39",
		Decisions: map[string]Decision{
			"toy": {Name: "toy", Kind: KindService, Action: ActionBuild, SourceHash: "sha256:abc"},
		},
		Summary: PlanSummary{BuildCount: 1, CarryForwardCount: 0},
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`"platform_version":"v0.2.40"`,
		`"track":"stable"`,
		`"baseline_tag":"v0.2.39"`,
		`"action":"build"`,
		`"source_hash":"sha256:abc"`,
		`"build_count":1`,
		`"carry_forward_count":0`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q in: %s", want, got)
		}
	}
}
