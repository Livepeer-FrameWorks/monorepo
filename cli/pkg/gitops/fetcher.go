package gitops

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultRepository = "https://raw.githubusercontent.com/Livepeer-FrameWorks/gitops/main"
	DefaultCacheDir   = "~/.frameworks/cache/manifests"
)

// Fetcher fetches and caches manifests from the gitops repository
type Fetcher struct {
	repository string
	cacheDir   string
	client     *http.Client
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

	return &Fetcher{
		repository: repo,
		cacheDir:   cacheDir,
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
	if version == "" || version == "latest" {
		version = "latest"
	}

	// Check cache first
	cached, err := f.loadFromCache(channel, version)
	if err == nil {
		return cached, nil
	}

	// Check if repository is a local path
	if f.isLocalPath(f.repository) {
		manifest, err := f.fetchFromLocal(channel, version)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch from local path: %w", err)
		}
		return manifest, nil
	}

	// Fetch from repository
	manifest, err := f.fetchFromRepo(channel, version)
	if err != nil {
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

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest not found: %s (HTTP %d)", url, resp.StatusCode)
	}

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

// loadFromCache loads a manifest from local cache
func (f *Fetcher) loadFromCache(channel, version string) (*Manifest, error) {
	cachePath := filepath.Join(f.cacheDir, channel, fmt.Sprintf("%s.yaml", version))

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}

	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	return &manifest, nil
}

// saveToCache saves a manifest to local cache
func (f *Fetcher) saveToCache(channel, version string, manifest *Manifest) error {
	channelDir := filepath.Join(f.cacheDir, channel)
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		return err
	}

	cachePath := filepath.Join(channelDir, fmt.Sprintf("%s.yaml", version))

	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}

	return os.WriteFile(cachePath, data, 0644)
}

// ResolveVersion resolves a version string to channel and version
func ResolveVersion(versionStr string) (channel, version string) {
	// If it looks like a version tag (v1.2.3), use stable channel
	if len(versionStr) > 0 && versionStr[0] == 'v' {
		return "stable", versionStr
	}

	// If it's a channel name (stable, rc), use latest
	switch versionStr {
	case "stable", "rc":
		return versionStr, "latest"
	default:
		// Default to stable channel with specific version
		return "stable", versionStr
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
			for _, file := range files {
				if !file.IsDir() && filepath.Ext(file.Name()) == ".yaml" {
					latestFile = file.Name()
				}
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
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	return &manifest, nil
}
