package gitops

import "time"

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
	Raw        map[string]any   `yaml:",inline"` // Catch any additional fields
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
}

// RegistryImage is an alternate registry location for the same release image.
type RegistryImage struct {
	Image  string `yaml:"image"`
	Digest string `yaml:"digest"`
}

// NativeBinary represents native binaries for a service
type NativeBinary struct {
	Name      string     `yaml:"name"`
	Artifacts []Artifact `yaml:"artifacts"`
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
}

// InfrastructureEntry represents an infrastructure component pinned by a
// platform release. Artifacts is populated for components that raw-download
// a tarball/binary instead of pulling from an OS package manager or Docker
// registry.
type InfrastructureEntry struct {
	Name      string     `yaml:"name"`
	Version   string     `yaml:"version"`
	Image     string     `yaml:"image,omitempty"`
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
