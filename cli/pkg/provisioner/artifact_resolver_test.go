package provisioner

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestGitopsRelease(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "channels", "stable.yaml"), []byte("platform_version: vtest\nmanifest: releases/vtest.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "releases", "vtest.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveReleaseArtifactFindsNativeBinary(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
native_binaries:
  - name: privateer
    artifacts:
      - arch: linux-amd64
        url: https://example.test/privateer.tar.gz
        checksum: sha256:abc
`)

	artifact, err := resolveReleaseArtifactFromChannel("privateer", "linux-amd64", "stable", map[string]any{
		"gitops_repository": repo,
	})
	if err != nil {
		t.Fatalf("resolveReleaseArtifactFromChannel: %v", err)
	}
	if artifact.URL != "https://example.test/privateer.tar.gz" {
		t.Fatalf("URL = %q", artifact.URL)
	}
	if artifact.Checksum != "sha256:abc" {
		t.Fatalf("Checksum = %q", artifact.Checksum)
	}
}

func TestImageFromReleaseManifestFindsInfrastructureImage(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
infrastructure:
  - name: nginx
    version: "1.29.3"
    image: nginx:1.29.3-alpine
    digest: sha256:nginxdigest
`)

	image, err := imageFromReleaseManifest("nginx", "stable", map[string]any{
		"gitops_repository": repo,
	})
	if err != nil {
		t.Fatalf("imageFromReleaseManifest: %v", err)
	}
	if image != "nginx:1.29.3-alpine@sha256:nginxdigest" {
		t.Fatalf("image = %q", image)
	}
}

func TestImageFromReleaseManifestDefaultsToDockerHubRegistryImage(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
interfaces:
  - name: chartroom
    image: livepeerframeworks/frameworks-chartroom:vtest
    digest: sha256:dockerhub
    images:
      dockerhub:
        image: livepeerframeworks/frameworks-chartroom:vtest
        digest: sha256:dockerhub
      ghcr:
        image: ghcr.io/livepeer-frameworks/frameworks-chartroom:vtest
        digest: sha256:ghcr
`)

	image, err := imageFromReleaseManifest("chartroom", "stable", map[string]any{
		"gitops_repository": repo,
	})
	if err != nil {
		t.Fatalf("imageFromReleaseManifest: %v", err)
	}
	if image != "livepeerframeworks/frameworks-chartroom:vtest@sha256:dockerhub" {
		t.Fatalf("image = %q", image)
	}
}

func TestImageFromReleaseManifestCanSelectGHCRRegistryImage(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
interfaces:
  - name: chartroom
    image: livepeerframeworks/frameworks-chartroom:vtest
    digest: sha256:dockerhub
    images:
      dockerhub:
        image: livepeerframeworks/frameworks-chartroom:vtest
        digest: sha256:dockerhub
      ghcr:
        image: ghcr.io/livepeer-frameworks/frameworks-chartroom:vtest
        digest: sha256:ghcr
`)

	image, err := imageFromReleaseManifest("chartroom", "stable", map[string]any{
		"gitops_repository": repo,
		"image_registry":    "ghcr",
	})
	if err != nil {
		t.Fatalf("imageFromReleaseManifest: %v", err)
	}
	if image != "ghcr.io/livepeer-frameworks/frameworks-chartroom:vtest@sha256:ghcr" {
		t.Fatalf("image = %q", image)
	}
}

func TestImageFromReleaseManifestPinsInfrastructureByDigest(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
infrastructure:
  - name: caddy
    version: "2.8.4"
    image: caddy:2.8.4
    digest: sha256:226d1f059b75399fe19182893c7184591c07b97afc8dfcf44eeb80c9a77a530f
`)

	image, err := imageFromReleaseManifest("caddy", "stable", map[string]any{
		"gitops_repository": repo,
	})
	if err != nil {
		t.Fatalf("imageFromReleaseManifest: %v", err)
	}
	if want := "caddy:2.8.4@sha256:226d1f059b75399fe19182893c7184591c07b97afc8dfcf44eeb80c9a77a530f"; image != want {
		t.Fatalf("image = %q, want %q", image, want)
	}
}

func TestImageFromReleaseManifestInfrastructureRejectsMissingDigest(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
infrastructure:
  - name: nginx
    version: "1.29.3"
    image: nginx:1.29.3-alpine
`)

	_, err := imageFromReleaseManifest("nginx", "stable", map[string]any{
		"gitops_repository": repo,
	})
	if err == nil {
		t.Fatal("imageFromReleaseManifest accepted infrastructure image without digest")
	}
}

func TestImageFromReleaseManifestPinsExternalDepByDigest(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
external_dependencies:
  - name: go-livepeer
    image: ghcr.io/livepeer-frameworks/go-livepeer:vtest
    digest: sha256:abc123
`)

	image, err := imageFromReleaseManifest("livepeer-gateway", "stable", map[string]any{
		"gitops_repository": repo,
	})
	if err != nil {
		t.Fatalf("imageFromReleaseManifest: %v", err)
	}
	if want := "ghcr.io/livepeer-frameworks/go-livepeer:vtest@sha256:abc123"; image != want {
		t.Fatalf("image = %q, want %q", image, want)
	}
}

func TestImageFromReleaseManifestExternalDepRejectsMissingDigest(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
external_dependencies:
  - name: go-livepeer
    image: ghcr.io/livepeer-frameworks/go-livepeer:vtest
`)

	_, err := imageFromReleaseManifest("livepeer-gateway", "stable", map[string]any{
		"gitops_repository": repo,
	})
	if err == nil {
		t.Fatal("imageFromReleaseManifest accepted external dependency image without digest")
	}
}

func TestBinaryFromExternalDependencyFindsLivepeerArtifact(t *testing.T) {
	repo := writeTestGitopsRelease(t, `
platform_version: vtest
external_dependencies:
  - name: go-livepeer
    image: ghcr.io/livepeer-frameworks/go-livepeer:vtest
    binaries:
      - name: livepeer-linux-amd64.tar.gz
        url: https://example.test/livepeer-linux-amd64.tar.gz
      - name: livepeer-linux-gpu-amd64.tar.gz
        url: https://example.test/livepeer-linux-gpu-amd64.tar.gz
`)
	manifest, err := fetchGitopsManifest("stable", "latest", map[string]any{
		"gitops_repository": repo,
	})
	if err != nil {
		t.Fatalf("fetchGitopsManifest: %v", err)
	}

	bin, depName := binaryFromExternalDependency("livepeer-gateway", "linux", "amd64", manifest)
	if depName != "go-livepeer" {
		t.Fatalf("depName = %q", depName)
	}
	if bin == nil || bin.URL != "https://example.test/livepeer-linux-amd64.tar.gz" {
		t.Fatalf("bin = %#v", bin)
	}
}
