package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultRepository = "https://raw.githubusercontent.com/Livepeer-FrameWorks/gitops/main"
	DefaultCacheDir   = "~/.frameworks/cache/manifests"
)

// Fetcher fetches and caches manifests from the gitops repository
type Fetcher struct {
	repository     string
	cacheDir       string
	client         *http.Client
	offline        bool
	latestTTL      time.Duration
	latestMaxStale time.Duration
	pinnedTTL      time.Duration
	pinnedMaxStale time.Duration
	retryCount     int
	retryDelay     time.Duration
}

type cacheMetadata struct {
	FetchedAt time.Time `json:"fetched_at"`
}

// NewFetcher creates a new manifest fetcher
func NewFetcher(opts FetchOptions) (*Fetcher, error) {
	repo := opts.Repository
	if repo == "" {
		repo = DefaultRepository
	}

	cacheDir := opts.CacheDir
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".frameworks", "cache", "manifests")
	}

	// Create cache directory
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	latestTTL := opts.LatestTTL
	if latestTTL == 0 {
		latestTTL = 15 * time.Minute
	}
	latestMaxStale := opts.LatestMaxStale
	if latestMaxStale == 0 {
		latestMaxStale = 1 * time.Hour
	}
	pinnedTTL := opts.PinnedTTL
	if pinnedTTL == 0 {
		pinnedTTL = 24 * time.Hour
	}
	pinnedMaxStale := opts.PinnedMaxStale
	if pinnedMaxStale == 0 {
		pinnedMaxStale = 7 * 24 * time.Hour
	}
	retryCount := opts.RetryCount
	if retryCount == 0 {
		retryCount = 3
	}
	retryDelay := opts.RetryDelay
	if retryDelay == 0 {
		retryDelay = 250 * time.Millisecond
	}

	return &Fetcher{
		repository:     repo,
		cacheDir:       cacheDir,
		offline:        opts.Offline,
		latestTTL:      latestTTL,
		latestMaxStale: latestMaxStale,
		pinnedTTL:      pinnedTTL,
		pinnedMaxStale: pinnedMaxStale,
		retryCount:     retryCount,
		retryDelay:     retryDelay,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// Fetch retrieves a manifest for a specific channel and version
func (f *Fetcher) Fetch(channel, version string) (*Manifest, error) {
	// Default channel
	if channel == "" {
		channel = "stable"
	}

	// Default version
	if version == "" {
		version = "latest"
	}
	version = normalizeVersion(version)

	ttl, maxStale := f.cachePolicy(version)

	// Check cache first
	cached, cachedAt, cacheErr := f.loadFromCache(channel, version)
	if cacheErr == nil {
		age := time.Since(cachedAt)
		if age <= ttl {
			return cached, nil
		}
		if f.offline && age <= maxStale {
			return cached, nil
		}
	}

	if f.offline {
		return nil, fmt.Errorf("offline and no usable cache for %s/%s", channel, version)
	}

	// Check if repository is a local path
	if f.isLocalPath(f.repository) {
		manifest, errFetch := f.fetchFromLocal(channel, version)
		if errFetch != nil {
			if cacheErr == nil && time.Since(cachedAt) <= maxStale {
				fmt.Printf("Warning: using stale cached manifest after local fetch failure: %v\n", errFetch)
				return cached, nil
			}
			return nil, fmt.Errorf("failed to fetch from local path: %w", errFetch)
		}
		if err := f.saveToCache(channel, version, manifest); err != nil {
			fmt.Printf("Warning: failed to cache manifest: %v\n", err)
		}
		return manifest, nil
	}

	// Fetch from repository
	manifest, err := f.fetchFromRepo(channel, version)
	if err != nil {
		if cacheErr == nil && time.Since(cachedAt) <= maxStale {
			fmt.Printf("Warning: using stale cached manifest after fetch failure: %v\n", err)
			return cached, nil
		}
		return nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}

	// Save to cache
	if err := f.saveToCache(channel, version, manifest); err != nil {
		// Non-fatal - just log
		fmt.Printf("Warning: failed to cache manifest: %v\n", err)
	}

	return manifest, nil
}

// fetchFromRepo downloads a manifest from the gitops repository
func (f *Fetcher) fetchFromRepo(channel, version string) (*Manifest, error) {
	// Build URL: https://raw.githubusercontent.com/Livepeer-FrameWorks/gitops/main/manifests/{channel}/{version}.yaml
	url := fmt.Sprintf("%s/manifests/%s/%s.yaml", f.repository, channel, version)

	var lastErr error
	for attempt := 1; attempt <= f.retryCount; attempt++ {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build request: %w", err)
		}

		resp, err := f.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to download manifest: %w", err)
		} else {
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("manifest not found: %s (HTTP %d)", url, resp.StatusCode)
				if !shouldRetryStatus(resp.StatusCode) {
					return nil, lastErr
				}
			} else {
				// Read body
				data, err := io.ReadAll(resp.Body)
				if err != nil {
					return nil, fmt.Errorf("failed to read manifest: %w", err)
				}

				// Parse YAML
				var manifest Manifest
				if err := yaml.Unmarshal(data, &manifest); err != nil {
					return nil, fmt.Errorf("failed to parse manifest: %w", err)
				}

				return &manifest, nil
			}
		}

		if attempt < f.retryCount {
			if delay := f.retryDelay * time.Duration(attempt); delay > 0 {
				time.Sleep(delay)
			}
		}
	}

	return nil, lastErr
}

