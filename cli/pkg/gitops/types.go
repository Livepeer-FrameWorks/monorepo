package gitops

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Manifest represents a release manifest from the gitops repo (actual CI/CD format)
type Manifest struct {
	PlatformVersion      string                `yaml:"platform_version"`
	GitCommit            string                `yaml:"git_commit"`
	ReleaseDate          time.Time             `yaml:"release_date"`
	Services             []ServiceEntry        `yaml:"services"`
	NativeBinaries       []NativeBinary        `yaml:"native_binaries"`
	Interfaces           []InterfaceEntry      `yaml:"interfaces"`
	Infrastructure       []InfrastructureEntry `yaml:"infrastructure"`
	ExternalDependencies []ExternalDependency  `yaml:"external_dependencies,omitempty"`
}

// ExternalDependency represents an external dependency (e.g. MistServer)
type ExternalDependency struct {
	Name       string           `yaml:"name"`
	Image      string           `yaml:"image,omitempty"`
	Digest     string           `yaml:"digest,omitempty"`
	ReleaseURL string           `yaml:"release_url,omitempty"`
	ReleaseTag string           `yaml:"release_tag,omitempty"`
	Binaries   []ExternalBinary `yaml:"binaries,omitempty"`
	Raw        map[string]any   `yaml:",inline"` // Preserve dependency fields this CLI does not consume.
}

// ExternalBinary represents a binary artifact for an external dependency.
// Checksum format is "<algo>:<hex>" with algo in {sha256, sha512}.
type ExternalBinary struct {
	Name      string `yaml:"name"`
	URL       string `yaml:"url,omitempty"`
	Checksum  string `yaml:"checksum,omitempty"`
	SizeBytes int64  `yaml:"size_bytes,omitempty"`
}

// ServiceEntry represents a single service in the manifest
type ServiceEntry struct {
	Name           string                   `yaml:"name"`
	ServiceVersion string                   `yaml:"service_version"`
	Image          string                   `yaml:"image"`
	Digest         string                   `yaml:"digest"`
	Images         map[string]RegistryImage `yaml:"images,omitempty"`
	SourceHash     string                   `yaml:"source_hash,omitempty"`
	CarriedFrom    string                   `yaml:"carried_from,omitempty"`
}

// RegistryImage is an alternate registry location for the same release image.
type RegistryImage struct {
	Image  string `yaml:"image"`
	Digest string `yaml:"digest"`
}

// NativeBinary represents native binaries for a service
type NativeBinary struct {
	Name        string     `yaml:"name"`
	SourceHash  string     `yaml:"source_hash,omitempty"`
	CarriedFrom string     `yaml:"carried_from,omitempty"`
	Artifacts   []Artifact `yaml:"artifacts"`
}

// Artifact represents a single binary artifact.
// Checksum format is "<algo>:<hex>" with algo in {sha256, sha512}.
type Artifact struct {
	Arch      string `yaml:"arch"` // linux-amd64, linux-arm64, etc.
	File      string `yaml:"file"` // filename
	URL       string `yaml:"url,omitempty"`
	Checksum  string `yaml:"checksum,omitempty"`
	SizeBytes int64  `yaml:"size_bytes,omitempty"`
}

// InterfaceEntry represents an interface service (chartroom, foredeck)
type InterfaceEntry struct {
	Name         string                   `yaml:"name"`
	Image        string                   `yaml:"image"`
	Digest       string                   `yaml:"digest"`
	StaticBundle string                   `yaml:"static_bundle,omitempty"`
	Images       map[string]RegistryImage `yaml:"images,omitempty"`
	SourceHash   string                   `yaml:"source_hash,omitempty"`
	CarriedFrom  string                   `yaml:"carried_from,omitempty"`
}

// InfrastructureEntry represents an infrastructure component pinned by a
// platform release. Artifacts is populated for components that raw-download
// a tarball/binary instead of pulling from an OS package manager or Docker
// registry. Digest is the OCI manifest digest for content-addressed Docker
// pulls; when present, the resolver returns image@digest.
type InfrastructureEntry struct {
	Name      string     `yaml:"name"`
	Version   string     `yaml:"version"`
	Image     string     `yaml:"image,omitempty"`
	Digest    string     `yaml:"digest,omitempty"`
	Artifacts []Artifact `yaml:"artifacts,omitempty"`
}

// GetInfrastructure looks up an infrastructure entry by name.
func (m *Manifest) GetInfrastructure(name string) *InfrastructureEntry {
	for i := range m.Infrastructure {
		if m.Infrastructure[i].Name == name {
			return &m.Infrastructure[i]
		}
	}
	return nil
}

// GetArtifact returns the artifact for the given arch (linux-amd64, linux-arm64, ...),
// or nil if the infra entry has no artifact for that arch.
func (e *InfrastructureEntry) GetArtifact(arch string) *Artifact {
	for i := range e.Artifacts {
		if e.Artifacts[i].Arch == arch {
			return &e.Artifacts[i]
		}
	}
	return nil
}

