package gitops

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveVersionNormalization(t *testing.T) {
	tests := []struct {
		input   string
		channel string
		version string
	}{
		{input: "", channel: "stable", version: "latest"},
		{input: "latest", channel: "stable", version: "latest"},
		{input: "stable", channel: "stable", version: "latest"},
		{input: "rc", channel: "rc", version: "latest"},
		{input: "v1.2.3", channel: "stable", version: "v1.2.3"},
		{input: "1.2.3", channel: "stable", version: "v1.2.3"},
		{input: "1.2.3-rc1", channel: "stable", version: "v1.2.3-rc1"},
	}

	for _, tc := range tests {
		channel, version := ResolveVersion(tc.input)
		if channel != tc.channel || version != tc.version {
			t.Fatalf("ResolveVersion(%q) = %s/%s, expected %s/%s", tc.input, channel, version, tc.channel, tc.version)
		}
	}
}

func TestFetchPinnedVersion(t *testing.T) {
	t.Parallel()

	manifestYAML := []byte("platform_version: v1.2.3\nservices: []\nnative_binaries: []\ninterfaces: []\ninfrastructure: []\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/v1.2.3.yaml" {
			t.Errorf("unexpected path: %s (expected /releases/v1.2.3.yaml)", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(manifestYAML)
	}))
	t.Cleanup(server.Close)

	fetcher, err := NewFetcher(FetchOptions{
		Repository: server.URL,
		CacheDir:   t.TempDir(),
		RetryCount: 1,
	})
	if err != nil {
		t.Fatalf("failed to create fetcher: %v", err)
	}

	manifest, err := fetcher.Fetch("stable", "v1.2.3")
	if err != nil {
		t.Fatalf("expected fetch to succeed: %v", err)
	}
	if manifest.PlatformVersion != "v1.2.3" {
		t.Fatalf("unexpected version: %s", manifest.PlatformVersion)
	}
}

