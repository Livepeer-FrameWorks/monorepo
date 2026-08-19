package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"frameworks/cli/internal/releases"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
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

// FetchFromRepositories tries each repository in order until one returns a
// manifest. Callers can provide a preferred local/public gitops source first
// and still fall back to the default upstream repository.
func FetchFromRepositories(opts FetchOptions, repositories []string, channel, version string) (*Manifest, error) {
	repos := dedupeRepositories(repositories)
	if len(repos) == 0 {
		if opts.Repository != "" {
			repos = []string{opts.Repository}
		} else {
			repos = []string{DefaultRepository}
		}
	}

	var errs []string
	for _, repo := range repos {
		repoOpts := opts
		repoOpts.Repository = repo

		fetcher, err := NewFetcher(repoOpts)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: create fetcher: %v", repo, err))
			continue
		}

		manifest, err := fetcher.Fetch(channel, version)
		if err == nil {
			return manifest, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", repo, err))
	}

	return nil, fmt.Errorf("failed to fetch manifest from configured gitops repositories: %s", strings.Join(errs, "; "))
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
		// An identity-mismatched cache entry is unusable — treat it as a miss so a later re-fetch replaces it, rather
		// than serving a manifest whose release identity does not match what was requested.
		if idErr := bindManifestIdentity(cached, version); idErr != nil {
			cacheErr = idErr
		}
	}
	if cacheErr == nil {
		if validationErr := cached.ValidateServiceArtifacts(); validationErr != nil {
			cacheErr = validationErr
		} else {
			age := time.Since(cachedAt)
			if age <= ttl {
				return cached, nil
			}
			if f.offline && age <= maxStale {
				return cached, nil
			}
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
		if err := manifest.ValidateServiceArtifacts(); err != nil {
			return nil, fmt.Errorf("invalid local release manifest: %w", err)
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
	if err := manifest.ValidateServiceArtifacts(); err != nil {
		return nil, fmt.Errorf("invalid release manifest: %w", err)
	}

	// Save to cache
	if err := f.saveToCache(channel, version, manifest); err != nil {
		// Non-fatal - just log
		fmt.Printf("Warning: failed to cache manifest: %v\n", err)
	}

	return manifest, nil
}

// channelPointer represents a channel file that points to a specific release manifest.
type channelPointer struct {
	PlatformVersion string    `yaml:"platform_version"`
	Manifest        string    `yaml:"manifest"` // relative path, e.g. "releases/v0.1.0-rc2.yaml"
	UpdatedAt       time.Time `yaml:"updated_at"`
}

// fetchFromRepo downloads a manifest from the gitops repository.
// For "latest": resolves the channel pointer (channels/{channel}.yaml), then fetches the release manifest it references.
// For pinned versions: fetches releases/{version}.yaml directly.
func (f *Fetcher) fetchFromRepo(channel, version string) (*Manifest, error) {
	if version == "latest" {
		pointerURL := fmt.Sprintf("%s/channels/%s.yaml", f.repository, channel)
		pointerData, err := f.fetchHTTP(pointerURL)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch %s channel pointer: %w", channel, err)
		}
		pointer, err := parseChannelPointer(pointerData)
		if err != nil {
			return nil, fmt.Errorf("channel %q pointer: %w", channel, err)
		}
		manifestURL := fmt.Sprintf("%s/%s", f.repository, pointer.Manifest)
		m, err := f.fetchManifestHTTP(manifestURL)
		if err != nil {
			return nil, err
		}
		if err := bindManifestIdentity(m, pointer.PlatformVersion); err != nil {
			return nil, fmt.Errorf("channel %q pointer/manifest identity mismatch: %w", channel, err)
		}
		return m, nil
	}

	manifestURL := fmt.Sprintf("%s/releases/%s.yaml", f.repository, version)
	m, err := f.fetchManifestHTTP(manifestURL)
	if err != nil {
		return nil, err
	}
	if err := bindManifestIdentity(m, version); err != nil {
		return nil, fmt.Errorf("pinned release %q identity mismatch: %w", version, err)
	}
	return m, nil
}

// fetchHTTP downloads raw bytes from a URL with retries.
func (f *Fetcher) fetchHTTP(url string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= f.retryCount; attempt++ {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build request: %w", err)
		}

		resp, err := f.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to download %s: %w", url, err)
		} else {
			data, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("fetch failed: %s (HTTP %d)", url, resp.StatusCode)
				if !shouldRetryStatus(resp.StatusCode) {
					return nil, lastErr
				}
			} else if readErr != nil {
				return nil, fmt.Errorf("failed to read response from %s: %w", url, readErr)
			} else {
				return data, nil
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

// parseManifest is the SINGLE strict entry point every manifest-loading path (HTTP, cache, local) goes through.
// Decoding is STRICT (KnownFields), so a misspelled compatibility field — min_cli_verison, rollback_disabld,
// required_transition — is a HARD ERROR instead of a silently dropped safety gate. The fetched manifest is the
// primary compatibility authority, so a typo must never quietly remove min_cli_version, required_transitions, or
// rollback_disabled. It also enforces a SINGLE-DOCUMENT contract: empty input and any trailing `---` document are
// rejected, so compatibility metadata cannot hide in a second document the loader would ignore. (ExternalDependency
// preserves its own unknown fields via an inline map, so forward-compatible dependency evolution still parses.) After
// decoding, the compatibility metadata itself is validated.
func parseManifest(data []byte) (*Manifest, error) {
	var manifest Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&manifest); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("failed to parse manifest: empty document")
		}
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}
	// Reject trailing YAML documents: a second `---` document (e.g. one carrying compatibility/rollback metadata) would
	// be silently ignored and never gate deployment. Only a single document is a valid manifest.
	if err := dec.Decode(new(Manifest)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid release manifest: unexpected trailing YAML document (the manifest must be a single document)")
		}
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}
	if err := validateManifestCompat(&manifest); err != nil {
		return nil, fmt.Errorf("invalid release manifest: %w", err)
	}
	return &manifest, nil
}

