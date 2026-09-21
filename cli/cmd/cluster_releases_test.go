package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNormalizeReleaseTargetVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{" latest ", ""},
		{"LATEST", ""},
		{"v1.2.3", "v1.2.3"},
	}

	for _, tc := range tests {
		if got := normalizeReleaseTargetVersion(tc.input); got != tc.want {
			t.Fatalf("normalizeReleaseTargetVersion(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeReleaseTargetChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{" stable ", "stable"},
		{"STABLE", "stable"},
		{"Rc", "rc"},
	}

	for _, tc := range tests {
		got, err := normalizeReleaseTargetChannel(tc.input)
		if err != nil {
			t.Fatalf("normalizeReleaseTargetChannel(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeReleaseTargetChannel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeReleaseTargetChannelRejectsCandidateSelector(t *testing.T) {
	t.Parallel()

	if _, err := normalizeReleaseTargetChannel("candidate"); err == nil {
		t.Fatal("normalizeReleaseTargetChannel accepted candidate without resolving a GitOps manifest; want error")
	}
}

func TestEdgeReleaseCatalogChannelUsesConcreteManifestVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version string
		want    string
	}{
		{version: "v1.2.3", want: "stable"},
		{version: "v1.2.4-rc1", want: "rc"},
	}
	for _, tc := range tests {
		got, err := edgeReleaseCatalogChannel(&gitops.Manifest{PlatformVersion: tc.version})
		if err != nil {
			t.Fatalf("edgeReleaseCatalogChannel(%q): %v", tc.version, err)
		}
		if got != tc.want {
			t.Fatalf("edgeReleaseCatalogChannel(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}

func TestEdgeReleaseCatalogChannelRejectsNonConcreteVersions(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"", "stable", "rc", "candidate", "latest", "garbage", "v1.2", "v1.2.3-01"} {
		t.Run(version, func(t *testing.T) {
			if channel, err := edgeReleaseCatalogChannel(&gitops.Manifest{PlatformVersion: version}); err == nil {
				t.Fatalf("accepted non-concrete version %q as %q", version, channel)
			}
		})
	}
	if _, err := edgeReleaseCatalogChannel(nil); err == nil {
		t.Fatal("accepted nil manifest")
	}
}

func TestContextTargetsManifestRequiresDeclaredCluster(t *testing.T) {
	t.Parallel()

	manifest := &inventory.Manifest{Clusters: map[string]inventory.ClusterConfig{
		"staging-core": {},
	}}
	if !contextTargetsManifest(fwcfg.Context{ClusterID: "staging-core"}, manifest) {
		t.Fatal("declared staging cluster should match the selected manifest")
	}
	if contextTargetsManifest(fwcfg.Context{ClusterID: "production-core"}, manifest) {
		t.Fatal("an unrelated active context must not override the selected manifest")
	}
	if contextTargetsManifest(fwcfg.Context{}, manifest) {
		t.Fatal("an empty context cluster must not override the selected manifest")
	}
}

func TestNormalizeReleaseTargetChannelRejectsUnknown(t *testing.T) {
	t.Parallel()

	if _, err := normalizeReleaseTargetChannel("nightly"); err == nil {
		t.Fatal("normalizeReleaseTargetChannel accepted nightly; want error")
	}
}

func TestNormalizeReleaseTargetChannelRejectsEdgeAsTrack(t *testing.T) {
	t.Parallel()

	if _, err := normalizeReleaseTargetChannel("edge"); err == nil {
		t.Fatal("normalizeReleaseTargetChannel accepted edge as a release track; want error")
	}
}

func TestEdgeReleaseHasUpdateableComponent(t *testing.T) {
	t.Parallel()

	if edgeReleaseHasUpdateableComponent(map[string]edgeReleaseComponentSpec{"config_schema": {Version: "4"}}) {
		t.Fatal("config_schema-only release should not be updateable")
	}
	if !edgeReleaseHasUpdateableComponent(map[string]edgeReleaseComponentSpec{"mist": {Version: "v1.2.3"}}) {
		t.Fatal("mist release should be updateable")
	}
}

// TestEdgeExternalArtifactsParsesRealMistAssetNames pins that catalog
// publishing understands actual MistServer release asset filenames: full
// names carrying platform tokens, plus debug bundles and docker-tag.txt
// that must be skipped — never treated as platform keys.
func TestEdgeExternalArtifactsParsesRealMistAssetNames(t *testing.T) {
	t.Parallel()

	dep := &gitops.ExternalDependency{
		Name: "mistserver",
		Binaries: []gitops.ExternalBinary{
			{Name: "docker-tag.txt", URL: "https://example.test/docker-tag.txt"},
			{Name: "mistserver-linux-amd64-development-c97caf1.tar.gz.sha256", URL: "https://example.test/linux-amd64.tar.gz.sha256"},
			{Name: "mistserver-linux-amd64-debug-development-c97caf1.tar.gz", URL: "https://example.test/linux-amd64-debug.tar.gz", Checksum: "sha256:dd"},
			{Name: "mistserver-linux-amd64-development-c97caf1.tar.gz", URL: "https://example.test/linux-amd64.tar.gz", Checksum: "sha256:aa"},
			{Name: "mistserver-linux-arm64-development-c97caf1.tar.gz", URL: "https://example.test/linux-arm64.tar.gz", Checksum: "sha256:bb"},
			{Name: "mistserver-darwin-arm64-development-c97caf1.tar.gz", URL: "https://example.test/darwin-arm64.tar.gz", Checksum: "sha256:cc"},
		},
	}

	artifacts, err := edgeExternalArtifacts(dep, "", "")
	if err != nil {
		t.Fatalf("edgeExternalArtifacts (unfiltered) returned error: %v", err)
	}
	want := map[string]string{
		"linux/amd64":  "https://example.test/linux-amd64.tar.gz",
		"linux/arm64":  "https://example.test/linux-arm64.tar.gz",
		"darwin/arm64": "https://example.test/darwin-arm64.tar.gz",
	}
	if len(artifacts) != len(want) {
		t.Fatalf("artifacts = %#v, want exactly %d platform keys", artifacts, len(want))
	}
	for key, url := range want {
		if artifacts[key].ArtifactURL != url {
			t.Fatalf("artifacts[%q].ArtifactURL = %q, want %q", key, artifacts[key].ArtifactURL, url)
		}
	}

	filtered, err := edgeExternalArtifacts(dep, "linux", "amd64")
	if err != nil {
		t.Fatalf("edgeExternalArtifacts (filtered) returned error: %v", err)
	}
	if filtered["linux/amd64"].ArtifactURL != "https://example.test/linux-amd64.tar.gz" {
		t.Fatalf("filtered artifact = %#v, want the linux/amd64 runtime tarball", filtered)
	}
	if filtered["linux/amd64"].Checksum != "sha256:aa" {
		t.Fatalf("filtered checksum = %q, want sha256:aa", filtered["linux/amd64"].Checksum)
	}
}

func TestEdgeExternalVariantsPublishesExactIndexedArtifacts(t *testing.T) {
	t.Parallel()

	dep := &gitops.ExternalDependency{ReleaseIndex: &gitops.ExternalReleaseIndex{
		Profiles: map[string]gitops.ExternalProfile{
			"cpu": {Platforms: map[string]gitops.ExternalPlatform{
				"linux-amd64": {Artifact: &gitops.ExternalBinary{Name: "cpu.tgz", URL: "https://example.test/cpu.tgz", Checksum: "sha256:" + strings.Repeat("a", 64)}},
			}},
			"cuda": {Platforms: map[string]gitops.ExternalPlatform{
				"linux-amd64": {Artifact: &gitops.ExternalBinary{Name: "cuda.tgz", URL: "https://example.test/cuda.tgz", Checksum: "sha256:" + strings.Repeat("b", 64)}},
			}},
		},
	}}

	variants := edgeExternalVariants(dep, "", "")
	if len(variants) != 1 || variants["cuda"].Artifacts["linux/amd64"].ArtifactURL != "https://example.test/cuda.tgz" {
		t.Fatalf("variants = %#v, want only exact CUDA artifact", variants)
	}
	filtered := edgeExternalVariants(dep, "linux", "arm64")
	if len(filtered) != 0 {
		t.Fatalf("filtered variants = %#v, want none for unsupported platform", filtered)
	}
}

func TestPlatformKeyFromArtifactName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"linux-amd64":   "linux/amd64",
		"darwin/arm64":  "darwin/arm64",
		" LINUX-ARM64 ": "linux/arm64",
	}
	for input, want := range tests {
		got, ok := platformKeyFromArtifactName(input)
		if !ok {
			t.Fatalf("platformKeyFromArtifactName(%q) returned ok=false", input)
		}
		if got != want {
			t.Fatalf("platformKeyFromArtifactName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRetryEdgeReleaseSyncRPCRetriesSchemaVersionMismatch(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := retryEdgeReleaseSyncRPCWithBackoff(context.Background(), 3, time.Nanosecond, func() error {
		attempts++
		if attempts == 1 {
			return errors.New("rpc error: code = Internal desc = get release target: pq: schema version mismatch for table x: expected 92, got 91 (40001)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryEdgeReleaseSyncRPCWithBackoff returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRetryEdgeReleaseSyncRPCDoesNotRetryPermanentError(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := retryEdgeReleaseSyncRPCWithBackoff(context.Background(), 3, time.Nanosecond, func() error {
		attempts++
		return errors.New("rpc error: code = PermissionDenied desc = provider authority required")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestRetryEdgeReleaseSyncRPCRetriesTransientGRPCDeadline(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := retryEdgeReleaseSyncRPCWithBackoff(context.Background(), 3, time.Nanosecond, func() error {
		attempts++
		if attempts == 1 {
			return status.Error(codes.DeadlineExceeded, "cold control-plane connection")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryEdgeReleaseSyncRPCWithBackoff returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRetryEdgeReleaseSyncRPCDoesNotStartAfterOperationDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err := retryEdgeReleaseSyncRPCWithBackoff(ctx, 3, time.Nanosecond, func() error {
		attempts++
		return status.Error(codes.Unavailable, "down")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context cancellation", err)
	}
	if attempts != 0 {
		t.Fatalf("attempts after operation deadline = %d, want 0", attempts)
	}
}