// loadFromCache loads a manifest from local cache
func (f *Fetcher) loadFromCache(channel, version string) (*Manifest, time.Time, error) {
	cachePath, metaPath := f.cachePaths(channel, version)

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, time.Time{}, err
	}

	var manifest Manifest
	if unmarshalErr := yaml.Unmarshal(data, &manifest); unmarshalErr != nil {
		return nil, time.Time{}, unmarshalErr
	}

	fetchedAt, err := f.readMetadata(metaPath)
	if err != nil {
		info, statErr := os.Stat(cachePath)
		if statErr == nil {
			fetchedAt = info.ModTime()
		} else {
			fetchedAt = time.Time{}
		}
	}

	return &manifest, fetchedAt, nil
}

// saveToCache saves a manifest to local cache
func (f *Fetcher) saveToCache(channel, version string, manifest *Manifest) error {
	channelDir := filepath.Join(f.cacheDir, channel)
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		return err
	}

	cachePath, metaPath := f.cachePaths(channel, version)

	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}

	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		return err
	}

	return f.writeMetadata(metaPath, time.Now().UTC())
}

// ResolveVersion resolves a version string to channel and version
func ResolveVersion(versionStr string) (channel, version string) {
	if versionStr == "" {
		return "stable", "latest"
	}

	// If it looks like a version tag (v1.2.3), use stable channel
	if len(versionStr) > 0 && versionStr[0] == 'v' {
		return "stable", versionStr
	}

	// If it's a channel name (stable, rc), use latest
	switch versionStr {
	case "stable", "rc":
		return versionStr, "latest"
	case "latest":
		return "stable", "latest"
	default:
		// Default to stable channel with specific version
		return "stable", normalizeVersion(versionStr)
	}
}

// GetServiceInfo retrieves service information from a manifest
func (m *Manifest) GetServiceInfo(serviceName string) (*ServiceInfo, error) {
	// Search in services
	for _, svc := range m.Services {
		if svc.Name == serviceName {
			info := &ServiceInfo{
				Name:      svc.Name,
				Version:   svc.ServiceVersion,
				Image:     svc.Image,
				Digest:    svc.Digest,
				FullImage: fmt.Sprintf("%s@%s", svc.Image, svc.Digest),
				Binaries:  make(map[string]string),
			}

			// Find binaries for this service
			for _, nb := range m.NativeBinaries {
				if nb.Name == serviceName {
					for _, artifact := range nb.Artifacts {
						info.Binaries[artifact.Arch] = artifact.File
					}
					break
				}
			}

			return info, nil
		}
	}

	// Search in interfaces
	for _, iface := range m.Interfaces {
		if iface.Name == serviceName {
			return &ServiceInfo{
				Name:      iface.Name,
				Version:   "", // Interfaces don't have separate versions
				Image:     iface.Image,
				Digest:    iface.Digest,
				FullImage: fmt.Sprintf("%s@%s", iface.Image, iface.Digest),
				Binaries:  make(map[string]string),
			}, nil
		}
	}

	return nil, fmt.Errorf("service %s not found in manifest", serviceName)
}