// validateManifestCompat fails closed on corrupt compatibility metadata: a min_cli_version that is present but not a
// valid version (so the floor can never be compared), an empty required-transition id, or a rollback-disabled name that
// is empty OR is not a canonical service — a valid-looking typo such as `chandlr` would silently never match
// IsRollbackDisabled("chandler") and re-enable rollback across a readiness-contract cut. The fetched manifest is the
// deployment authority, so it validates names INDEPENDENTLY of release generation. platform_version is NOT
// format-checked here — callers accept channel-ish sentinels for it — but the SAFETY fields must be well-formed.
func validateManifestCompat(m *Manifest) error {
	if v := strings.TrimSpace(m.MinCLIVersion); v != "" {
		if err := releases.ValidateVersion(v); err != nil {
			return fmt.Errorf("min_cli_version %w", err)
		}
	}
	for _, id := range m.RequiredTransitions {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("required_transitions contains an empty transition id")
		}
		if strings.TrimSpace(id) != id {
			return fmt.Errorf("required_transitions entry %q has surrounding whitespace; store the canonical id %q", id, strings.TrimSpace(id))
		}
	}
	for _, name := range m.RollbackDisabled {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return fmt.Errorf("rollback_disabled contains an empty deploy name")
		}
		// Reject non-canonical whitespace: the stored slice is matched EXACTLY by IsRollbackDisabled, so a value like
		// " chandler " would pass a trimmed lookup here yet never match "chandler" at deploy — silently re-enabling
		// rollback across a readiness-contract cut. Require the stored value to already be canonical.
		if trimmed != name {
			return fmt.Errorf("rollback_disabled entry %q has surrounding whitespace; store the canonical name %q", name, trimmed)
		}
		if _, ok := servicedefs.Lookup(trimmed); !ok {
			return fmt.Errorf("rollback_disabled names unknown service %q; every entry must be a canonical service/deploy id", trimmed)
		}
	}
	return nil
}

// parseChannelPointer strictly decodes a channels/<name>.yaml pointer. Decoding is STRICT (KnownFields) and
// single-document, so a misspelled key — platform_verison — is a HARD ERROR rather than a silently-empty
// platform_version that would make bindManifestIdentity accept any concrete manifest. The pointer MUST declare a valid
// concrete platform_version and a manifest path; both are required before the referenced manifest is bound to it.
func parseChannelPointer(data []byte) (channelPointer, error) {
	var p channelPointer
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		if errors.Is(err, io.EOF) {
			return p, fmt.Errorf("empty channel pointer")
		}
		return p, fmt.Errorf("parse channel pointer: %w", err)
	}
	if err := dec.Decode(new(channelPointer)); !errors.Is(err, io.EOF) {
		if err == nil {
			return p, fmt.Errorf("channel pointer has an unexpected trailing YAML document")
		}
		return p, fmt.Errorf("parse channel pointer: %w", err)
	}
	if err := releases.ValidateVersion(p.PlatformVersion); err != nil {
		return p, fmt.Errorf("channel pointer platform_version %w", err)
	}
	if strings.TrimSpace(p.Manifest) == "" {
		return p, fmt.Errorf("channel pointer names no manifest")
	}
	return p, nil
}

