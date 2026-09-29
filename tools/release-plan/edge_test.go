package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type edgeFixture struct {
	monorepo   string
	gitopsDir  string
	components ReleaseComponents
	mistIndex  string
}

func testMistIndex() map[string]any {
	platform := func(profile, arch string, image bool) map[string]any {
		out := map[string]any{
			"artifact": map[string]any{
				"name":     fmt.Sprintf("mistserver-linux-%s-%s.tar.gz", profile, arch),
				"checksum": "sha256:" + profile + arch,
			},
		}
		if image {
			out["image"] = map[string]any{
				"ref":    "ghcr.io/livepeer-frameworks/mistserver-" + profile,
				"digest": "sha256:img" + profile + arch,
			}
		}
		return out
	}
	return map[string]any{
		"schema":          "mistserver.release/v1",
		"release_tag":     "v3.10.0",
		"default_profile": "cpu",
		"profiles": map[string]any{
			"cpu": map[string]any{"platforms": map[string]any{
				"linux/amd64": platform("cpu", "amd64", false),
				"linux/arm64": platform("cpu", "arm64", false),
			}},
			"cuda":     map[string]any{"platforms": map[string]any{"linux/amd64": platform("cuda", "amd64", true)}},
			"tensorrt": map[string]any{"platforms": map[string]any{"linux/amd64": platform("tensorrt", "amd64", true)}},
			"openvino": map[string]any{"platforms": map[string]any{"linux/amd64": platform("openvino", "amd64", true)}},
		},
	}
}

