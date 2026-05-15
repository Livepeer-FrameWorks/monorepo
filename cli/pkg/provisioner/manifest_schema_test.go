package provisioner

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"frameworks/cli/pkg/gitops"
	"gopkg.in/yaml.v3"
)

// TestLatestReleaseManifestHasResolvableIdentityForEveryComponent walks the
// most recent published release manifest in ../gitops/releases/ and asserts
// that for every entry the resolvers used by provisioning return a
// content-addressed identity for the mode that entry advertises.
//
//   - services[]: Docker mode must resolve to image@sha256:..., native mode (if
//     the service has a native_binaries[] companion) must resolve to a URL +
//     checksum for at least linux-amd64.
//   - external_dependencies[]: Docker mode must resolve to image@sha256:...,
//     and every dep that ships binaries must have at least one deployable asset.
//     Missing checksums in immutable published history are logged; current
//     source config is enforced separately below.
//   - infrastructure[] source config requires every image to carry a digest.
//
// The test is skipped (not failed) if the gitops sibling repo isn't present;
// running with the sibling is what guards real-world regressions in CI.
func TestLatestReleaseManifestHasResolvableIdentityForEveryComponent(t *testing.T) {
	gitopsDir := findGitopsReleasesDir(t)
	if gitopsDir == "" {
		t.Skip("gitops sibling repo not found; set FRAMEWORKS_GITOPS_DIR or run from a checkout with ../gitops present")
	}

	manifestPath := latestReleaseManifest(t, gitopsDir)
	if manifestPath == "" {
		t.Skip("no releases/v*.yaml found in gitops sibling repo")
	}

	manifest := loadManifest(t, manifestPath)

	// Set up a fixture gitops dir the resolver can fetch from. The resolver
	// expects channels/stable.yaml -> releases/<file>. We synthesize that
	// pointing at the real manifest we just loaded so the resolver path is
	// exercised end-to-end against real production data.
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	tag := manifest.PlatformVersion
	if tag == "" {
		tag = "vtest"
	}
	if err := os.WriteFile(filepath.Join(repoDir, "channels", "stable.yaml"), []byte("platform_version: "+tag+"\nmanifest: releases/"+tag+".yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "releases", tag+".yaml"), manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"gitops_repository": repoDir}

	// services[] — first-party Docker images must be digest-pinned; native
	// binaries for at least linux-amd64 must be present.
	for _, svc := range manifest.Services {
		t.Run("service/"+svc.Name+"/docker", func(t *testing.T) {
			image, err := imageFromReleaseManifest(svc.Name, "stable", meta)
			if err != nil {
				t.Fatalf("imageFromReleaseManifest(%s): %v", svc.Name, err)
			}
			if !strings.Contains(image, "@sha256:") {
				t.Fatalf("Docker identity for %s is not digest-pinned: %q", svc.Name, image)
			}
		})

		if hasNativeBinary(manifest, svc.Name) {
			t.Run("service/"+svc.Name+"/native", func(t *testing.T) {
				art, err := resolveReleaseArtifactFromChannel(svc.Name, "linux-amd64", "stable", meta)
				if err != nil {
					t.Fatalf("resolveReleaseArtifactFromChannel(%s linux-amd64): %v", svc.Name, err)
				}
				if art.URL == "" {
					t.Fatalf("native URL for %s linux-amd64 is empty", svc.Name)
				}
				if !strings.HasPrefix(art.Checksum, "sha256:") && !strings.HasPrefix(art.Checksum, "sha512:") {
					t.Fatalf("native checksum for %s linux-amd64 is not a recognized algo: %q", svc.Name, art.Checksum)
				}
			})
		}
	}

	// external_dependencies[] — Docker pin must include digest; per-arch
	// binaries must each ship a checksum.
	for _, dep := range manifest.ExternalDependencies {
		if dep.Image != "" {
			t.Run("external_dep/"+dep.Name+"/docker", func(t *testing.T) {
				if dep.Digest == "" {
					t.Fatalf("external_dependency %s has image %q but no digest — Docker deploys cannot pin by content", dep.Name, dep.Image)
				}
			})
		}
		if len(dep.Binaries) > 0 {
			t.Run("external_dep/"+dep.Name+"/native", func(t *testing.T) {
				deployableCount := 0
				for _, bin := range dep.Binaries {
					if bin.URL == "" || isMetadataAsset(bin.Name) {
						continue
					}
					deployableCount++
					if bin.Checksum == "" {
						t.Logf("external_dependency %s binary %q has URL but no checksum in published manifest — immutable history; new upstream releases should include checksums", dep.Name, bin.Name)
					}
				}
				if deployableCount == 0 {
					t.Fatalf("external_dependency %s has no deployable binaries (all are metadata or empty URL)", dep.Name)
				}
			})
		}
	}

	// infrastructure[] entries in published manifests are immutable. Warn on
	// missing digests here; enforce the strict policy against the source file in
	// TestConfigInfrastructureYamlPinsEveryImageByDigest.
	for _, infra := range manifest.Infrastructure {
		if infra.Image == "" {
			continue
		}
		if infra.Digest == "" {
			t.Logf("infrastructure/%s has image %q but no digest in published manifest; config/infrastructure.yaml enforces digest pinning", infra.Name, infra.Image)
		}
	}
}

// TestConfigInfrastructureYamlPinsEveryImageByDigest enforces the
// infra-image-pinning policy against the source-of-truth file
// (config/infrastructure.yaml). Older published manifests in gitops are
// exempt (immutable history); the next release built from this config
// must have a digest for every entry with an image.
//
// The test is skipped when the file isn't present (e.g. an isolated
// module check), which keeps the package testable in isolation.
func TestConfigInfrastructureYamlPinsEveryImageByDigest(t *testing.T) {
	path := findInfrastructureYaml(t)
	if path == "" {
		t.Skip("config/infrastructure.yaml not found; run from a checkout that contains the monorepo root")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Infrastructure []gitops.InfrastructureEntry `yaml:"infrastructure"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	for _, infra := range doc.Infrastructure {
		if infra.Image == "" {
			continue
		}
		if infra.Digest == "" {
			t.Errorf("infrastructure/%s has image %q but no digest — pin it with `docker buildx imagetools inspect %q --raw | sha256sum`", infra.Name, infra.Image, infra.Image)
		}
	}
}

func findInfrastructureYaml(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "config", "infrastructure.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// findGitopsReleasesDir locates the gitops sibling repo. It prefers the
// FRAMEWORKS_GITOPS_DIR env override (used in CI), and otherwise walks up
// from the test's working directory looking for a sibling named "gitops"
// with a releases/ subdir.
func findGitopsReleasesDir(t *testing.T) string {
	t.Helper()
	if env := strings.TrimSpace(os.Getenv("FRAMEWORKS_GITOPS_DIR")); env != "" {
		releases := filepath.Join(env, "releases")
		if dirExists(releases) {
			return env
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "..", "gitops")
		if dirExists(filepath.Join(candidate, "releases")) {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func latestReleaseManifest(t *testing.T, gitopsDir string) string {
	t.Helper()
	releasesDir := filepath.Join(gitopsDir, "releases")
	entries, err := os.ReadDir(releasesDir)
	if err != nil {
		return ""
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "v") || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if strings.Contains(name, "-rc") {
			continue // prefer stable releases for the schema baseline
		}
		files = append(files, name)
	}
	if len(files) == 0 {
		return ""
	}
	sort.Strings(files) // lexicographic on semver-ish tags is fine within a major.minor
	return filepath.Join(releasesDir, files[len(files)-1])
}

func loadManifest(t *testing.T, path string) *gitops.Manifest {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m gitops.Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return &m
}

// isMetadataAsset reports whether an external_dependency binary entry is a
// release-metadata file rather than a deployable artefact. mistserver ships
// docker-tag.txt to decouple its GitHub release tag from its Docker tag —
// the release workflow reads it at manifest-generation time and never
// installs it as a binary.
func isMetadataAsset(name string) bool {
	switch name {
	case "docker-tag.txt":
		return true
	}
	return false
}

func hasNativeBinary(manifest *gitops.Manifest, name string) bool {
	for _, nb := range manifest.NativeBinaries {
		if nb.Name == name && len(nb.Artifacts) > 0 {
			return true
		}
	}
	return false
}