// GetBinaryURL returns the download URL for a binary
func (s *ServiceInfo) GetBinaryURL(os, arch string) (string, error) {
	key := fmt.Sprintf("%s-%s", os, arch)
	filename, ok := s.Binaries[key]
	if !ok {
		return "", fmt.Errorf("binary not available for %s", key)
	}

	// For now, return the filename - provisioner will need to construct full URL
	// based on GitHub releases or local path
	return filename, nil
}

// isLocalPath checks if a path is local (starts with / or ./)
func (f *Fetcher) isLocalPath(path string) bool {
	return len(path) > 0 && (path[0] == '/' || path[0] == '.' || path[0] == '~')
}

func (f *Fetcher) cachePolicy(version string) (time.Duration, time.Duration) {
	if version == "latest" {
		return f.latestTTL, f.latestMaxStale
	}
	return f.pinnedTTL, f.pinnedMaxStale
}

func (f *Fetcher) cachePaths(channel, version string) (string, string) {
	channelDir := filepath.Join(f.cacheDir, channel)
	cachePath := filepath.Join(channelDir, fmt.Sprintf("%s.yaml", version))
	metaPath := filepath.Join(channelDir, fmt.Sprintf("%s.meta.json", version))
	return cachePath, metaPath
}

func (f *Fetcher) readMetadata(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}

	var meta cacheMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return time.Time{}, err
	}

	return meta.FetchedAt, nil
}

func (f *Fetcher) writeMetadata(path string, fetchedAt time.Time) error {
	meta := cacheMetadata{FetchedAt: fetchedAt}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func normalizeVersion(version string) string {
	if version == "" || version == "latest" {
		return "latest"
	}
	if version == "stable" || version == "rc" {
		return version
	}
	if strings.HasPrefix(version, "v") {
		return version
	}
	if looksLikeSemver(version) {
		return "v" + version
	}
	return version
}

func looksLikeSemver(version string) bool {
	semverPattern := regexp.MustCompile(`^\d+\.\d+\.\d+`)
	return semverPattern.MatchString(version)
}

func shouldRetryStatus(status int) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return status >= http.StatusInternalServerError
}

// fetchFromLocal loads a manifest from local filesystem
func (f *Fetcher) fetchFromLocal(channel, version string) (*Manifest, error) {
	// For local paths, support two patterns:
	// 1. /path/to/gitops/releases/vX.Y.Z.yaml (specific version)
	// 2. /path/to/gitops/channels/stable.yaml (channel pointer)

	var manifestPath string

	if version == "latest" {
		// Try channel file first
		manifestPath = filepath.Join(f.repository, "channels", channel+".yaml")
		if _, err := os.Stat(manifestPath); err != nil {
			// Fallback to releases directory, find latest
			releasesDir := filepath.Join(f.repository, "releases")
			files, err := os.ReadDir(releasesDir)
			if err != nil {
				return nil, fmt.Errorf("failed to read releases directory: %w", err)
			}

			// Find latest release (simple heuristic: last file alphabetically)
			var latestFile string
			var releaseFiles []string
			for _, file := range files {
				if !file.IsDir() && filepath.Ext(file.Name()) == ".yaml" {
					releaseFiles = append(releaseFiles, file.Name())
				}
			}

			if len(releaseFiles) > 0 {
				sort.Strings(releaseFiles)
				latestFile = releaseFiles[len(releaseFiles)-1]
			}

			if latestFile == "" {
				return nil, fmt.Errorf("no release manifests found in %s", releasesDir)
			}

			manifestPath = filepath.Join(releasesDir, latestFile)
		}
	} else {
		// Specific version
		manifestPath = filepath.Join(f.repository, "releases", version+".yaml")
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest %s: %w", manifestPath, err)
	}

	var manifest Manifest
	if unmarshalErr := yaml.Unmarshal(data, &manifest); unmarshalErr != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", unmarshalErr)
	}

	return &manifest, nil
}