// ServiceInfo holds release information for a service (helper struct).
// Binaries is keyed by arch and preserves URL + Checksum from the manifest so
// callers can do integrity-verified downloads.
type ServiceInfo struct {
	Name      string
	Version   string
	Image     string
	Digest    string
	Images    map[string]RegistryImage
	Binaries  map[string]Artifact
	FullImage string
}

// ValidateServiceArtifacts verifies each service's native artifacts match that
// service's own release identity. Carry-forward may intentionally leave
// different services at different versions; dependency-aware release hashing
// decides when a service must rebuild.
func (m *Manifest) ValidateServiceArtifacts() error {
	if m == nil {
		return nil
	}
	if !supportsServiceArtifactValidation(m.PlatformVersion) {
		return nil
	}
	for _, svc := range m.Services {
		version := strings.TrimSpace(svc.ServiceVersion)
		if version == "" {
			version = strings.TrimSpace(m.PlatformVersion)
		}
		if err := m.validateNativeBinaryVersion(svc.Name, version); err != nil {
			return err
		}
	}
	return nil
}

func supportsServiceArtifactValidation(version string) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return true
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	patch, patchErr := strconv.Atoi(parts[2])
	if majorErr != nil || minorErr != nil || patchErr != nil {
		return true
	}
	if major != 0 {
		return major > 0
	}
	if minor != 2 {
		return minor > 2
	}
	return patch >= 40
}

// ValidateServiceCohorts is kept for callers compiled against the old method
// name. It no longer enforces cross-service version equality.
func (m *Manifest) ValidateServiceCohorts() error {
	return m.ValidateServiceArtifacts()
}

func (m *Manifest) validateNativeBinaryVersion(name, version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	for _, nb := range m.NativeBinaries {
		if nb.Name != name {
			continue
		}
		for _, artifact := range nb.Artifacts {
			if artifact.URL == "" || strings.Contains(artifact.URL, version) {
				continue
			}
			return fmt.Errorf("release manifest %s has %s native artifact %s outside service version %s", m.PlatformVersion, name, artifact.URL, version)
		}
	}
	return nil
}

// GetExternalDependency looks up an external dependency by name (e.g., "mistserver", "caddy").
func (m *Manifest) GetExternalDependency(name string) *ExternalDependency {
	for i := range m.ExternalDependencies {
		if m.ExternalDependencies[i].Name == name {
			return &m.ExternalDependencies[i]
		}
	}
	return nil
}

// GetBinary returns the full binary record for the given os-arch key, or nil
// if the dependency carries no artifact for that arch.
func (d *ExternalDependency) GetBinary(arch string) *ExternalBinary {
	for i := range d.Binaries {
		if d.Binaries[i].Name == arch {
			return &d.Binaries[i]
		}
	}
	return nil
}

// RuntimeBinaryForPlatform returns the runtime (non-debug) binary tarball
// whose asset name carries the given "<os>-<arch>" token. External
// dependency binaries keep their full release asset filenames (e.g.
// mistserver-linux-amd64-<tag>.tar.gz), so exact-name lookups like
// GetBinary("linux-amd64") do not match them; this is the canonical way to
// resolve a platform's runtime artifact. Only ".tar.gz" assets qualify
// (mirroring the CI selector), so checksum/SBOM/signature sidecar assets
// that embed the platform token are never mistaken for the binary.
func (d *ExternalDependency) RuntimeBinaryForPlatform(platform string) *ExternalBinary {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return nil
	}
	for i := range d.Binaries {
		bin := &d.Binaries[i]
		name := strings.ToLower(strings.TrimSpace(bin.Name))
		if bin.URL == "" || IsDebugAssetName(name) {
			continue
		}
		if name == platform {
			return bin
		}
		if strings.HasSuffix(name, ".tar.gz") && strings.Contains(name, "-"+platform+"-") {
			return bin
		}
	}
	return nil
}

// IsDebugAssetName reports whether a release asset name is a debug-symbol
// artifact rather than a runtime binary.
func IsDebugAssetName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(lower, "-debug-") ||
		strings.HasSuffix(lower, "-debug.tar.gz") ||
		strings.HasSuffix(lower, ".debug") ||
		strings.Contains(lower, "/debug/")
}

// FetchOptions configures manifest fetching
type FetchOptions struct {
	Channel        string        // stable | rc
	Version        string        // v1.2.3 | latest
	CacheDir       string        // Local cache directory
	Offline        bool          // Use cache only, don't fetch
	Repository     string        // GitOps repository URL (or local path)
	LatestTTL      time.Duration // Cache TTL for latest (channel) manifests
	LatestMaxStale time.Duration // Maximum staleness for latest manifests
	PinnedTTL      time.Duration // Cache TTL for pinned versions
	PinnedMaxStale time.Duration // Maximum staleness for pinned versions
	RetryCount     int           // Retry attempts for remote fetch
	RetryDelay     time.Duration // Base retry delay for remote fetch
}
