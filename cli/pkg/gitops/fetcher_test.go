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
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		t.Fatalf("failed to create cache dir: %v", err)
	}
	if err := os.WriteFile(cachePath, manifestYAML, 0644); err != nil {
		t.Fatalf("failed to write cache: %v", err)
	}
	if err := fetcher.writeMetadata(metaPath, time.Now().Add(-2*time.Second)); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
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
	if err := fetcher.saveToCache("stable", "v1.2.3", manifest); err != nil {
		t.Fatalf("failed to save cache: %v", err)
	}

	got, err := fetcher.Fetch("stable", "1.2.3")
	if err != nil {
		t.Fatalf("expected cache hit, got error: %v", err)
	}
	if got.PlatformVersion != "v1.2.3" {
		t.Fatalf("expected cached manifest, got %s", got.PlatformVersion)
	}
}