// bindManifestIdentity verifies a fetched manifest actually IS the release it was requested under. Its platform_version
// must be a CONCRETE version (never a channel-ish sentinel like "cached" or empty), and — when a specific version was
// requested (a pinned tag, or the version a channel pointer declares) — must equal it EXACTLY. Without this binding,
// one release's migrations/transitions could run while consuming artifacts and rollback policy from a manifest that is
// actually a DIFFERENT release. want == "" or "latest" means no concrete identity was requested (only the
// concrete-platform_version requirement applies).
func bindManifestIdentity(m *Manifest, want string) error {
	if err := releases.ValidateVersion(m.PlatformVersion); err != nil {
		return fmt.Errorf("platform_version %w", err)
	}
	if want == "" || want == "latest" {
		return nil
	}
	if err := releases.ValidateVersion(want); err != nil {
		return fmt.Errorf("requested release %w", err)
	}
	// EXACT identity, NOT SemVer precedence: precedence ignores build metadata, so v1.2.3+buildA would otherwise bind
	// to a manifest declaring v1.2.3+buildB. Both values are validated-canonical, so compare byte-for-byte.
	if m.PlatformVersion != want {
		return fmt.Errorf("platform_version %q does not match the requested release %q", m.PlatformVersion, want)
	}
	return nil
}

// fetchManifestHTTP downloads and parses a manifest YAML from a URL.
func (f *Fetcher) fetchManifestHTTP(url string) (*Manifest, error) {
	data, err := f.fetchHTTP(url)
	if err != nil {
		return nil, err
	}
	return parseManifest(data)
}

// loadFromCache loads a manifest from local cache
func (f *Fetcher) loadFromCache(channel, version string) (*Manifest, time.Time, error) {
	cachePath, metaPath := f.cachePaths(channel, version)

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, time.Time{}, err
	}

	manifest, err := parseManifest(data)
	if err != nil {
		return nil, time.Time{}, err
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

	return manifest, fetchedAt, nil
}

// saveToCache saves a manifest to local cache
func (f *Fetcher) saveToCache(channel, version string, manifest *Manifest) error {
	cachePath, metaPath := f.cachePaths(channel, version)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}

	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		return err
	}

	return f.writeMetadata(metaPath, time.Now().UTC())
}

// ResolveVersion resolves a version string to channel and version. A concrete tag's channel is derived from its
// SemVer PRERELEASE identifier (vX.Y.Z-rc1 → "rc"; a plain vX.Y.Z → "stable"), so a release candidate is fetched and
// published under the rc channel, never mislabeled stable.
func ResolveVersion(versionStr string) (channel, version string) {
	if versionStr == "" {
		return "stable", "latest"
	}

	// A concrete version tag (v1.2.3 / v1.2.3-rc1): classify by prerelease.
	if versionStr[0] == 'v' {
		return channelForTag(versionStr), versionStr
	}

	// A release track name uses that channel's latest.
	switch versionStr {
	case "stable", "rc":
		return versionStr, "latest"
	case "latest":
		return "stable", "latest"
	default:
		nv := normalizeVersion(versionStr)
		return channelForTag(nv), nv
	}
}

// channelForTag maps a concrete SemVer tag to its release channel by its prerelease identifier. A prerelease
// (the segment after '-', excluding build metadata after '+') means the rc channel; a plain release tag is stable.
func channelForTag(tag string) string {
	// Strip build metadata (+...), which is not prerelease.
	if plus := strings.IndexByte(tag, '+'); plus >= 0 {
		tag = tag[:plus]
	}
	if dash := strings.IndexByte(tag, '-'); dash >= 0 && dash < len(tag)-1 {
		return "rc"
	}
	return "stable"
}

