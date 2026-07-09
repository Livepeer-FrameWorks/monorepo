package cmd

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/cli/pkg/gitops"
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