func writeMistIndex(t *testing.T, path string, index map[string]any) {
	t.Helper()
	b, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

const edgeFixtureCaddy = `infrastructure:
  - name: caddy
    version: "2.11.3"
    artifacts:
      - arch: linux-amd64
        url: https://example.com/caddy_linux_amd64.tar.gz
        checksum: sha512:aaa
      - arch: linux-arm64
        url: https://example.com/caddy_linux_arm64.tar.gz
        checksum: sha512:bbb
      - arch: darwin-arm64
        url: https://example.com/caddy_mac_arm64.tar.gz
        checksum: sha512:ccc
`

// newEdgeFixture builds a monorepo with helmsman + the edge image inputs and a
// v0.2.39 baseline that recorded exactly this tree's hashes, so an unchanged
// re-plan carries the edge image forward.
func newEdgeFixture(t *testing.T) edgeFixture {
	t.Helper()
	monorepo := writeFakeMonorepo(t, map[string]string{
		".github/release-components.json":                              `{"services":[{"name":"helmsman","context":"api_sidecar","cmd":"./cmd/helmsman","cgo":false,"darwin_binary":true}]}`,
		".go-version":                                                  "1.26.2",
		".github/workflows/release.yml":                                "name: release\n",
		"tools/release-plan/release-plan.go":                           "package main\n",
		"pkg/go.mod":                                                   "module github.com/Livepeer-FrameWorks/monorepo/pkg\n\ngo 1.26.2\n",
		"api_sidecar/go.mod":                                           "module example.com/sidecar\n\ngo 1.26.2\n",
		"api_sidecar/cmd/helmsman/main.go":                             "package main\nfunc main() {}\n",
		"config/infrastructure.yaml":                                   edgeFixtureCaddy,
		"edge/Dockerfile":                                              "FROM ubuntu:24.04\nARG S6_OVERLAY_VERSION=3.2.3.0\n",
		"edge/stage-dist.sh":                                           "#!/usr/bin/env bash\n",
		"edge/rootfs/etc/s6-overlay/s6-rc.d/helmsman/run":              "#!/command/execlineb -P\n",
		"edge/rootfs/etc/frameworks/templates/Caddyfile.bootstrap":     ":80\n",
		"edge/dist/versions.env":                                       "HELMSMAN_VERSION=local\n",
		"edge/rootfs/etc/s6-overlay/s6-rc.d/helmsman/dependencies.d/x": "",
	})
	for _, rel := range []string{"edge/stage-dist.sh", "edge/rootfs/etc/s6-overlay/s6-rc.d/helmsman/run"} {
		if err := os.Chmod(filepath.Join(monorepo, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mistIndex := filepath.Join(t.TempDir(), "mistserver-release-index.json")
	writeMistIndex(t, mistIndex, testMistIndex())

	components, err := LoadComponentsFromFile(filepath.Join(monorepo, ".github", "release-components.json"))
	if err != nil {
		t.Fatal(err)
	}
	gitopsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitopsDir, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := edgeFixture{monorepo: monorepo, gitopsDir: gitopsDir, components: components, mistIndex: mistIndex}

	first := f.plan(t)
	if d := first.Decisions[edgeDecisionName]; d.Action != ActionBuild || d.SourceHash == "" {
		t.Fatalf("first plan: edge decision = %+v, want build with a source hash", d)
	}
	writeEdgeBaseline(t, gitopsDir, first.Decisions[edgeHelmsmanComponent].SourceHash, first.Decisions[edgeDecisionName].SourceHash, nil)
	return f
}

func (f edgeFixture) plan(t *testing.T) *PlanOutput {
	t.Helper()
	planner := NewPlanner(f.monorepo, f.gitopsDir, "v0.2.40", f.components)
	planner.MistIndexPath = f.mistIndex
	out, err := planner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f edgeFixture) write(t *testing.T, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.monorepo, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeEdgeBaseline writes a v0.2.39 manifest with a helmsman native binary
// and a fully pinned edge service entry. Profiles in dropVariants are omitted.
func writeEdgeBaseline(t *testing.T, gitopsDir, helmsmanHash, edgeHash string, dropVariants []string) {
	t.Helper()
	variants := map[string]ServiceVariant{}
	for _, v := range edgeImageVariants {
		if slices.Contains(dropVariants, v.Profile) {
			continue
		}
		suffix := ""
		if v.Profile != "cpu" {
			suffix = "-onnx-" + v.Profile
		}
		digest := "sha256:edge" + v.Profile
		variants[v.Profile] = ServiceVariant{
			Image:  "livepeerframeworks/frameworks-edge:v0.2.39" + suffix,
			Digest: digest,
			Images: map[string]RegistryImage{
				"dockerhub": {Image: "livepeerframeworks/frameworks-edge:v0.2.39" + suffix, Digest: digest},
				"ghcr":      {Image: "ghcr.io/livepeer-frameworks/frameworks-edge:v0.2.39" + suffix, Digest: digest},
			},
		}
	}
	manifest := Manifest{
		PlatformVersion: "v0.2.39",
		Services: []ServiceEntry{{
			Name:           edgeDecisionName,
			ServiceVersion: "v0.2.38",
			Image:          "livepeerframeworks/frameworks-edge:v0.2.38",
			Digest:         "sha256:edgecpu",
			Images: map[string]RegistryImage{
				"dockerhub": {Image: "livepeerframeworks/frameworks-edge:v0.2.38", Digest: "sha256:edgecpu"},
				"ghcr":      {Image: "ghcr.io/livepeer-frameworks/frameworks-edge:v0.2.38", Digest: "sha256:edgecpu"},
			},
			Variants:   variants,
			SourceHash: edgeHash,
		}},
		NativeBinaries: []NativeBinary{{
			Name:       edgeHelmsmanComponent,
			SourceHash: helmsmanHash,
			Artifacts: []Artifact{
				{Arch: "linux-amd64", File: "frameworks-helmsman-v0.2.39-linux-amd64.tar.gz", URL: "https://example.com/h-amd64.tar.gz", Checksum: "sha256:h1"},
				{Arch: "linux-arm64", File: "frameworks-helmsman-v0.2.39-linux-arm64.tar.gz", URL: "https://example.com/h-arm64.tar.gz", Checksum: "sha256:h2"},
			},
		}},
	}
	b, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitopsDir, "releases", "v0.2.39.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEdgeImageCarriesForwardWhenInputsUnchanged(t *testing.T) {
	f := newEdgeFixture(t)
	// The staged dist is build output, not an input.
	f.write(t, "edge/dist/versions.env", "HELMSMAN_VERSION=other\n")

	plan := f.plan(t)
	if h := plan.Decisions[edgeHelmsmanComponent]; h.Action != ActionCarryForward {
		t.Fatalf("helmsman action = %s, want carry_forward", h.Action)
	}
	d := plan.Decisions[edgeDecisionName]
	if d.Action != ActionCarryForward {
		t.Fatalf("edge action = %s (hash=%s baseline=%s), want carry_forward", d.Action, d.SourceHash, d.BaselineSourceHash)
	}
	if d.Kind != KindService || d.BaselineTag != "v0.2.39" || d.BaselineSourceHash != d.SourceHash {
		t.Fatalf("edge decision = %+v", d)
	}
	if d.CarriedService == nil || d.CarriedService.ServiceVersion != "v0.2.38" || d.CarriedService.Digest != "sha256:edgecpu" {
		t.Fatalf("carried edge entry = %+v, want baseline entry verbatim", d.CarriedService)
	}
	for _, v := range edgeImageVariants {
		if got := d.CarriedService.Variants[v.Profile].Images["ghcr"].Digest; got != "sha256:edge"+v.Profile {
			t.Fatalf("carried %s ghcr digest = %q", v.Profile, got)
		}
	}
	if plan.Summary.CarryForwardCount != 2 || plan.Summary.BuildCount != 0 {
		t.Fatalf("summary = %+v, want carry=2 build=0", plan.Summary)
	}
}

func TestEdgeImageBuildsWhenAnyInputChanges(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, f edgeFixture)
	}{
		{"helmsman source", func(t *testing.T, f edgeFixture) {
			f.write(t, "api_sidecar/cmd/helmsman/main.go", "package main\nfunc main() { _ = 1 }\n")
		}},
		{"mist image digest for one profile", func(t *testing.T, f edgeFixture) {
			idx := testMistIndex()
			idx["profiles"].(map[string]any)["cuda"].(map[string]any)["platforms"].(map[string]any)["linux/amd64"].(map[string]any)["image"].(map[string]any)["digest"] = "sha256:rebuilt"
			writeMistIndex(t, f.mistIndex, idx)
		}},
		{"mist artifact checksum for one profile", func(t *testing.T, f edgeFixture) {
			idx := testMistIndex()
			idx["profiles"].(map[string]any)["cpu"].(map[string]any)["platforms"].(map[string]any)["linux/arm64"].(map[string]any)["artifact"].(map[string]any)["checksum"] = "sha256:rebuilt"
			writeMistIndex(t, f.mistIndex, idx)
		}},
		{"mist release tag", func(t *testing.T, f edgeFixture) {
			idx := testMistIndex()
			idx["release_tag"] = "v3.10.1"
			writeMistIndex(t, f.mistIndex, idx)
		}},
		{"caddy pin", func(t *testing.T, f edgeFixture) {
			f.write(t, "config/infrastructure.yaml", strings.Replace(edgeFixtureCaddy, "sha512:bbb", "sha512:bbc", 1))
		}},
		{"caddy version", func(t *testing.T, f edgeFixture) {
			f.write(t, "config/infrastructure.yaml", strings.Replace(edgeFixtureCaddy, `"2.11.3"`, `"2.11.4"`, 1))
		}},
		{"edge Dockerfile (s6 version)", func(t *testing.T, f edgeFixture) {
			f.write(t, "edge/Dockerfile", "FROM ubuntu:24.04\nARG S6_OVERLAY_VERSION=3.2.4.0\n")
		}},
		{"edge stage-dist.sh", func(t *testing.T, f edgeFixture) {
			f.write(t, "edge/stage-dist.sh", "#!/usr/bin/env bash\nset -e\n")
		}},
		{"edge rootfs file", func(t *testing.T, f edgeFixture) {
			f.write(t, "edge/rootfs/etc/frameworks/templates/Caddyfile.bootstrap", ":8080\n")
		}},
		{"edge rootfs new file", func(t *testing.T, f edgeFixture) {
			f.write(t, "edge/rootfs/etc/s6-overlay/s6-rc.d/helmsman/type", "longrun\n")
		}},
		{"edge rootfs executable bit", func(t *testing.T, f edgeFixture) {
			if err := os.Chmod(filepath.Join(f.monorepo, "edge/rootfs/etc/s6-overlay/s6-rc.d/helmsman/run"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"release workflow", func(t *testing.T, f edgeFixture) {
			f.write(t, ".github/workflows/release.yml", "name: release\non: push\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEdgeFixture(t)
			tc.mutate(t, f)
			if d := f.plan(t).Decisions[edgeDecisionName]; d.Action != ActionBuild || d.CarriedService != nil {
				t.Fatalf("edge decision = %+v, want build", d)
			}
		})
	}
}

// A helmsman rebuild bakes new bytes and a new HELMSMAN_VERSION even when its
// source hash is unchanged (here: the baseline never recorded one).
func TestEdgeImageBuildsWhenHelmsmanBuilds(t *testing.T) {
	f := newEdgeFixture(t)
	first := f.plan(t)
	writeEdgeBaseline(t, f.gitopsDir, "", first.Decisions[edgeDecisionName].SourceHash, nil)

	plan := f.plan(t)
	if h := plan.Decisions[edgeHelmsmanComponent]; h.Action != ActionBuild {
		t.Fatalf("helmsman action = %s, want build", h.Action)
	}
	if d := plan.Decisions[edgeDecisionName]; d.Action != ActionBuild {
		t.Fatalf("edge action = %s, want build while helmsman builds", d.Action)
	}
}

func TestEdgeImageBuildsWhenBaselineLacksAVariant(t *testing.T) {
	f := newEdgeFixture(t)
	first := f.plan(t)
	writeEdgeBaseline(t, f.gitopsDir, first.Decisions[edgeHelmsmanComponent].SourceHash, first.Decisions[edgeDecisionName].SourceHash, []string{"openvino"})

	if d := f.plan(t).Decisions[edgeDecisionName]; d.Action != ActionBuild {
		t.Fatalf("edge action = %s, want build when the baseline cannot supply every variant", d.Action)
	}
}

func TestEdgeImageBuildsWhenBaselineLacksSourceHash(t *testing.T) {
	f := newEdgeFixture(t)
	first := f.plan(t)
	writeEdgeBaseline(t, f.gitopsDir, first.Decisions[edgeHelmsmanComponent].SourceHash, "", nil)

	if d := f.plan(t).Decisions[edgeDecisionName]; d.Action != ActionBuild {
		t.Fatalf("edge action = %s, want build against a pre-decision baseline", d.Action)
	}
}

func TestEdgeImagePlanRejectsIncompleteMistIndex(t *testing.T) {
	f := newEdgeFixture(t)
	idx := testMistIndex()
	delete(idx["profiles"].(map[string]any), "tensorrt")
	writeMistIndex(t, f.mistIndex, idx)

	planner := NewPlanner(f.monorepo, f.gitopsDir, "v0.2.40", f.components)
	planner.MistIndexPath = f.mistIndex
	if _, err := planner.Plan(); err == nil || !strings.Contains(err.Error(), "tensorrt") {
		t.Fatalf("Plan() error = %v, want missing tensorrt profile", err)
	}
}

func TestEdgeImageDecisionOmittedWithoutMistIndex(t *testing.T) {
	f := newEdgeFixture(t)
	f.mistIndex = ""
	if d, ok := f.plan(t).Decisions[edgeDecisionName]; ok {
		t.Fatalf("edge decision = %+v, want none without a Mist index", d)
	}
}

type releaseWorkflowDoc struct {
	Jobs map[string]struct {
		Needs    any `yaml:"needs"`
		Strategy struct {
			// Matrix is an expression string in the jobs that read it from
			// define-release-components, so it is decoded per job.
			Matrix yaml.Node `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readReleaseWorkflow(t *testing.T) releaseWorkflowDoc {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var doc releaseWorkflowDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// The edge hash names the variants it covers; a matrix entry it does not
// know would ship without being part of the carry-forward decision.
func TestEdgeImageVariantsMatchReleaseWorkflow(t *testing.T) {
	job, ok := readReleaseWorkflow(t).Jobs["build-edge-image"]
	if !ok {
		t.Fatal("build-edge-image job not found")
	}
	var matrix struct {
		Include []map[string]string `yaml:"include"`
	}
	if err := job.Strategy.Matrix.Decode(&matrix); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, entry := range matrix.Include {
		got[entry["profile"]] = append(got[entry["profile"]], "linux/"+entry["arch"])
	}
	want := map[string][]string{}
	for _, v := range edgeImageVariants {
		want[v.Profile] = v.Platforms
	}
	for profile := range got {
		slices.Sort(got[profile])
	}
	for profile := range want {
		slices.Sort(want[profile])
	}
	if len(got) != len(want) {
		t.Fatalf("build-edge-image matrix profiles = %v, edgeImageVariants = %v", got, want)
	}
	for profile, platforms := range want {
		if !slices.Equal(got[profile], platforms) {
			t.Fatalf("profile %s: workflow platforms %v, edgeImageVariants %v", profile, got[profile], platforms)
		}
	}
}

func TestReleaseWorkflowWiresEdgeImageDecision(t *testing.T) {
	doc := readReleaseWorkflow(t)

	var planRun string
	for _, step := range doc.Jobs["release-plan"].Steps {
		if step.Name == "Run release-plan" {
			planRun = step.Run
		}
	}
	if !strings.Contains(planRun, "--mist-index") {
		t.Fatalf("release-plan step does not pass --mist-index:\n%s", planRun)
	}

	// The layer cache exported the multi-GB accelerator base layers and never
	// hit; the edge build must not use the GitHub Actions cache backend.
	for _, step := range doc.Jobs["build-edge-image"].Steps {
		for key, value := range step.With {
			if strings.HasPrefix(key, "cache-") && strings.Contains(value, "type=gha") {
				t.Fatalf("build-edge-image step %q uses the GHA cache: %s=%s", step.Name, key, value)
			}
		}
		if strings.Contains(step.Run, "--config-schema-version") {
			t.Fatalf("build-edge-image bakes a per-tag config schema version, which defeats carry-forward:\n%s", step.Run)
		}
	}

	var mergeRun string
	for _, step := range doc.Jobs["merge-edge-image"].Steps {
		mergeRun += step.Run
	}
	if !strings.Contains(mergeRun, "carried_service") {
		t.Fatal("merge-edge-image does not consume the carried edge entry")
	}
}