// GetServiceInfo retrieves service information from a manifest
func (m *Manifest) GetServiceInfo(serviceName string) (*ServiceInfo, error) {
	// Search in services
	for _, svc := range m.Services {
		if svc.Name == serviceName {
			// service_version is the artefact provenance label and is
			// trusted as written: supported release manifests stamp the
			// platform tag here, and carry-forward entries preserve the
			// baseline's value verbatim. The only
			// defensive fallback is when the field is literally empty,
			// which would mean a malformed manifest.
			version := strings.TrimSpace(svc.ServiceVersion)
			if version == "" {
				version = strings.TrimSpace(m.PlatformVersion)
			}
			info := &ServiceInfo{
				Name:      svc.Name,
				Version:   version,
				Image:     svc.Image,
				Digest:    svc.Digest,
				Images:    svc.Images,
				FullImage: fmt.Sprintf("%s@%s", svc.Image, svc.Digest),
				Binaries:  make(map[string]Artifact),
			}
			m.populateBinaries(info)
			return info, nil
		}
	}

	// Search in interfaces
	for _, iface := range m.Interfaces {
		if iface.Name == serviceName {
			// service_version is the interface's artefact provenance and is trusted as written: a freshly built
			// interface stamps the platform tag, a carried-forward one preserves the OLDER baseline value verbatim
			// (never relabeled with this platform tag). Fall back to the platform version only when the field is
			// literally empty — a manifest predating the field — so the deployed version is never blank (an empty
			// version would make image resolution fall back to latest/stable). The exact image@digest still travels
			// via FullImage, so the deploy pins the release image regardless of the version label.
			version := strings.TrimSpace(iface.ServiceVersion)
			if version == "" {
				version = strings.TrimSpace(m.PlatformVersion)
			}
			return &ServiceInfo{
				Name:      iface.Name,
				Version:   version,
				Image:     iface.Image,
				Digest:    iface.Digest,
				Images:    iface.Images,
				FullImage: fmt.Sprintf("%s@%s", iface.Image, iface.Digest),
				Binaries:  make(map[string]Artifact),
			}, nil
		}
	}

	// Search in native_binaries for binary-only services (e.g., privateer)
	for _, nb := range m.NativeBinaries {
		if nb.Name == serviceName {
			info := &ServiceInfo{
				Name:     nb.Name,
				Binaries: make(map[string]Artifact),
			}
			m.populateBinaries(info)
			return info, nil
		}
	}

	return nil, fmt.Errorf("service %s not found in manifest", serviceName)
}

// populateBinaries fills ServiceInfo.Binaries from native_binaries, preserving
// URL + Checksum so callers can download with integrity verification.
func (m *Manifest) populateBinaries(info *ServiceInfo) {
	for _, nb := range m.NativeBinaries {
		if nb.Name == info.Name {
			for _, artifact := range nb.Artifacts {
				info.Binaries[artifact.Arch] = artifact
			}
			break
		}
	}
}

// GetBinary returns the full artifact record (URL + Checksum) for the os/arch,
// or an error if no entry matches.
func (s *ServiceInfo) GetBinary(os, arch string) (*Artifact, error) {
	key := fmt.Sprintf("%s-%s", os, arch)
	a, ok := s.Binaries[key]
	if !ok {
		return nil, fmt.Errorf("binary not available for %s", key)
	}
	return &a, nil
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
	channelDir := filepath.Join(f.cacheDir, repositoryCacheKey(f.repository), channel)
	cachePath := filepath.Join(channelDir, fmt.Sprintf("%s.yaml", version))
	metaPath := filepath.Join(channelDir, fmt.Sprintf("%s.meta.json", version))
	return cachePath, metaPath
}

func repositoryCacheKey(repository string) string {
	if repository == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(repository))
	return hex.EncodeToString(sum[:8])
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

func dedupeRepositories(repositories []string) []string {
	seen := make(map[string]struct{}, len(repositories))
	out := make([]string, 0, len(repositories))
	for _, repo := range repositories {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		if _, ok := seen[repo]; ok {
			continue
		}
		seen[repo] = struct{}{}
		out = append(out, repo)
	}
	return out
}

// fetchFromLocal loads a manifest from local filesystem
func (f *Fetcher) fetchFromLocal(channel, version string) (*Manifest, error) {
	var manifestPath, wantVersion string

	if version == "latest" {
		// The channel pointer is REQUIRED and authoritative (matching the HTTP path). A missing, malformed, or
		// manifest-less pointer fails CLOSED — there is NO directory-scan fallback: a scan cannot know the requested
		// channel (stable vs rc), so a missing stable pointer could deploy an RC (or vice versa), and lexical filename
		// order is not SemVer order. Pointer-less release selection is not supported.
		channelPath := filepath.Join(f.repository, "channels", channel+".yaml")
		data, readErr := os.ReadFile(channelPath)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				return nil, fmt.Errorf("channel pointer %s not found; a channel pointer is required (pointer-less release selection is not supported)", channelPath)
			}
			return nil, fmt.Errorf("read channel pointer %s: %w", channelPath, readErr)
		}
		pointer, err := parseChannelPointer(data)
		if err != nil {
			return nil, fmt.Errorf("channel pointer %s: %w", channelPath, err)
		}
		manifestPath = filepath.Join(f.repository, pointer.Manifest)
		wantVersion = pointer.PlatformVersion
	} else {
		manifestPath = filepath.Join(f.repository, "releases", version+".yaml")
		wantVersion = version
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest %s: %w", manifestPath, err)
	}
	m, err := parseManifest(data)
	if err != nil {
		return nil, err
	}
	if err := bindManifestIdentity(m, wantVersion); err != nil {
		return nil, fmt.Errorf("local manifest %s identity mismatch: %w", manifestPath, err)
	}
	return m, nil
}