func TestFetchLatestResolvesChannelPointer(t *testing.T) {
	t.Parallel()

	channelYAML := []byte("platform_version: v2.0.0\nmanifest: releases/v2.0.0.yaml\nupdated_at: 2026-01-01T00:00:00Z\n")
	manifestYAML := []byte("platform_version: v2.0.0\nservices: []\nnative_binaries: []\ninterfaces: []\ninfrastructure: []\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/rc.yaml":
			_, _ = w.Write(channelYAML)
		case "/releases/v2.0.0.yaml":
			_, _ = w.Write(manifestYAML)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	fetcher, err := NewFetcher(FetchOptions{
		Repository: server.URL,
		CacheDir:   t.TempDir(),
		RetryCount: 1,
	})
	if err != nil {
		t.Fatalf("failed to create fetcher: %v", err)
	}

	manifest, err := fetcher.Fetch("rc", "latest")
	if err != nil {
		t.Fatalf("expected fetch to succeed: %v", err)
	}
	if manifest.PlatformVersion != "v2.0.0" {
		t.Fatalf("unexpected version: %s", manifest.PlatformVersion)
	}
}

func TestFetchRetriesTransientFailure(t *testing.T) {
	t.Parallel()

	attempts := 0
	manifestYAML := []byte("platform_version: v1.2.3\nservices: []\nnative_binaries: []\ninterfaces: []\ninfrastructure: []\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(manifestYAML)
	}))
	t.Cleanup(server.Close)

	fetcher, err := NewFetcher(FetchOptions{
		Repository:     server.URL,
		CacheDir:       t.TempDir(),
		RetryCount:     3,
		RetryDelay:     1 * time.Millisecond,
		PinnedTTL:      1 * time.Hour,
		PinnedMaxStale: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("failed to create fetcher: %v", err)
	}

	manifest, err := fetcher.Fetch("stable", "v1.2.3")
	if err != nil {
		t.Fatalf("expected fetch to succeed: %v", err)
	}
	if manifest.PlatformVersion != "v1.2.3" {
		t.Fatalf("unexpected manifest version: %s", manifest.PlatformVersion)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestFetchStaleCacheFallbackOnFetchFailure(t *testing.T) {
	t.Parallel()

	manifestYAML := []byte("platform_version: cached\nservices: []\nnative_binaries: []\ninterfaces: []\ninfrastructure: []\n")
	cacheDir := t.TempDir()

	fetcher, err := NewFetcher(FetchOptions{
		Repository:     "https://example.com",
		CacheDir:       cacheDir,
		PinnedTTL:      1 * time.Second,
		PinnedMaxStale: 1 * time.Hour,
		RetryCount:     1,
		RetryDelay:     1 * time.Millisecond,
		LatestTTL:      1 * time.Second,
		LatestMaxStale: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("failed to create fetcher: %v", err)
	}

	cachePath, metaPath := fetcher.cachePaths("stable", "v1.2.3")
	if mkdirErr := os.MkdirAll(filepath.Dir(cachePath), 0755); mkdirErr != nil {
		t.Fatalf("failed to create cache dir: %v", mkdirErr)
	}
	if writeErr := os.WriteFile(cachePath, manifestYAML, 0644); writeErr != nil {
		t.Fatalf("failed to write cache: %v", writeErr)
	}
	if metaErr := fetcher.writeMetadata(metaPath, time.Now().Add(-2*time.Second)); metaErr != nil {
		t.Fatalf("failed to write metadata: %v", metaErr)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	fetcher.repository = server.URL

	manifest, err := fetcher.Fetch("stable", "v1.2.3")
	if err != nil {
		t.Fatalf("expected cached fallback, got error: %v", err)
	}
	if manifest.PlatformVersion != "cached" {
		t.Fatalf("expected cached manifest, got %s", manifest.PlatformVersion)
	}
}

func TestFetchLocalResolvesChannelPointer(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "channels"), 0755)
	os.MkdirAll(filepath.Join(dir, "releases"), 0755)
	os.WriteFile(filepath.Join(dir, "channels", "stable.yaml"), []byte("platform_version: v3.0.0\nmanifest: releases/v3.0.0.yaml\n"), 0644)
	os.WriteFile(filepath.Join(dir, "releases", "v3.0.0.yaml"), []byte("platform_version: v3.0.0\nservices: []\nnative_binaries: []\ninterfaces: []\ninfrastructure: []\n"), 0644)

	fetcher, err := NewFetcher(FetchOptions{
		Repository: dir,
		CacheDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("failed to create fetcher: %v", err)
	}

	manifest, err := fetcher.Fetch("stable", "latest")
	if err != nil {
		t.Fatalf("expected fetch to succeed: %v", err)
	}
	if manifest.PlatformVersion != "v3.0.0" {
		t.Fatalf("expected v3.0.0, got %s", manifest.PlatformVersion)
	}
}

func TestGetServiceInfoBinaryOnlyService(t *testing.T) {
	m := &Manifest{
		NativeBinaries: []NativeBinary{
			{
				Name: "privateer",
				Artifacts: []Artifact{
					{Arch: "linux-amd64", File: "privateer-linux-amd64.tar.gz", URL: "https://example.com/privateer-linux-amd64.tar.gz"},
					{Arch: "linux-arm64", File: "privateer-linux-arm64.tar.gz"},
				},
			},
		},
	}

	info, err := m.GetServiceInfo("privateer")
	if err != nil {
		t.Fatalf("expected to find privateer: %v", err)
	}
	if info.Name != "privateer" {
		t.Fatalf("expected name privateer, got %s", info.Name)
	}

	// URL should be preferred over File when present
	url, err := info.GetBinaryURL("linux", "amd64")
	if err != nil {
		t.Fatalf("expected amd64 binary: %v", err)
	}
	if url != "https://example.com/privateer-linux-amd64.tar.gz" {
		t.Fatalf("expected full URL, got %s", url)
	}

	// Falls back to File when URL is empty
	url, err = info.GetBinaryURL("linux", "arm64")
	if err != nil {
		t.Fatalf("expected arm64 binary: %v", err)
	}
	if url != "privateer-linux-arm64.tar.gz" {
		t.Fatalf("expected filename fallback, got %s", url)
	}

	// Not found
	_, err = m.GetServiceInfo("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent service")
	}
}

func TestGetServiceInfoPrefersURLOverFile(t *testing.T) {
	m := &Manifest{
		Services: []ServiceEntry{
			{Name: "bridge", ServiceVersion: "0.1.0", Image: "img", Digest: "sha256:abc"},
		},
		NativeBinaries: []NativeBinary{
			{
				Name: "bridge",
				Artifacts: []Artifact{
					{Arch: "linux-amd64", File: "bridge.tar.gz", URL: "https://example.com/bridge.tar.gz"},
				},
			},
		},
	}

	info, err := m.GetServiceInfo("bridge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	url, err := info.GetBinaryURL("linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://example.com/bridge.tar.gz" {
		t.Fatalf("expected URL, got %s", url)
	}
}

func TestFetchUsesNormalizedVersionCacheKey(t *testing.T) {
	t.Parallel()

	fetcher, err := NewFetcher(FetchOptions{
		CacheDir:       t.TempDir(),
		Offline:        true,
		PinnedTTL:      1 * time.Hour,
		PinnedMaxStale: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("failed to create fetcher: %v", err)
	}

	manifest := &Manifest{PlatformVersion: "v1.2.3"}
	if saveErr := fetcher.saveToCache("stable", "v1.2.3", manifest); saveErr != nil {
		t.Fatalf("failed to save cache: %v", saveErr)
	}

	got, err := fetcher.Fetch("stable", "1.2.3")
	if err != nil {
		t.Fatalf("expected cache hit, got error: %v", err)
	}
	if got.PlatformVersion != "v1.2.3" {
		t.Fatalf("expected cached manifest, got %s", got.PlatformVersion)
	}
}
